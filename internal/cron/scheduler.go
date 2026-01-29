package cron

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"ok-gobot/internal/storage"
)

// JobExecutor is called when a cron job fires
type JobExecutor func(ctx context.Context, job storage.CronJob) error

// Scheduler manages cron jobs
type Scheduler struct {
	cron     *cron.Cron
	store    *storage.Store
	executor JobExecutor
	jobs     map[int64]cron.EntryID
	mu       sync.RWMutex
	running  bool
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
	entryID, err := s.cron.AddFunc(job.Expression, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		log.Printf("Executing cron job %d: %s", jobCopy.ID, jobCopy.Task)

		if s.executor != nil {
			if err := s.executor(ctx, jobCopy); err != nil {
				log.Printf("Cron job %d failed: %v", jobCopy.ID, err)
			}
		}
	})

	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}

	s.jobs[job.ID] = entryID
	return nil
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
