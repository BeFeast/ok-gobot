package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	artifactview "ok-gobot/internal/artifacts"
	"ok-gobot/internal/delegation"
	"ok-gobot/internal/evidence"
	"ok-gobot/internal/storage"
)

// JobStatus is the durable lifecycle state of a background job.
type JobStatus string

const (
	JobStatusPending         JobStatus = "pending"
	JobStatusRunning         JobStatus = "running"
	JobStatusSucceeded       JobStatus = "succeeded"
	JobStatusFailed          JobStatus = "failed"
	JobStatusPreflightFailed JobStatus = "preflight_failed"
	JobStatusCancelled       JobStatus = "cancelled"
	JobStatusTimedOut        JobStatus = "timed_out"
	JobStatusBudgetExceeded  JobStatus = "budget_exceeded"
)

// JobEventType is the persisted event stream for a job.
type JobEventType string

const (
	JobEventCreated         JobEventType = "created"
	JobEventStarted         JobEventType = "started"
	JobEventProgress        JobEventType = "progress"
	JobEventSucceeded       JobEventType = "succeeded"
	JobEventFailed          JobEventType = "failed"
	JobEventPreflightFailed JobEventType = "preflight_failed"
	JobEventCancelRequested JobEventType = "cancel_requested"
	JobEventCancelled       JobEventType = "cancelled"
	JobEventTimedOut        JobEventType = "timed_out"
	JobEventRetryRequested  JobEventType = "retry_requested"
	JobEventArtifactAdded   JobEventType = "artifact_added"
	JobEventBudgetExceeded  JobEventType = "budget_exceeded"
)

// JobSpec describes a new durable background job.
type JobSpec struct {
	JobID              string
	Kind               string
	Worker             string
	SessionKey         string
	DeliverySessionKey string
	RetryOfJobID       string
	Description        string
	RoleName           string
	ModelTier          string
	Branch             string
	WorktreePath       string
	Attempt            int
	MaxAttempts        int
	Timeout            time.Duration
	MaxToolCalls       int
	ArtifactRoots      []string
	Preflight          func(context.Context) error
}

// JobArtifactSpec describes one durable artifact emitted by a job.
type JobArtifactSpec struct {
	Name     string
	Type     string
	MimeType string
	Content  string
	URI      string
	Metadata any
}

// JobRunResult is the structured outcome of a background job.
type JobRunResult struct {
	Summary       string
	Artifacts     []JobArtifactSpec
	ArtifactRoots []string
}

// JobRunner executes one durable job.
type JobRunner func(context.Context, *storage.Job, *JobService) (JobRunResult, error)

// PreflightFailure indicates that a worker was refused before any code attempt.
type PreflightFailure struct {
	Message string
	Details []string
}

func (e *PreflightFailure) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "preflight failed"
	}
	return e.Message
}

func (e *PreflightFailure) eventPayload() map[string]any {
	if e == nil || len(e.Details) == 0 {
		return nil
	}
	return map[string]any{"details": e.Details}
}

// NewPreflightFailure creates a classified preflight error for job runners.
func NewPreflightFailure(message string, details []string) error {
	return &PreflightFailure{Message: strings.TrimSpace(message), Details: details}
}

// JobService persists and tracks first-class background jobs.
type JobService struct {
	store *storage.Store

	mu     sync.Mutex
	active map[string]context.CancelFunc
}

// NewJobService creates a durable job service backed by SQLite storage.
func NewJobService(store *storage.Store) *JobService {
	return &JobService{
		store:  store,
		active: make(map[string]context.CancelFunc),
	}
}

