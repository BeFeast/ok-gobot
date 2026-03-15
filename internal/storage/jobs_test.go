package storage

import "testing"

func TestJobTablesCreated(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	for _, table := range []string{"jobs", "job_events", "job_artifacts"} {
		if !tableExists(t, store.DB(), table) {
			t.Fatalf("expected table %q to exist", table)
		}
	}
}

func TestCreateAndGetJob(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	jobID, err := store.CreateJob(&JobRecord{
		SessionKey:         "dm:1",
		ChatID:             1,
		CreatedByMessageID: 42,
		Status:             JobQueued,
		TaskType:           "router",
		Summary:            "Implement feature",
		RouterDecision:     "launch_job",
		WorkerBackend:      "droid_cli",
		InputPayload:       `{"prompt":"do it"}`,
	})
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	got, err := store.GetJob(jobID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected job, got nil")
	}
	if got.WorkerBackend != "droid_cli" || got.ChatID != 1 || got.Status != JobQueued {
		t.Fatalf("unexpected job row: %+v", *got)
	}
}

func TestJobEventsAndArtifacts(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	jobID, err := store.CreateJob(&JobRecord{
		ChatID:        9,
		WorkerBackend: "droid_cli",
		InputPayload:  `{}`,
	})
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	if err := store.AddJobEvent(jobID, "queued", "job queued"); err != nil {
		t.Fatalf("AddJobEvent failed: %v", err)
	}
	if err := store.AddJobArtifact(&JobArtifactRecord{
		JobID: jobID,
		Name:  "report",
		Kind:  "markdown",
		URI:   "/tmp/report.md",
	}); err != nil {
		t.Fatalf("AddJobArtifact failed: %v", err)
	}

	events, err := store.ListJobEvents(jobID)
	if err != nil {
		t.Fatalf("ListJobEvents failed: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "queued" {
		t.Fatalf("unexpected events: %+v", events)
	}

	artifacts, err := store.ListJobArtifacts(jobID)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Name != "report" {
		t.Fatalf("unexpected artifacts: %+v", artifacts)
	}
}

func TestUpdateJobStatusAndList(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	jobID, err := store.CreateJob(&JobRecord{
		ChatID:        3,
		WorkerBackend: "droid_cli",
		InputPayload:  `{}`,
	})
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	if err := store.UpdateJobStatus(jobID, JobRunning, "", ""); err != nil {
		t.Fatalf("UpdateJobStatus running failed: %v", err)
	}
	if err := store.UpdateJobStatus(jobID, JobDone, `{"output":"ok"}`, ""); err != nil {
		t.Fatalf("UpdateJobStatus done failed: %v", err)
	}

	doneJobs, err := store.ListJobs(10, JobDone)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	if len(doneJobs) != 1 || doneJobs[0].ID != jobID {
		t.Fatalf("unexpected done jobs: %+v", doneJobs)
	}
	if doneJobs[0].FinishedAt == "" {
		t.Fatalf("expected finished_at to be set: %+v", doneJobs[0])
	}
}
