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

	"ok-gobot/internal/storage"
)

// JobStatus is the durable lifecycle state of a background job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusSucceeded JobStatus = "succeeded"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
	JobStatusTimedOut  JobStatus = "timed_out"
)

// JobEventType is the persisted event stream for a job.
type JobEventType string

const (
	JobEventCreated         JobEventType = "created"
	JobEventStarted         JobEventType = "started"
	JobEventProgress        JobEventType = "progress"
	JobEventSucceeded       JobEventType = "succeeded"
	JobEventFailed          JobEventType = "failed"
	JobEventCancelRequested JobEventType = "cancel_requested"
	JobEventCancelled       JobEventType = "cancelled"
	JobEventTimedOut        JobEventType = "timed_out"
	JobEventRetryRequested  JobEventType = "retry_requested"
	JobEventArtifactAdded   JobEventType = "artifact_added"
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
	Attempt            int
	MaxAttempts        int
	Timeout            time.Duration
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
	Summary   string
	Artifacts []JobArtifactSpec
}

// JobRunner executes one durable job.
type JobRunner func(context.Context, *storage.Job, *JobService) (JobRunResult, error)

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

	job, err := s.createJob(spec)
	if err != nil {
		return nil, err
	}

	go s.run(parentCtx, job, spec.Timeout, runner)
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
	if existing.MaxAttempts > 0 && existing.Attempt >= existing.MaxAttempts {
		return nil, fmt.Errorf("job %q reached max attempts (%d)", jobID, existing.MaxAttempts)
	}

	retryJob, err := s.StartDetached(parentCtx, JobSpec{
		Kind:               existing.Kind,
		Worker:             existing.Worker,
		SessionKey:         existing.SessionKey,
		DeliverySessionKey: existing.DeliverySessionKey,
		RetryOfJobID:       existing.JobID,
		Description:        existing.Description,
		Attempt:            existing.Attempt + 1,
		MaxAttempts:        existing.MaxAttempts,
		Timeout:            time.Duration(existing.TimeoutSeconds) * time.Second,
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

// AddArtifact persists one durable artifact and emits a matching artifact event.
func (s *JobService) AddArtifact(jobID string, artifact JobArtifactSpec) error {
	metaJSON, err := marshalPayload(artifact.Metadata)
	if err != nil {
		return err
	}
	if err := s.store.AddJobArtifact(storage.JobArtifact{
		JobID:        strings.TrimSpace(jobID),
		Name:         artifact.Name,
		ArtifactType: artifact.Type,
		MimeType:     artifact.MimeType,
		Content:      artifact.Content,
		URI:          artifact.URI,
		Metadata:     metaJSON,
	}); err != nil {
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
	}); err != nil {
		return nil, err
	}

	if err := s.AppendEvent(jobID, JobEventCreated, spec.Description, map[string]any{
		"kind":                 strings.TrimSpace(spec.Kind),
		"worker":               strings.TrimSpace(spec.Worker),
		"delivery_session_key": strings.TrimSpace(spec.DeliverySessionKey),
		"retry_of_job_id":      strings.TrimSpace(spec.RetryOfJobID),
		"attempt":              attempt,
		"max_attempts":         maxAttempts,
		"timeout_seconds":      timeoutSeconds,
	}); err != nil {
		return nil, err
	}

	return s.store.GetJob(jobID)
}

func (s *JobService) run(parentCtx context.Context, job *storage.Job, timeout time.Duration, runner JobRunner) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parentCtx, timeout)
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
	// Persist artifacts regardless of success/failure so that failed exec output
	// (stdout/stderr) is retained in the durable record for post-mortem inspection.
	for _, artifact := range result.Artifacts {
		if err := s.AddArtifact(job.JobID, artifact); err != nil {
			log.Printf("[jobs] failed to persist artifact %q for %s: %v", artifact.Name, job.JobID, err)
			if runErr == nil {
				runErr = fmt.Errorf("persist artifact %q: %w", artifact.Name, err)
			}
			break
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

	switch {
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