// StartDetached creates a durable job record and executes it in a goroutine.
func (s *JobService) StartDetached(parentCtx context.Context, spec JobSpec, runner JobRunner) (*storage.Job, error) {
	if runner == nil {
		return nil, fmt.Errorf("job runner is required")
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	if spec.Preflight != nil {
		if err := spec.Preflight(parentCtx); err != nil {
			return nil, fmt.Errorf("job preflight failed: %w", err)
		}
	}

	job, err := s.createJob(spec)
	if err != nil {
		return nil, err
	}

	go s.run(parentCtx, job, spec, runner)
	return job, nil
}

// RetryDetached clones a completed job into a fresh durable retry attempt.
func (s *JobService) RetryDetached(parentCtx context.Context, jobID string, runner JobRunner) (*storage.Job, error) {
	existing, err := s.store.GetJob(jobID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("job %q not found", jobID)
	}

	switch existing.Status {
	case string(JobStatusPending), string(JobStatusRunning):
		return nil, fmt.Errorf("job %q is not retryable while status=%s", jobID, existing.Status)
	}
	preflightRetry := existing.Status == string(JobStatusPreflightFailed)
	if !preflightRetry && existing.MaxAttempts > 0 && existing.Attempt >= existing.MaxAttempts {
		return nil, fmt.Errorf("job %q reached max attempts (%d)", jobID, existing.MaxAttempts)
	}
	nextAttempt := existing.Attempt + 1
	if preflightRetry {
		nextAttempt = existing.Attempt
		if nextAttempt <= 0 {
			nextAttempt = 1
		}
	}

	retryJob, err := s.StartDetached(parentCtx, JobSpec{
		Kind:               existing.Kind,
		Worker:             existing.Worker,
		SessionKey:         existing.SessionKey,
		DeliverySessionKey: existing.DeliverySessionKey,
		RetryOfJobID:       existing.JobID,
		Description:        existing.Description,
		RoleName:           existing.RoleName,
		ModelTier:          existing.ModelTier,
		Attempt:            nextAttempt,
		MaxAttempts:        existing.MaxAttempts,
		Timeout:            time.Duration(existing.TimeoutSeconds) * time.Second,
		MaxToolCalls:       existing.MaxToolCalls,
	}, runner)
	if err != nil {
		return nil, err
	}

	if err := s.AppendEvent(existing.JobID, JobEventRetryRequested, fmt.Sprintf("retry queued as %s", retryJob.JobID), map[string]any{
		"retry_job_id": retryJob.JobID,
	}); err != nil {
		return nil, err
	}
	return retryJob, nil
}

// Cancel requests cancellation for a durable job and cancels any active context.
func (s *JobService) Cancel(jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job ID is required")
	}
	if err := s.store.UpdateJobCancelRequested(jobID, true); err != nil {
		return err
	}
	if err := s.AppendEvent(jobID, JobEventCancelRequested, "cancel requested", nil); err != nil {
		return err
	}

	cancel := s.lookupCancel(jobID)
	if cancel != nil {
		cancel()
		return nil
	}

	job, err := s.store.GetJob(jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("job %q not found", jobID)
	}
	if job.Status == string(JobStatusPending) {
		if err := s.store.MarkJobCancelled(jobID, "cancelled before start"); err != nil {
			return err
		}
		return s.AppendEvent(jobID, JobEventCancelled, "cancelled before start", nil)
	}
	return nil
}

// AppendEvent persists a job lifecycle event with an optional JSON payload.
func (s *JobService) AppendEvent(jobID string, eventType JobEventType, message string, payload any) error {
	payloadJSON, err := marshalPayload(payload)
	if err != nil {
		return err
	}
	return s.store.AddJobEvent(storage.JobEvent{
		JobID:     strings.TrimSpace(jobID),
		EventType: string(eventType),
		Message:   message,
		Payload:   payloadJSON,
	})
}

// AppendEvidence persists one structured evidence ledger entry for a job.
func (s *JobService) AppendEvidence(jobID, eventType, status, summary string, payload any) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("job storage is required")
	}
	payloadMap, err := evidence.PayloadMap(payload)
	if err != nil {
		return err
	}
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("job %q not found", jobID)
	}
	sessionKey := strings.TrimSpace(job.SessionKey)
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(job.DeliverySessionKey)
	}
	return s.store.AddEvidenceEvent(evidence.Event{
		SessionKey: sessionKey,
		JobID:      job.JobID,
		Type:       eventType,
		Status:     status,
		Summary:    summary,
		Payload:    payloadMap,
	})
}

