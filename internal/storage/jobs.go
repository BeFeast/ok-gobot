package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

// JobStatus describes the lifecycle state of a first-class job.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobWaiting   JobStatus = "waiting_input"
	JobDone      JobStatus = "done"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// JobRecord is the persisted form of a background job.
type JobRecord struct {
	ID                 int64
	SessionKey         string
	ChatID             int64
	CreatedByMessageID int64
	Status             JobStatus
	TaskType           string
	Summary            string
	RouterDecision     string
	WorkerBackend      string
	WorkerProfile      string
	InputPayload       string
	ResultPayload      string
	Error              string
	StartedAt          string
	FinishedAt         string
	CreatedAt          string
	UpdatedAt          string
}

// JobEventRecord is one persisted lifecycle event for a job.
type JobEventRecord struct {
	ID        int64
	JobID     int64
	EventType string
	Message   string
	CreatedAt string
}

// JobArtifactRecord describes one artifact emitted by a job.
type JobArtifactRecord struct {
	ID        int64
	JobID     int64
	Name      string
	Kind      string
	URI       string
	Metadata  string
	CreatedAt string
}

// CreateJob inserts a new job row.
func (s *Store) CreateJob(job *JobRecord) (int64, error) {
	if job == nil {
		return 0, fmt.Errorf("job is required")
	}
	if strings.TrimSpace(job.WorkerBackend) == "" {
		return 0, fmt.Errorf("worker backend is required")
	}
	status := job.Status
	if status == "" {
		status = JobQueued
	}
	taskType := strings.TrimSpace(job.TaskType)
	if taskType == "" {
		taskType = "job"
	}

	result, err := s.db.Exec(`
		INSERT INTO jobs (
			session_key, chat_id, created_by_message_id, status, task_type, summary,
			router_decision, worker_backend, worker_profile, input_payload, result_payload, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		strings.TrimSpace(job.SessionKey),
		job.ChatID,
		job.CreatedByMessageID,
		string(status),
		taskType,
		job.Summary,
		job.RouterDecision,
		job.WorkerBackend,
		job.WorkerProfile,
		job.InputPayload,
		job.ResultPayload,
		job.Error,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetJob loads a single job row.
func (s *Store) GetJob(jobID int64) (*JobRecord, error) {
	var job JobRecord
	var startedAt, finishedAt sql.NullString
	err := s.db.QueryRow(`
		SELECT id, session_key, chat_id, created_by_message_id, status, task_type, summary,
		       router_decision, worker_backend, worker_profile, input_payload, result_payload,
		       error, started_at, finished_at, created_at, updated_at
		FROM jobs
		WHERE id = ?
	`, jobID).Scan(
		&job.ID,
		&job.SessionKey,
		&job.ChatID,
		&job.CreatedByMessageID,
		&job.Status,
		&job.TaskType,
		&job.Summary,
		&job.RouterDecision,
		&job.WorkerBackend,
		&job.WorkerProfile,
		&job.InputPayload,
		&job.ResultPayload,
		&job.Error,
		&startedAt,
		&finishedAt,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if startedAt.Valid {
		job.StartedAt = startedAt.String
	}
	if finishedAt.Valid {
		job.FinishedAt = finishedAt.String
	}
	return &job, nil
}

// ListJobs returns recent jobs ordered newest first.
func (s *Store) ListJobs(limit int, status JobStatus) ([]JobRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT id, session_key, chat_id, created_by_message_id, status, task_type, summary,
		       router_decision, worker_backend, worker_profile, input_payload, result_payload,
		       error, COALESCE(started_at, ''), COALESCE(finished_at, ''), created_at, updated_at
		FROM jobs
	`
	args := []interface{}{}
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, string(status))
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var jobs []JobRecord
	for rows.Next() {
		var job JobRecord
		if err := rows.Scan(
			&job.ID,
			&job.SessionKey,
			&job.ChatID,
			&job.CreatedByMessageID,
			&job.Status,
			&job.TaskType,
			&job.Summary,
			&job.RouterDecision,
			&job.WorkerBackend,
			&job.WorkerProfile,
			&job.InputPayload,
			&job.ResultPayload,
			&job.Error,
			&job.StartedAt,
			&job.FinishedAt,
			&job.CreatedAt,
			&job.UpdatedAt,
		); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}

	return jobs, rows.Err()
}

// UpdateJobStatus mutates the lifecycle fields for a job.
func (s *Store) UpdateJobStatus(jobID int64, status JobStatus, resultPayload, errText string) error {
	query := `
		UPDATE jobs
		SET status = ?, result_payload = ?, error = ?, updated_at = CURRENT_TIMESTAMP
	`
	args := []interface{}{string(status), resultPayload, errText}

	switch status {
	case JobRunning:
		query += ", started_at = COALESCE(started_at, CURRENT_TIMESTAMP)"
	case JobDone, JobFailed, JobCancelled:
		query += ", finished_at = CURRENT_TIMESTAMP"
	}

	query += " WHERE id = ?"
	args = append(args, jobID)

	_, err := s.db.Exec(query, args...)
	return err
}

// AddJobEvent inserts a lifecycle event for a job.
func (s *Store) AddJobEvent(jobID int64, eventType, message string) error {
	_, err := s.db.Exec(`
		INSERT INTO job_events (job_id, event_type, message)
		VALUES (?, ?, ?)
	`, jobID, strings.TrimSpace(eventType), message)
	return err
}

// ListJobEvents returns all lifecycle events for a job.
func (s *Store) ListJobEvents(jobID int64) ([]JobEventRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, job_id, event_type, message, created_at
		FROM job_events
		WHERE job_id = ?
		ORDER BY id ASC
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var events []JobEventRecord
	for rows.Next() {
		var event JobEventRecord
		if err := rows.Scan(&event.ID, &event.JobID, &event.EventType, &event.Message, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// AddJobArtifact inserts one job artifact row.
func (s *Store) AddJobArtifact(artifact *JobArtifactRecord) error {
	if artifact == nil {
		return fmt.Errorf("artifact is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO job_artifacts (job_id, name, kind, uri, metadata)
		VALUES (?, ?, ?, ?, ?)
	`, artifact.JobID, artifact.Name, artifact.Kind, artifact.URI, artifact.Metadata)
	return err
}

// ListJobArtifacts returns all artifact rows for a job.
func (s *Store) ListJobArtifacts(jobID int64) ([]JobArtifactRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, job_id, name, kind, uri, metadata, created_at
		FROM job_artifacts
		WHERE job_id = ?
		ORDER BY id ASC
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var artifacts []JobArtifactRecord
	for rows.Next() {
		var artifact JobArtifactRecord
		if err := rows.Scan(
			&artifact.ID,
			&artifact.JobID,
			&artifact.Name,
			&artifact.Kind,
			&artifact.URI,
			&artifact.Metadata,
			&artifact.CreatedAt,
		); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

// GetLatestJobForChat returns the newest non-terminal job for a chat, then any newest job.
func (s *Store) GetLatestJobForChat(chatID int64) (*JobRecord, error) {
	for _, query := range []string{
		`SELECT id FROM jobs WHERE chat_id = ? AND status IN ('queued', 'running', 'waiting_input') ORDER BY id DESC LIMIT 1`,
		`SELECT id FROM jobs WHERE chat_id = ? ORDER BY id DESC LIMIT 1`,
	} {
		var id int64
		err := s.db.QueryRow(query, chatID).Scan(&id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		return s.GetJob(id)
	}
	return nil, nil
}
