package router

import "testing"

func TestDecideReply(t *testing.T) {
	r := New("droid_cli")
	decision := r.Decide("What's the weather like?")

	if decision.Action != ActionReply {
		t.Fatalf("expected reply, got %s", decision.Action)
	}
	if decision.WorkerBackend != "" {
		t.Fatalf("reply should not select worker backend, got %q", decision.WorkerBackend)
	}
}

func TestDecideLaunchJobForHeavyKeyword(t *testing.T) {
	r := New("droid_cli")
	decision := r.Decide("Please implement the job router and refactor the runtime.")

	if decision.Action != ActionLaunchJob {
		t.Fatalf("expected launch_job, got %s", decision.Action)
	}
	if decision.WorkerBackend != "droid_cli" {
		t.Fatalf("unexpected worker backend: %q", decision.WorkerBackend)
	}
}

func TestDecideClarifyOnEmptyInput(t *testing.T) {
	r := New("droid_cli")
	decision := r.Decide("   ")

	if decision.Action != ActionClarify {
		t.Fatalf("expected clarify, got %s", decision.Action)
	}
}