// AddArtifact persists one durable artifact and emits a matching artifact event.
func (s *JobService) AddArtifact(jobID string, artifact JobArtifactSpec) error {
	jobID = strings.TrimSpace(jobID)
	row := storage.JobArtifact{
		JobID:        jobID,
		Name:         artifact.Name,
		ArtifactType: artifact.Type,
		MimeType:     artifact.MimeType,
		Content:      artifact.Content,
		URI:          artifact.URI,
	}
	createdAt := time.Now().UTC()
	producer := s.artifactProducer(jobID)
	metadata, err := artifactMetadataPayload(artifact.Metadata, artifactview.BuildMetadata(row, producer, createdAt))
	if err != nil {
		return err
	}
	metaJSON, err := marshalPayload(metadata)
	if err != nil {
		return err
	}
	row.Metadata = metaJSON
	if err := s.store.AddJobArtifact(row); err != nil {
		return err
	}
	return s.AppendEvent(jobID, JobEventArtifactAdded, artifact.Name, map[string]any{
		"name": artifact.Name,
		"type": artifact.Type,
		"uri":  artifact.URI,
	})
}

func (s *JobService) createJob(spec JobSpec) (*storage.Job, error) {
	if s.store == nil {
		return nil, fmt.Errorf("job storage is required")
	}

	if routeKey := strings.TrimSpace(spec.DeliverySessionKey); routeKey != "" {
		route, err := s.store.GetSessionRoute(routeKey)
		if err != nil {
			return nil, err
		}
		if route == nil {
			return nil, fmt.Errorf("delivery route %q not found", routeKey)
		}
	}

	jobID := strings.TrimSpace(spec.JobID)
	if jobID == "" {
		jobID = newJobID()
	}
	attempt := spec.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	maxAttempts := spec.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	timeoutSeconds := 0
	if spec.Timeout > 0 {
		timeoutSeconds = int(spec.Timeout / time.Second)
		if timeoutSeconds == 0 {
			timeoutSeconds = 1
		}
	}

	if err := s.store.CreateJob(storage.Job{
		JobID:              jobID,
		Kind:               strings.TrimSpace(spec.Kind),
		Worker:             strings.TrimSpace(spec.Worker),
		SessionKey:         strings.TrimSpace(spec.SessionKey),
		DeliverySessionKey: strings.TrimSpace(spec.DeliverySessionKey),
		RetryOfJobID:       strings.TrimSpace(spec.RetryOfJobID),
		Description:        spec.Description,
		Status:             string(JobStatusPending),
		Attempt:            attempt,
		MaxAttempts:        maxAttempts,
		TimeoutSeconds:     timeoutSeconds,
		MaxToolCalls:       spec.MaxToolCalls,
		RoleName:           strings.TrimSpace(spec.RoleName),
		ModelTier:          strings.TrimSpace(spec.ModelTier),
	}); err != nil {
		return nil, err
	}

	if err := s.AppendEvent(jobID, JobEventCreated, spec.Description, map[string]any{
		"kind":                 strings.TrimSpace(spec.Kind),
		"worker":               strings.TrimSpace(spec.Worker),
		"delivery_session_key": strings.TrimSpace(spec.DeliverySessionKey),
		"retry_of_job_id":      strings.TrimSpace(spec.RetryOfJobID),
		"role_name":            strings.TrimSpace(spec.RoleName),
		"model_tier":           strings.TrimSpace(spec.ModelTier),
		"attempt":              attempt,
		"max_attempts":         maxAttempts,
		"timeout_seconds":      timeoutSeconds,
	}); err != nil {
		return nil, err
	}

	hasEvidenceSession := strings.TrimSpace(spec.SessionKey) != "" || strings.TrimSpace(spec.DeliverySessionKey) != ""
	if hasEvidenceSession && (strings.TrimSpace(spec.Worker) != "" || strings.TrimSpace(spec.ModelTier) != "" || strings.TrimSpace(spec.RoleName) != "") {
		if err := s.AppendEvidence(jobID, evidence.EventBackendModel, "selected", "selected backend/model", map[string]any{
			"backend":    strings.TrimSpace(spec.Worker),
			"model_tier": strings.TrimSpace(spec.ModelTier),
			"role":       strings.TrimSpace(spec.RoleName),
		}); err != nil {
			log.Printf("[evidence] failed to append backend/model evidence for job %s: %v", jobID, err)
		}
	}
	if hasEvidenceSession && (strings.TrimSpace(spec.Branch) != "" || strings.TrimSpace(spec.WorktreePath) != "") {
		if err := s.AppendEvidence(jobID, evidence.EventWorkspace, "selected", "selected branch/worktree", map[string]any{
			"branch":        strings.TrimSpace(spec.Branch),
			"worktree_path": strings.TrimSpace(spec.WorktreePath),
		}); err != nil {
			log.Printf("[evidence] failed to append workspace evidence for job %s: %v", jobID, err)
		}
	}

	return s.store.GetJob(jobID)
}

