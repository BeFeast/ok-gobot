package cron

import (
	"context"
	"testing"

	"ok-gobot/internal/role"
)

func TestRoleTaskHelpers(t *testing.T) {
	name := "researcher"
	prompt := "# Researcher\nDo research."

	task := roleTaskFor(name, prompt)
	if !isRoleTask(task) {
		t.Errorf("isRoleTask(%q) = false, want true", task)
	}
	if got := roleNameFromTask(task); got != name {
		t.Errorf("roleNameFromTask(%q) = %q, want %q", task, got, name)
	}

	// Non-role tasks.
	for _, s := range []string{"", "plain task", "check health"} {
		if isRoleTask(s) {
			t.Errorf("isRoleTask(%q) = true, want false", s)
		}
		if roleNameFromTask(s) != "" {
			t.Errorf("roleNameFromTask(%q) = %q, want empty", s, roleNameFromTask(s))
		}
	}
}

func TestRegisterRoleJobs_RequiresNonZeroChatID(t *testing.T) {
	store := newTestStore(t)
	s := NewScheduler(store, nil)

	manifests := []*role.Manifest{
		{Name: "test", Schedule: "0 0 9 * * *", Prompt: "test prompt"},
	}
	if err := s.RegisterRoleJobs(manifests, 0); err == nil {
		t.Fatal("RegisterRoleJobs with chatID=0 should fail")
	}
}

func TestRegisterRoleJobs_SkipsNoSchedule(t *testing.T) {
	store := newTestStore(t)
	s := NewScheduler(store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	manifests := []*role.Manifest{
		{Name: "unscheduled", Schedule: "", Prompt: "no schedule here"},
	}
	if err := s.RegisterRoleJobs(manifests, 999); err != nil {
		t.Fatalf("RegisterRoleJobs failed: %v", err)
	}

	jobs, err := store.GetCronJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs for unscheduled role, got %d", len(jobs))
	}
}

func TestRegisterRoleJobs_Idempotent(t *testing.T) {
	store := newTestStore(t)
	s := NewScheduler(store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	m := &role.Manifest{
		Name:     "monitor",
		Schedule: "0 */30 * * * *",
		Prompt:   "monitor prompt",
		Approval: role.ApprovalAuto,
	}

	// Register twice — second call should be a no-op.
	if err := s.RegisterRoleJobs([]*role.Manifest{m}, 42); err != nil {
		t.Fatalf("first RegisterRoleJobs: %v", err)
	}
	if err := s.RegisterRoleJobs([]*role.Manifest{m}, 42); err != nil {
		t.Fatalf("second RegisterRoleJobs: %v", err)
	}

	jobs, err := store.GetCronJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Errorf("expected exactly 1 job after two RegisterRoleJobs calls, got %d", len(jobs))
	}
	if !isRoleTask(jobs[0].Task) {
		t.Errorf("job task %q is not a role task", jobs[0].Task)
	}
	if roleNameFromTask(jobs[0].Task) != "monitor" {
		t.Errorf("roleNameFromTask = %q, want %q", roleNameFromTask(jobs[0].Task), "monitor")
	}
}
