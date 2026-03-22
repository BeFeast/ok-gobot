package cron

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

const defaultJobTimeout = 15 * time.Minute

// JobExecutor is called when an LLM-type cron job fires.
type JobExecutor func(ctx context.Context, job storage.CronJob) error

// ReportDeliverer is called after a cron-triggered job completes to deliver a
// standardized report to the originating chat (Telegram, inbox, etc.).
type ReportDeliverer func(chatID int64, report JobReport)

// Scheduler manages cron jobs.
type Scheduler struct {
	cron       *cron.Cron
	store      *storage.Store
	executor   JobExecutor
	deliverer  ReportDeliverer
	jobService *runtime.JobService
	jobs       map[int64]cron.EntryID
	mu         sync.RWMutex
	running    bool
}

// NewScheduler creates a new cron scheduler.
func NewScheduler(store *storage.Store, executor JobExecutor) *Scheduler {
	return &Scheduler{
		cron:     cron.New(cron.WithSeconds()),
		store:    store,
		executor: executor,
		jobs:     make(map[int64]cron.EntryID),
	}
}

// SetDeliverer sets the callback for delivering standardized job reports.
func (s *Scheduler) SetDeliverer(d ReportDeliverer) {
	s.deliverer = d
}

// SetJobService connects the scheduler to the durable job service so that
// every cron fire creates a tracked Job record.
func (s *Scheduler) SetJobService(js *runtime.JobService) {
	s.jobService = js
}

// Start begins the scheduler.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.mu.Unlock()

	// Load existing jobs from database
	if err := s.loadJobs(); err != nil {
		log.Printf("Warning: failed to load cron jobs: %v", err)
	}

	// Start the cron scheduler
	s.cron.Start()

	// Wait for context cancellation
	go func() {
		<-ctx.Done()
		s.Stop()
	}()

	log.Println("Cron scheduler started")
	return nil
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	ctx := s.cron.Stop()
	<-ctx.Done()
	s.running = false
	log.Println("Cron scheduler stopped")
}

// loadJobs loads all enabled jobs from the database.
func (s *Scheduler) loadJobs() error {
	jobs, err := s.store.GetCronJobs()
	if err != nil {
		return err
	}

	for _, job := range jobs {
		if err := s.scheduleJob(job); err != nil {
			log.Printf("Failed to schedule job %d: %v", job.ID, err)
		}
	}

	log.Printf("Loaded %d cron jobs", len(jobs))
	return nil
}

// scheduleJob adds a job to the scheduler.
func (s *Scheduler) scheduleJob(job storage.CronJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing entry if present
	if entryID, ok := s.jobs[job.ID]; ok {
		s.cron.Remove(entryID)
	}

	// Create the cron function
	jobCopy := job // Capture for closure
	timeout := defaultJobTimeout
	if jobCopy.TimeoutSeconds > 0 {
		timeout = time.Duration(jobCopy.TimeoutSeconds) * time.Second
	}

	entryID, err := s.cron.AddFunc(job.Expression, func() {
		log.Printf("Executing cron job %d (type=%s): %s", jobCopy.ID, jobCopy.Type, jobCopy.Task)
		s.fireCronJob(jobCopy, timeout)
	})

	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}

	s.jobs[job.ID] = entryID
	return nil
}

// fireCronJob executes a single cron job fire. When a JobService is configured,
// this creates a durable Job record and delivers a standardized report on
// completion. Otherwise it falls back to the legacy direct-execution path.
func (s *Scheduler) fireCronJob(cronJob storage.CronJob, timeout time.Duration) {
	if s.jobService == nil {
		s.fireLegacy(cronJob, timeout)
		return
	}

	kind := "cron_llm"
	if cronJob.Type == "exec" {
		kind = "cron_exec"
	}

	runner := func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		if cronJob.Type == "exec" {
			return s.runExecJob(ctx, cronJob)
		}
		return s.runLLMJob(ctx, cronJob)
	}

	started := time.Now()
	runtimeJob, err := s.jobService.StartDetached(context.Background(), runtime.JobSpec{
		Kind:        kind,
		Worker:      "cron_scheduler",
		Description: fmt.Sprintf("cron #%d: %s", cronJob.ID, truncate(cronJob.Task, 80)),
		Timeout:     timeout,
	}, func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		result, runErr := runner(ctx, job, svc)

		// Deliver standardized report after execution completes.
		if s.deliverer != nil && cronJob.ChatID != 0 {
			report := buildReport(cronJob, job.JobID, started, result, runErr)
			s.deliverer(cronJob.ChatID, report)
		}

		return result, runErr
	})

	if err != nil {
		log.Printf("Cron job %d: failed to create durable job: %v", cronJob.ID, err)
		// Fall back to legacy path so the cron job still fires.
		s.fireLegacy(cronJob, timeout)
		return
	}

	log.Printf("Cron job %d: created durable job %s", cronJob.ID, runtimeJob.JobID)
}

// runLLMJob executes an LLM-type cron job through the configured executor.
func (s *Scheduler) runLLMJob(ctx context.Context, cronJob storage.CronJob) (runtime.JobRunResult, error) {
	if s.executor == nil {
		return runtime.JobRunResult{}, fmt.Errorf("no LLM executor configured")
	}
	if err := s.executor(ctx, cronJob); err != nil {
		return runtime.JobRunResult{}, err
	}
	return runtime.JobRunResult{Summary: "LLM task completed"}, nil
}