func (s *JobService) run(parentCtx context.Context, job *storage.Job, spec JobSpec, runner JobRunner) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if spec.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parentCtx, spec.Timeout)
	} else {
		ctx, cancel = context.WithCancel(parentCtx)
	}
	defer cancel()

	s.registerCancel(job.JobID, cancel)
	defer s.unregisterCancel(job.JobID)

	if err := s.store.MarkJobRunning(job.JobID); err != nil {
		log.Printf("[jobs] failed to mark %s running: %v", job.JobID, err)
		return
	}
	if err := s.AppendEvent(job.JobID, JobEventStarted, job.Description, nil); err != nil {
		log.Printf("[jobs] failed to persist start event for %s: %v", job.JobID, err)
		return
	}

	result, runErr := runner(ctx, job, s)
	if runErr == nil {
		if isRoleJob(job) {
			artifactRoots := spec.ArtifactRoots
			if len(result.ArtifactRoots) > 0 {
				artifactRoots = result.ArtifactRoots
			}
			result.Artifacts = roleProofArtifacts(result, artifactRoots)
		}
		for _, artifact := range result.Artifacts {
			if err := s.AddArtifact(job.JobID, artifact); err != nil {
				runErr = fmt.Errorf("persist artifact %q: %w", artifact.Name, err)
				break
			}
		}
	}

	if runErr == nil {
		if err := s.store.MarkJobSucceeded(job.JobID, result.Summary); err != nil {
			log.Printf("[jobs] failed to mark %s succeeded: %v", job.JobID, err)
			return
		}
		if err := s.AppendEvent(job.JobID, JobEventSucceeded, result.Summary, nil); err != nil {
			log.Printf("[jobs] failed to persist success event for %s: %v", job.JobID, err)
		}
		return
	}

	cancelRequested := false
	if storedJob, err := s.store.GetJob(job.JobID); err == nil && storedJob != nil {
		cancelRequested = storedJob.CancelRequested
	}

	var budgetErr *delegation.BudgetExceededError
	var preflightErr *PreflightFailure

	switch {
	case errors.As(runErr, &preflightErr):
		if err := s.store.MarkJobPreflightFailed(job.JobID, preflightErr.Error()); err != nil {
			log.Printf("[jobs] failed to mark %s preflight_failed: %v", job.JobID, err)
			return
		}
		if err := s.AppendEvent(job.JobID, JobEventPreflightFailed, preflightErr.Error(), preflightErr.eventPayload()); err != nil {
			log.Printf("[jobs] failed to persist preflight event for %s: %v", job.JobID, err)
		}
	case errors.As(runErr, &budgetErr):
		summary := budgetErr.Report.Summary
		if summary == "" {
			summary = result.Summary
		}
		if err := s.store.MarkJobBudgetExceeded(job.JobID, summary, string(budgetErr.Reason)); err != nil {
			log.Printf("[jobs] failed to mark %s budget_exceeded: %v", job.JobID, err)
			return
		}
		if err := s.AppendEvent(job.JobID, JobEventBudgetExceeded, budgetErr.Error(), map[string]any{
			"limit_reason":    string(budgetErr.Reason),
			"tool_calls_used": budgetErr.Report.ToolCallsUsed,
			"tool_call_max":   budgetErr.Report.ToolCallMax,
		}); err != nil {
			log.Printf("[jobs] failed to persist budget event for %s: %v", job.JobID, err)
		}
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(runErr, context.DeadlineExceeded):
		if err := s.store.MarkJobTimedOut(job.JobID, runErr.Error()); err != nil {
			log.Printf("[jobs] failed to mark %s timed out: %v", job.JobID, err)
			return
		}
		if err := s.AppendEvent(job.JobID, JobEventTimedOut, runErr.Error(), nil); err != nil {
			log.Printf("[jobs] failed to persist timeout event for %s: %v", job.JobID, err)
		}
	case cancelRequested || errors.Is(ctx.Err(), context.Canceled) || errors.Is(runErr, context.Canceled):
		if err := s.store.MarkJobCancelled(job.JobID, runErr.Error()); err != nil {
			log.Printf("[jobs] failed to mark %s cancelled: %v", job.JobID, err)
			return
		}
		if err := s.AppendEvent(job.JobID, JobEventCancelled, runErr.Error(), nil); err != nil {
			log.Printf("[jobs] failed to persist cancel event for %s: %v", job.JobID, err)
		}
	default:
		if err := s.store.MarkJobFailed(job.JobID, runErr.Error()); err != nil {
			log.Printf("[jobs] failed to mark %s failed: %v", job.JobID, err)
			return
		}
		if err := s.AppendEvent(job.JobID, JobEventFailed, runErr.Error(), nil); err != nil {
			log.Printf("[jobs] failed to persist failure event for %s: %v", job.JobID, err)
		}
	}
}

