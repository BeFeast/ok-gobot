package worker

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestNewBatchRunnerDefaults(t *testing.T) {
	t.Run("zero MaxParallel becomes 5", func(t *testing.T) {
		r := NewBatchRunner(BatchConfig{})
		if r.cfg.MaxParallel != 5 {
			t.Fatalf("MaxParallel = %d, want 5", r.cfg.MaxParallel)
		}
	})

	t.Run("empty BaseBranch becomes main", func(t *testing.T) {
		r := NewBatchRunner(BatchConfig{})
		if r.cfg.BaseBranch != "main" {
			t.Fatalf("BaseBranch = %q, want %q", r.cfg.BaseBranch, "main")
		}
	})

	t.Run("explicit values are preserved", func(t *testing.T) {
		r := NewBatchRunner(BatchConfig{MaxParallel: 3, BaseBranch: "develop"})
		if r.cfg.MaxParallel != 3 {
			t.Fatalf("MaxParallel = %d, want 3", r.cfg.MaxParallel)
		}
		if r.cfg.BaseBranch != "develop" {
			t.Fatalf("BaseBranch = %q, want %q", r.cfg.BaseBranch, "develop")
		}
	})
}

func TestParseSubtasksEnvelope(t *testing.T) {
	input := `{"subtasks":[{"name":"foo","description":"do foo"},{"name":"bar","description":"do bar"}]}`
	got, err := parseSubtasks(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "foo" || got[0].Description != "do foo" {
		t.Fatalf("got[0] = %+v", got[0])
	}
	if got[1].Name != "bar" || got[1].Description != "do bar" {
		t.Fatalf("got[1] = %+v", got[1])
	}
}

func TestParseSubtasksFlatArray(t *testing.T) {
	input := `[{"name":"update_imports","description":"update all import paths"},{"name":"add_tests","description":"add unit tests"}]`
	got, err := parseSubtasks(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "update_imports" {
		t.Fatalf("got[0].Name = %q", got[0].Name)
	}
}

func TestParseSubtasksEmbeddedInProse(t *testing.T) {
	input := `Here are the subtasks I identified:
{"subtasks":[{"name":"task_a","description":"do A"},{"name":"task_b","description":"do B"}]}
Let me know if you want changes.`
	got, err := parseSubtasks(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

func TestParseSubtasksEmbeddedArray(t *testing.T) {
	input := `Sure, here is the JSON array:
[{"name":"one","description":"first"},{"name":"two","description":"second"}]
Done.`
	got, err := parseSubtasks(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

func TestParseSubtasksInvalid(t *testing.T) {
	_, err := parseSubtasks("this is not json at all")
	if err == nil {
		t.Fatal("expected error for non-JSON input")
	}
	if !strings.Contains(err.Error(), "could not parse subtasks") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBatchRunnerDecomposeWithMockAdapter(t *testing.T) {
	adapter := &stubAdapter{
		runResult: &Result{
			Content: `{"subtasks":[{"name":"step_one","description":"do step one"},{"name":"step_two","description":"do step two"}]}`,
		},
	}
	r := NewBatchRunner(BatchConfig{Adapter: adapter})

	subtasks, err := r.Decompose(context.Background(), "some big task")
	if err != nil {
		t.Fatalf("Decompose error: %v", err)
	}
	if len(subtasks) != 2 {
		t.Fatalf("len(subtasks) = %d, want 2", len(subtasks))
	}
	if subtasks[0].Name != "step_one" {
		t.Fatalf("subtasks[0].Name = %q", subtasks[0].Name)
	}
}

func TestBatchRunnerDecomposeAdapterError(t *testing.T) {
	adapter := &stubAdapter{
		runErr: fmt.Errorf("adapter down"),
	}
	r := NewBatchRunner(BatchConfig{Adapter: adapter})

	_, err := r.Decompose(context.Background(), "task")
	if err == nil {
		t.Fatal("expected error when adapter fails")
	}
	if !strings.Contains(err.Error(), "decompose request") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBatchTruncate(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello..."},
		{"short", 5, "short"},
		{"exactly5", 5, "ex..."},
	}
	for _, tt := range tests {
		got := batchTruncate(tt.input, tt.max)
		if got != tt.want {
			t.Errorf("batchTruncate(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
		}
	}
}