// runExecJob executes a shell command and returns the output as a job result.
func (s *Scheduler) runExecJob(ctx context.Context, cronJob storage.CronJob) (runtime.JobRunResult, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", cronJob.Task)
	output, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(output))

	if err != nil {
		summary := fmt.Sprintf("command failed: %v", err)
		if result != "" {
			summary += "\n" + result
		}
		return runtime.JobRunResult{Summary: summary}, err
	}

	return runtime.JobRunResult{
		Summary: "command completed",
		Artifacts: []runtime.JobArtifactSpec{{
			Name:     "output.txt",
			Type:     "stdout",
			MimeType: "text/plain",
			Content:  result,
		}},
	}, nil
}

// fireLegacy is the pre-JobService execution path. Kept for backwards
// compatibility when no JobService is wired in.
func (s *Scheduler) fireLegacy(cronJob storage.CronJob, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if cronJob.Type == "exec" {
		s.executeExecJobLegacy(ctx, cronJob)
	} else {
		if s.executor != nil {
			if err := s.executor(ctx, cronJob); err != nil {
				log.Printf("Cron job %d failed: %v", cronJob.ID, err)
			}
		}
	}
}

// executeExecJobLegacy runs a shell command directly without LLM (legacy path).
func (s *Scheduler) executeExecJobLegacy(ctx context.Context, job storage.CronJob) {
	cmd := exec.CommandContext(ctx, "bash", "-c", job.Task)
	output, err := cmd.CombinedOutput()

	result := strings.TrimSpace(string(output))
	if err != nil {
		log.Printf("Cron exec job %d failed: %v\nOutput: %s", job.ID, err, result)
		if s.deliverer != nil && job.ChatID != 0 {
			report := JobReport{
				CronJobID: job.ID,
				Type:      job.Type,
				Task:      job.Task,
				Status:    "failed",
				Error:     err.Error(),
				Output:    result,
				Duration:  0,
			}
			s.deliverer(job.ChatID, report)
		}
		return
	}

	log.Printf("Cron exec job %d completed. Output: %d bytes", job.ID, len(result))
	if s.deliverer != nil && job.ChatID != 0 {
		report := JobReport{
			CronJobID: job.ID,
			Type:      job.Type,
			Task:      job.Task,
			Status:    "succeeded",
			Output:    result,
			Duration:  0,
		}
		s.deliverer(job.ChatID, report)
	}
}

// AddJob creates and schedules a new job.
func (s *Scheduler) AddJob(expression, task string, chatID int64) (int64, error) {
	// Validate cron expression
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser.Parse(expression); err != nil {
		return 0, fmt.Errorf("invalid cron expression: %w", err)
	}

	// Save to database
	jobID, err := s.store.SaveCronJob(expression, task, chatID)
	if err != nil {
		return 0, fmt.Errorf("failed to save job: %w", err)
	}

	// Schedule the job
	job := storage.CronJob{
		ID:         jobID,
		Expression: expression,
		Task:       task,
		ChatID:     chatID,
		Enabled:    true,
	}

	if err := s.scheduleJob(job); err != nil {
		// Clean up on failure
		s.store.DeleteCronJob(jobID)
		return 0, err
	}

	return jobID, nil
}

// AddExecJob creates and schedules a new exec-type job.
func (s *Scheduler) AddExecJob(expression, task string, chatID int64, timeoutSeconds int) (int64, error) {
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser.Parse(expression); err != nil {
		return 0, fmt.Errorf("invalid cron expression: %w", err)
	}

	jobID, err := s.store.SaveCronJobFull(expression, task, chatID, "exec", timeoutSeconds)
	if err != nil {
		return 0, fmt.Errorf("failed to save job: %w", err)
	}

	job := storage.CronJob{
		ID:             jobID,
		Expression:     expression,
		Task:           task,
		ChatID:         chatID,
		Enabled:        true,
		Type:           "exec",
		TimeoutSeconds: timeoutSeconds,
	}

	if err := s.scheduleJob(job); err != nil {
		s.store.DeleteCronJob(jobID)
		return 0, err
	}

	return jobID, nil
}

// RemoveJob removes a scheduled job.
func (s *Scheduler) RemoveJob(jobID int64) error {
	s.mu.Lock()
	if entryID, ok := s.jobs[jobID]; ok {
		s.cron.Remove(entryID)
		delete(s.jobs, jobID)
	}
	s.mu.Unlock()

	return s.store.DeleteCronJob(jobID)
}

// ToggleJob enables or disables a job.
func (s *Scheduler) ToggleJob(jobID int64, enabled bool) error {
	if err := s.store.ToggleCronJob(jobID, enabled); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !enabled {
		// Remove from scheduler
		if entryID, ok := s.jobs[jobID]; ok {
			s.cron.Remove(entryID)
			delete(s.jobs, jobID)
		}
	} else {
		// Reload job
		jobs, err := s.store.GetCronJobs()
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if job.ID == jobID {
				return s.scheduleJob(job)
			}
		}
	}

	return nil
}

// ListJobs returns all scheduled jobs.
func (s *Scheduler) ListJobs() ([]storage.CronJob, error) {
	return s.store.GetCronJobs()
}

// GetNextRun returns the next run time for a job.
func (s *Scheduler) GetNextRun(jobID int64) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entryID, ok := s.jobs[jobID]
	if !ok {
		return time.Time{}, fmt.Errorf("job not found")
	}

	entry := s.cron.Entry(entryID)
	return entry.Next, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
