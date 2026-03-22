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

// ExecResultNotifier is called after an exec-type job finishes to deliver results.
// Retained for legacy mode when no JobService is configured.
type ExecResultNotifier func(chatID int64, message string)

// ReportDeliverer sends a standardized report to a chat after a job completes.
type ReportDeliverer func(chatID int64, report JobReport)

// Scheduler manages cron jobs.
type Scheduler struct {
	cron       *cron.Cron
	store      *storage.Store
	executor   JobExecutor
	notifier   ExecResultNotifier
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

// SetNotifier sets the callback for legacy exec-type job result delivery.
func (s *Scheduler) SetNotifier(n ExecResultNotifier) {
	s.notifier = n
}

// SetJobService enables durable job tracking for every cron fire.
func (s *Scheduler) SetJobService(js *runtime.JobService) {
	s.jobService = js
}

// SetReportDeliverer sets the callback for standardized report delivery.
func (s *Scheduler) SetReportDeliverer(d ReportDeliverer) {
	s.deliverer = d
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

		if s.jobService != nil {
			s.fireDurable(jobCopy, timeout)
		} else {
			s.fireLegacy(jobCopy, timeout)
		}
	})

	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}

	s.jobs[job.ID] = entryID
	return nil
}

// fireDurable creates a durable runtime.Job for the cron fire and delivers a
// standardized report on completion.
func (s *Scheduler) fireDurable(cronJob storage.CronJob, timeout time.Duration) {
	kind := "cron_exec"
	if cronJob.Type != "exec" {
		kind = "cron_llm"
	}

	runner := func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		if cronJob.Type == "exec" {
			return s.runExec(ctx, cronJob)
		}
		return s.runLLM(ctx, cronJob)
	}

	start := time.Now()
	job, err := s.jobService.StartDetached(context.Background(), runtime.JobSpec{
		Kind:        kind,
		Worker:      "cron_scheduler",
		Description: fmt.Sprintf("schedule #%d: %s", cronJob.ID, cronJob.Task),
		Timeout:     timeout,
	}, runner)

	if err != nil {
		log.Printf("Cron job %d: failed to create durable job: %v", cronJob.ID, err)
		// Fall back to legacy execution on job creation failure
		s.fireLegacy(cronJob, timeout)
		return
	}

	// Wait for the durable job to reach a terminal state, then deliver the report.
	go s.waitAndDeliver(cronJob, job.JobID, start)
}

// waitAndDeliver polls for the durable job to complete and delivers a report.
func (s *Scheduler) waitAndDeliver(cronJob storage.CronJob, jobID string, start time.Time) {
	var finished *storage.Job
	for {
		j, err := s.store.GetJob(jobID)
		if err != nil {
			log.Printf("Cron job %d: failed to poll durable job %s: %v", cronJob.ID, jobID, err)
			return
		}
		if j != nil && isTerminal(j.Status) {
			finished = j
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	report := JobReport{
		CronJobID:  cronJob.ID,
		Expression: cronJob.Expression,
		Task:       cronJob.Task,
		JobType:    cronJob.Type,
		Status:     finished.Status,
		Summary:    finished.Summary,
		Error:      finished.Error,
		Duration:   time.Since(start),
		JobID:      finished.JobID,
	}

	if cronJob.Type == "" {
		report.JobType = "llm"
	}

	s.deliver(cronJob.ChatID, report)
}

// fireLegacy executes the cron job directly without durable job tracking.
func (s *Scheduler) fireLegacy(cronJob storage.CronJob, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if cronJob.Type == "exec" {
		s.executeExecJob(ctx, cronJob)
	} else {
		if s.executor != nil {
			if err := s.executor(ctx, cronJob); err != nil {
				log.Printf("Cron job %d failed: %v", cronJob.ID, err)
			}
		}
	}
}

// runExec wraps shell execution as a JobRunner for durable jobs.
func (s *Scheduler) runExec(ctx context.Context, cronJob storage.CronJob) (runtime.JobRunResult, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", cronJob.Task)
	output, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(output))

	if err != nil {
		return runtime.JobRunResult{Summary: result}, fmt.Errorf("exec failed: %w", err)
	}

	var artifacts []runtime.JobArtifactSpec
	if result != "" {
		artifacts = append(artifacts, runtime.JobArtifactSpec{
			Name:     "output.txt",
			Type:     "exec_output",
			MimeType: "text/plain",
			Content:  result,
		})
	}

	return runtime.JobRunResult{
		Summary:   result,
		Artifacts: artifacts,
	}, nil
}

// runLLM wraps LLM execution as a JobRunner for durable jobs.
func (s *Scheduler) runLLM(ctx context.Context, cronJob storage.CronJob) (runtime.JobRunResult, error) {
	if s.executor == nil {
		return runtime.JobRunResult{}, fmt.Errorf("no LLM executor configured")
	}

	if err := s.executor(ctx, cronJob); err != nil {
		return runtime.JobRunResult{}, err
	}

	return runtime.JobRunResult{
		Summary: fmt.Sprintf("completed task: %s", cronJob.Task),
	}, nil
}

// deliver sends a report through the configured deliverer, falling back to the
// legacy notifier.
func (s *Scheduler) deliver(chatID int64, report JobReport) {
	if chatID == 0 {
		return
	}

	if s.deliverer != nil {
		s.deliverer(chatID, report)
		return
	}

	// Fall back to legacy notifier with formatted message
	if s.notifier != nil {
		s.notifier(chatID, report.FormatTelegram())
	}
}

// executeExecJob runs a shell command directly without LLM (legacy path).
func (s *Scheduler) executeExecJob(ctx context.Context, job storage.CronJob) {
	cmd := exec.CommandContext(ctx, "bash", "-c", job.Task)
	output, err := cmd.CombinedOutput()

	result := strings.TrimSpace(string(output))
	if err != nil {
		log.Printf("Cron exec job %d failed: %v\nOutput: %s", job.ID, err, result)
		if s.notifier != nil && job.ChatID != 0 {
			msg := fmt.Sprintf("Cron job #%d failed: %v\n\n%s", job.ID, err, result)
			if len(msg) > 4000 {
				msg = msg[:4000] + "\n...(truncated)"
			}
			s.notifier(job.ChatID, msg)
		}
		return
	}

	log.Printf("Cron exec job %d completed. Output: %d bytes", job.ID, len(result))
	if s.notifier != nil && job.ChatID != 0 && result != "" {
		msg := fmt.Sprintf("Cron job #%d completed:\n\n%s", job.ID, result)
		if len(msg) > 4000 {
			msg = msg[:4000] + "\n...(truncated)"
		}
		s.notifier(job.ChatID, msg)
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

func isTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "timed_out":
		return true
	}
	return false
}
