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
// It returns the result message text.
type JobExecutor func(ctx context.Context, job storage.CronJob) (string, error)

// ExecResultNotifier is called after an exec-type job finishes to deliver results
type ExecResultNotifier func(chatID int64, message string)

// DeliveryFunc sends a formatted report to a chat.
type DeliveryFunc func(chatID int64, text string)

// Scheduler manages cron jobs
type Scheduler struct {
	cron       *cron.Cron
	store      *storage.Store
	executor   JobExecutor
	notifier   ExecResultNotifier
	deliverer  DeliveryFunc
	jobService *runtime.JobService
	jobs       map[int64]cron.EntryID
	mu         sync.RWMutex
	running    bool
}

// NewScheduler creates a new cron scheduler
func NewScheduler(store *storage.Store, executor JobExecutor) *Scheduler {
	return &Scheduler{
		cron:     cron.New(cron.WithSeconds()),
		store:    store,
		executor: executor,
		jobs:     make(map[int64]cron.EntryID),
	}
}

// SetNotifier sets the callback for exec-type job result delivery (legacy path).
func (s *Scheduler) SetNotifier(n ExecResultNotifier) {
	s.notifier = n
}

// SetJobService enables durable-job creation for every cron schedule firing.
func (s *Scheduler) SetJobService(js *runtime.JobService) {
	s.jobService = js
}

// SetDeliverer sets the callback that sends standardised reports to chats.
func (s *Scheduler) SetDeliverer(d DeliveryFunc) {
	s.deliverer = d
}

// Start begins the scheduler
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

// Stop halts the scheduler
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

// loadJobs loads all enabled jobs from the database
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

// scheduleJob adds a job to the scheduler
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
			s.executeViaJob(jobCopy, timeout)
			return
		}

		// Legacy path — no durable job tracking.
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if jobCopy.Type == "exec" {
			s.executeExecJob(ctx, jobCopy)
		} else {
			if s.executor != nil {
				msg, err := s.executor(ctx, jobCopy)
				if err != nil {
					log.Printf("Cron job %d failed: %v", jobCopy.ID, err)
				} else if msg != "" && s.notifier != nil && jobCopy.ChatID != 0 {
					s.notifier(jobCopy.ChatID, msg)
				}
			}
		}
	})

	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}

	s.jobs[job.ID] = entryID
	return nil
}

// executeViaJob creates a durable background job for a cron trigger.
func (s *Scheduler) executeViaJob(cronJob storage.CronJob, timeout time.Duration) {
	// Ensure a delivery route exists so the job service can validate it.
	routeKey := fmt.Sprintf("cron:schedule:%d", cronJob.ID)
	if cronJob.ChatID != 0 {
		if err := s.store.UpsertSessionRoute(storage.SessionRoute{
			SessionKey: routeKey,
			Channel:    "telegram",
			ChatID:     cronJob.ChatID,
		}); err != nil {
			log.Printf("[cron] failed to upsert session route for cron #%d: %v", cronJob.ID, err)
		}
	}

	deliveryKey := ""
	if cronJob.ChatID != 0 {
		deliveryKey = routeKey
	}

	spec := runtime.JobSpec{
		Kind:               "cron",
		Worker:             fmt.Sprintf("cron:%d", cronJob.ID),
		SessionKey:         fmt.Sprintf("cron:run:%d:%d", cronJob.ID, time.Now().UnixNano()),
		DeliverySessionKey: deliveryKey,
		Description:        cronJob.Task,
		Timeout:            timeout,
	}

	cj := cronJob // capture

	runner := func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		start := time.Now()
		var (
			summary string
			runErr  error
		)

		if cj.Type == "exec" {
			summary, runErr = runExecCommand(ctx, cj.Task)
		} else if s.executor != nil {
			summary, runErr = s.executor(ctx, cj)
		} else {
			return runtime.JobRunResult{}, fmt.Errorf("no executor configured")
		}

		elapsed := time.Since(start)

		// Build the result for the job service.
		result := runtime.JobRunResult{Summary: summary}
		if summary != "" {
			result.Artifacts = []runtime.JobArtifactSpec{{
				Name:     "report.md",
				Type:     "report",
				MimeType: "text/markdown",
				Content:  summary,
			}}
		}

		// Deliver report to the chat.
		// NOTE: delivery happens before the job reaches its terminal status in the
		// durable store (MarkJobSucceeded/Failed runs after the runner returns).
		// A user who queries the job ID from the report immediately may still see
		// "running". This is acceptable for now; a post-run hook could close the gap.
		if s.deliverer != nil && cj.ChatID != 0 {
			report := FormatReport(cj, job.JobID, result, runErr, elapsed)
			s.deliverer(cj.ChatID, report)
		}

		if runErr != nil {
			return result, runErr
		}
		return result, nil
	}

	if _, err := s.jobService.StartDetached(context.Background(), spec, runner); err != nil {
		log.Printf("[cron] failed to create job for cron #%d: %v", cronJob.ID, err)
	}
}

// runExecCommand runs a shell command and returns its output.
func runExecCommand(ctx context.Context, task string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", task)
	output, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(output))
	if err != nil {
		return result, fmt.Errorf("exec failed: %w", err)
	}
	return result, nil
}

// executeExecJob runs a shell command directly without LLM (legacy path).
func (s *Scheduler) executeExecJob(ctx context.Context, job storage.CronJob) {
	cmd := exec.CommandContext(ctx, "bash", "-c", job.Task)
	output, err := cmd.CombinedOutput()

	result := strings.TrimSpace(string(output))
	if err != nil {
		log.Printf("Cron exec job %d failed: %v\nOutput: %s", job.ID, err, result)
		if s.notifier != nil && job.ChatID != 0 {
			// Truncate for Telegram (4096 char limit)
			msg := fmt.Sprintf("Cron job #%d failed: %v\n\n%s", job.ID, err, result)
			if r := []rune(msg); len(r) > 4000 {
				msg = string(r[:4000]) + "\n...(truncated)"
			}
			s.notifier(job.ChatID, msg)
		}
		return
	}

	log.Printf("Cron exec job %d completed. Output: %d bytes", job.ID, len(result))
	if s.notifier != nil && job.ChatID != 0 && result != "" {
		msg := fmt.Sprintf("Cron job #%d completed:\n\n%s", job.ID, result)
		if r := []rune(msg); len(r) > 4000 {
			msg = string(r[:4000]) + "\n...(truncated)"
		}
		s.notifier(job.ChatID, msg)
	}
}

// AddJob creates and schedules a new job
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

// AddExecJob creates and schedules a new exec-type job
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

// RemoveJob removes a scheduled job
func (s *Scheduler) RemoveJob(jobID int64) error {
	s.mu.Lock()
	if entryID, ok := s.jobs[jobID]; ok {
		s.cron.Remove(entryID)
		delete(s.jobs, jobID)
	}
	s.mu.Unlock()

	return s.store.DeleteCronJob(jobID)
}

// ToggleJob enables or disables a job
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

// ListJobs returns all scheduled jobs
func (s *Scheduler) ListJobs() ([]storage.CronJob, error) {
	return s.store.GetCronJobs()
}

// GetNextRun returns the next run time for a job
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
