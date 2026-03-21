package storage

import (
	"testing"
)

func TestRecentUserMessages(t *testing.T) {
	s := newV2TestStore(t)

	// Ensure session exists.
	err := s.ensureSessionV2("agent:bot:telegram:dm:1", "bot", "")
	if err != nil {
		t.Fatalf("ensureSessionV2: %v", err)
	}

	// Insert some messages.
	for _, msg := range []struct {
		role, content string
	}{
		{"user", "deploy the app"},
		{"assistant", "deploying now..."},
		{"user", "check the logs"},
		{"user", "review this PR"},
	} {
		if err := s.SaveSessionMessageV2("agent:bot:telegram:dm:1", msg.role, msg.content, ""); err != nil {
			t.Fatalf("SaveSessionMessageV2: %v", err)
		}
	}

	msgs, err := s.RecentUserMessages(10)
	if err != nil {
		t.Fatalf("RecentUserMessages: %v", err)
	}

	// Should only get user messages.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 user messages, got %d", len(msgs))
	}
	for _, m := range msgs {
		if m.Role != "user" {
			t.Errorf("expected role 'user', got %q", m.Role)
		}
	}
}

func TestRecentUserMessages_Empty(t *testing.T) {
	s := newV2TestStore(t)

	msgs, err := s.RecentUserMessages(10)
	if err != nil {
		t.Fatalf("RecentUserMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(msgs))
	}
}

func TestRecentCompletedJobs(t *testing.T) {
	s := newV2TestStore(t)

	// Insert jobs with different statuses.
	for _, j := range []Job{
		{JobID: "j1", Kind: "chat", Status: "succeeded", Description: "deploy app"},
		{JobID: "j2", Kind: "chat", Status: "failed", Description: "run tests"},
		{JobID: "j3", Kind: "chat", Status: "running", Description: "still going"},
		{JobID: "j4", Kind: "chat", Status: "pending", Description: "waiting"},
	} {
		if err := s.CreateJob(j); err != nil {
			t.Fatalf("CreateJob(%s): %v", j.JobID, err)
		}
	}

	// Mark j1 as succeeded and j2 as failed.
	if err := s.MarkJobSucceeded("j1", "done"); err != nil {
		t.Fatalf("MarkJobSucceeded: %v", err)
	}
	if err := s.MarkJobFailed("j2", "oops"); err != nil {
		t.Fatalf("MarkJobFailed: %v", err)
	}

	jobs, err := s.RecentCompletedJobs(10)
	if err != nil {
		t.Fatalf("RecentCompletedJobs: %v", err)
	}

	if len(jobs) != 2 {
		t.Fatalf("expected 2 completed jobs, got %d", len(jobs))
	}
	for _, j := range jobs {
		if j.Status != "succeeded" && j.Status != "failed" {
			t.Errorf("expected succeeded/failed, got %q", j.Status)
		}
	}
}

func TestAllCronJobs(t *testing.T) {
	s := newV2TestStore(t)

	// Insert cron jobs.
	if _, err := s.SaveCronJob("0 9 * * *", "morning check", 123); err != nil {
		t.Fatalf("SaveCronJob: %v", err)
	}
	id2, err := s.SaveCronJob("0 18 * * 1-5", "evening report", 123)
	if err != nil {
		t.Fatalf("SaveCronJob: %v", err)
	}

	// Disable second job.
	if err := s.ToggleCronJob(id2, false); err != nil {
		t.Fatalf("ToggleCronJob: %v", err)
	}

	jobs, err := s.AllCronJobs()
	if err != nil {
		t.Fatalf("AllCronJobs: %v", err)
	}

	// Should get both enabled and disabled.
	if len(jobs) != 2 {
		t.Fatalf("expected 2 cron jobs, got %d", len(jobs))
	}

	// Verify we got both.
	foundEnabled, foundDisabled := false, false
	for _, j := range jobs {
		if j.Enabled {
			foundEnabled = true
		} else {
			foundDisabled = true
		}
	}
	if !foundEnabled || !foundDisabled {
		t.Error("expected both enabled and disabled cron jobs")
	}
}