func (s *JobService) registerCancel(jobID string, cancel context.CancelFunc) {
	s.mu.Lock()
	s.active[jobID] = cancel
	s.mu.Unlock()
}

func (s *JobService) unregisterCancel(jobID string) {
	s.mu.Lock()
	delete(s.active, jobID)
	s.mu.Unlock()
}

func (s *JobService) lookupCancel(jobID string) context.CancelFunc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[jobID]
}

func (s *JobService) artifactProducer(jobID string) string {
	if s != nil && s.store != nil && strings.TrimSpace(jobID) != "" {
		if job, err := s.store.GetJob(jobID); err == nil && job != nil {
			if roleName := strings.TrimSpace(job.RoleName); roleName != "" {
				return "role:" + roleName
			}
			if worker := strings.TrimSpace(job.Worker); worker != "" {
				return "worker:" + worker
			}
			if kind := strings.TrimSpace(job.Kind); kind != "" {
				return kind
			}
		}
	}
	if strings.TrimSpace(jobID) != "" {
		return "job:" + strings.TrimSpace(jobID)
	}
	return "job"
}

func artifactMetadataPayload(existing any, proof artifactview.Metadata) (map[string]any, error) {
	payload, err := metadataObject(existing)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		payload = make(map[string]any)
	}
	if proof.Kind != "" {
		payload["kind"] = proof.Kind
	}
	if proof.NormalizedPath != "" {
		payload["normalized_path"] = proof.NormalizedPath
	}
	if proof.SizeBytes != nil {
		payload["size_bytes"] = *proof.SizeBytes
	}
	if proof.SHA256 != "" {
		payload["sha256"] = proof.SHA256
	}
	if proof.Producer != "" {
		payload["producer"] = proof.Producer
	}
	if proof.CreatedAt != "" {
		payload["created_at"] = proof.CreatedAt
	}
	return payload, nil
}

func metadataObject(existing any) (map[string]any, error) {
	if existing == nil {
		return nil, nil
	}
	if raw, ok := existing.(string); ok {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, nil
		}
		var object map[string]any
		if err := json.Unmarshal([]byte(raw), &object); err == nil && object != nil {
			return object, nil
		}
		return map[string]any{"source_metadata": raw}, nil
	}
	if object, ok := existing.(map[string]any); ok {
		copy := make(map[string]any, len(object))
		for k, v := range object {
			copy[k] = v
		}
		return copy, nil
	}
	raw, err := json.Marshal(existing)
	if err != nil {
		return nil, fmt.Errorf("marshal artifact metadata: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil && object != nil {
		return object, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode artifact metadata: %w", err)
	}
	return map[string]any{"source_metadata": value}, nil
}

func marshalPayload(payload any) (string, error) {
	if payload == nil {
		return "", nil
	}
	if s, ok := payload.(string); ok {
		return s, nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	return string(b), nil
}

func newJobID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("job-%d-%x", time.Now().UnixNano(), b)
}
