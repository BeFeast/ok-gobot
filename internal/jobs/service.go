package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"ok-gobot/internal/storage"
	"ok-gobot/internal/workers"
)

// ArtifactRef is the operator-facing artifact emitted by a job.
type ArtifactRef struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	URI      string `json:"uri"`
	Metadata string `json:"metadata,omitempty"`
}

// InputPayload is the normalized job input sent to a worker backend.
type InputPayload struct {
	Prompt     string `json:"prompt"`
	Model      string `json:"model,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
}

// Result is the normalized stored output from a job.
type Result struct {
	Output    string        `json:"output"`
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
}

// LaunchRequest defines a new job request.
type LaunchRequest struct {
	SessionKey         string
	ChatID             int64
	CreatedByMessageID int64
	TaskType           string
	Summary            string
	RouterDecision     string
	WorkerBackend      string
	WorkerProfile      string
	Input              InputPayload
}

// Update is emitted when a job changes state.
type Update struct {
	JobID   int64
	Status  storage.JobStatus
	Message string
	Result  *Result
}

// UpdateHook is invoked after persisted job state changes.
type UpdateHook func(context.Context, *storage.JobRecord, Update)

// Service owns job persistence and background execution.
type Service struct {
	store    *storage.Store
	registry *workers.Registry
	onUpdate UpdateHook

	mu      sync.Mutex
	running map[int64]context.CancelFunc
}

// NewService creates a jobs service.
func NewService(store *storage.Store, registry *workers.Registry, onUpdate UpdateHook) *Service {
	return &Service{
		store:    store,
		registry: registry,
		onUpdate: onUpdate,
		running:  make(map[int64]context.CancelFunc),
	}
}

// Launch persists and starts a background job.
func (s *Service) Launch(ctx context.Context, req LaunchRequest) (*storage.JobRecord, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if s.registry == nil {
		return nil, fmt.Errorf("worker registry is required")
	}

	backend := strings.TrimSpace(req.WorkerBackend)
	if backend == "" {
		backend = s.registry.DefaultName()
	}
	if backend == "" {
		return nil, fmt.Errorf("no worker backend selected")
	}

	inputJSON, err := json.Marshal(req.Input)
	if err != nil {
		return nil, fmt.Errorf("marshal job input: %w", err)
	}

	jobID, err := s.store.CreateJob(&storage.JobRecord{
		SessionKey:         req.SessionKey,
		ChatID:             req.ChatID,
		CreatedByMessageID: req.CreatedByMessageID,
		Status:             storage.JobQueued,
		TaskType:           defaultString(req.TaskType, "job"),
		Summary:            req.Summary,
		RouterDecision:     req.RouterDecision,
		WorkerBackend:      backend,
		WorkerProfile:      req.WorkerProfile,
		InputPayload:       string(inputJSON),
	})
	if err != nil {
		return nil, err
	}
	if err := s.store.AddJobEvent(jobID, "queued", "job queued"); err != nil {
		return nil, err
	}

	job, err := s.store.GetJob(jobID)
	if err != nil {
		return nil, err
	}
	s.emit(ctx, job, Update{JobID: jobID, Status: storage.JobQueued, Message: "queued"})

	go s.runJob(jobID)

	return job, nil
}

// Get returns one job record.
func (s *Service) Get(jobID int64) (*storage.JobRecord, error) {
	return s.store.GetJob(jobID)
}

// List returns recent jobs.
func (s *Service) List(limit int, status storage.JobStatus) ([]storage.JobRecord, error) {
	return s.store.ListJobs(limit, status)
}

// Events returns the event log for a job.
func (s *Service) Events(jobID int64) ([]storage.JobEventRecord, error) {
	return s.store.ListJobEvents(jobID)
}

// Workers returns registered worker metadata.
func (s *Service) Workers() []workers.Info {
	if s.registry == nil {
		return nil
	}
	return s.registry.List()
}

// Cancel stops a running job or marks a queued one as cancelled.
func (s *Service) Cancel(jobID int64) error {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("job %d not found", jobID)
	}

	s.mu.Lock()
	cancel, ok := s.running[jobID]
	s.mu.Unlock()
	if ok {
		cancel()
		return nil
	}

	if job.Status == storage.JobQueued {
		if err := s.store.UpdateJobStatus(jobID, storage.JobCancelled, job.ResultPayload, "cancelled before start"); err != nil {
			return err
		}
		updated, _ := s.store.GetJob(jobID)
		s.store.AddJobEvent(jobID, "cancelled", "job cancelled before start") //nolint:errcheck
		s.emit(context.Background(), updated, Update{JobID: jobID, Status: storage.JobCancelled, Message: "cancelled"})
	}

	return nil
}

// Retry clones a finished job into a fresh queued job.
func (s *Service) Retry(ctx context.Context, jobID int64) (*storage.JobRecord, error) {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, fmt.Errorf("job %d not found", jobID)
	}

	var input InputPayload
	if err := json.Unmarshal([]byte(job.InputPayload), &input); err != nil {
		return nil, fmt.Errorf("decode job input: %w", err)
	}

	return s.Launch(ctx, LaunchRequest{
		SessionKey:         job.SessionKey,
		ChatID:             job.ChatID,
		CreatedByMessageID: job.CreatedByMessageID,
		TaskType:           job.TaskType,
		Summary:            job.Summary,
		RouterDecision:     job.RouterDecision,
		WorkerBackend:      job.WorkerBackend,
		WorkerProfile:      job.WorkerProfile,
		Input:              input,
	})
}

// CancelLatestForChat cancels the newest active job for a Telegram chat.
func (s *Service) CancelLatestForChat(chatID int64) error {
	job, err := s.store.GetLatestJobForChat(chatID)
	if err != nil {
		return err
	}
	if job == nil {
		return nil
	}
	if job.Status != storage.JobQueued && job.Status != storage.JobRunning && job.Status != storage.JobWaiting {
		return nil
	}
	return s.Cancel(job.ID)
}

func (s *Service) runJob(jobID int64) {
	job, err := s.store.GetJob(jobID)
	if err != nil || job == nil {
		return
	}

	adapter, ok := s.registry.Get(job.WorkerBackend)
	if !ok {
		s.failJob(job, fmt.Errorf("worker backend %q not registered", job.WorkerBackend))
		return
	}

	var input InputPayload
	if err := json.Unmarshal([]byte(job.InputPayload), &input); err != nil {
		s.failJob(job, fmt.Errorf("decode job input: %w", err))
		return
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.running[jobID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, jobID)
		s.mu.Unlock()
	}()

	if err := s.store.UpdateJobStatus(jobID, storage.JobRunning, job.ResultPayload, ""); err != nil {
		s.failJob(job, err)
		return
	}
	job, _ = s.store.GetJob(jobID)
	s.store.AddJobEvent(jobID, "running", "job started") //nolint:errcheck
	s.emit(runCtx, job, Update{JobID: jobID, Status: storage.JobRunning, Message: "running"})

	result, err := adapter.Run(runCtx, workers.RunRequest{
		Prompt:     input.Prompt,
		Model:      input.Model,
		WorkingDir: input.WorkingDir,
	}, func(update workers.RunUpdate) {
		msg := strings.TrimSpace(update.Message)
		if msg == "" {
			return
		}
		s.store.AddJobEvent(jobID, update.Kind, msg) //nolint:errcheck
		job, _ := s.store.GetJob(jobID)
		s.emit(runCtx, job, Update{JobID: jobID, Status: storage.JobRunning, Message: msg})
	})
	if err != nil {
		if runCtx.Err() != nil {
			if updateErr := s.store.UpdateJobStatus(jobID, storage.JobCancelled, "", "cancelled"); updateErr == nil {
				s.store.AddJobEvent(jobID, "cancelled", "job cancelled") //nolint:errcheck
				job, _ := s.store.GetJob(jobID)
				s.emit(context.Background(), job, Update{JobID: jobID, Status: storage.JobCancelled, Message: "cancelled"})
			}
			return
		}
		s.failJob(job, err)
		return
	}

	out := Result{Output: result.Output}
	for _, artifact := range result.Artifacts {
		out.Artifacts = append(out.Artifacts, ArtifactRef{
			Name:     artifact.Name,
			Kind:     artifact.Kind,
			URI:      artifact.URI,
			Metadata: artifact.Metadata,
		})
		s.store.AddJobArtifact(&storage.JobArtifactRecord{
			JobID:    jobID,
			Name:     artifact.Name,
			Kind:     artifact.Kind,
			URI:      artifact.URI,
			Metadata: artifact.Metadata,
		}) //nolint:errcheck
	}

	resultJSON, err := json.Marshal(out)
	if err != nil {
		s.failJob(job, fmt.Errorf("marshal job result: %w", err))
		return
	}
	if err := s.store.UpdateJobStatus(jobID, storage.JobDone, string(resultJSON), ""); err != nil {
		s.failJob(job, err)
		return
	}
	s.store.AddJobEvent(jobID, "done", "job completed") //nolint:errcheck
	job, _ = s.store.GetJob(jobID)
	s.emit(context.Background(), job, Update{
		JobID:   jobID,
		Status:  storage.JobDone,
		Message: "done",
		Result:  &out,
	})
}

func (s *Service) failJob(job *storage.JobRecord, err error) {
	if job == nil {
		return
	}
	msg := err.Error()
	s.store.UpdateJobStatus(job.ID, storage.JobFailed, "", msg) //nolint:errcheck
	s.store.AddJobEvent(job.ID, "failed", msg)                  //nolint:errcheck
	updated, _ := s.store.GetJob(job.ID)
	s.emit(context.Background(), updated, Update{JobID: job.ID, Status: storage.JobFailed, Message: msg})
}

func (s *Service) emit(ctx context.Context, job *storage.JobRecord, update Update) {
	if s.onUpdate != nil && job != nil {
		s.onUpdate(ctx, job, update)
	}
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
