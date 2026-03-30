package ai

import (
	"testing"
)

func TestRouter_Route_ExactMatch(t *testing.T) {
	r := NewRouter(map[string]string{
		"vision":    "openai/gpt-4o",
		"summarize": "openai/gpt-4o-mini",
		"coding":    "moonshotai/kimi-k2.5",
	}, "default-model")

	tests := []struct {
		taskType    TaskType
		wantModel   string
		wantContain string // substring expected in reason
	}{
		{TaskTypeVision, "openai/gpt-4o", "task_type=vision"},
		{TaskTypeSummarize, "openai/gpt-4o-mini", "task_type=summarize"},
		{TaskTypeCoding, "moonshotai/kimi-k2.5", "task_type=coding"},
	}

	for _, tt := range tests {
		model, reason := r.Route(tt.taskType)
		if model != tt.wantModel {
			t.Errorf("Route(%s) model = %q, want %q", tt.taskType, model, tt.wantModel)
		}
		if tt.wantContain != "" && reason == "" {
			t.Errorf("Route(%s) reason is empty, want non-empty", tt.taskType)
		}
	}
}

func TestRouter_Route_FallbackToDefaultRoute(t *testing.T) {
	r := NewRouter(map[string]string{
		"default": "fallback-model",
	}, "global-default")

	model, reason := r.Route(TaskTypeCoding)
	if model != "fallback-model" {
		t.Errorf("expected fallback-model, got %q", model)
	}
	if reason == "" {
		t.Error("reason should not be empty")
	}
}

func TestRouter_Route_FallbackToGlobalDefault(t *testing.T) {
	r := NewRouter(map[string]string{
		"vision": "vision-model",
	}, "global-default")

	model, reason := r.Route(TaskTypeCoding)
	if model != "global-default" {
		t.Errorf("expected global-default, got %q", model)
	}
	if reason == "" {
		t.Error("reason should not be empty")
	}
}

func TestRouter_Route_EmptyRoutes(t *testing.T) {
	r := NewRouter(nil, "global-default")

	model, reason := r.Route(TaskTypeVision)
	if model != "global-default" {
		t.Errorf("expected global-default, got %q", model)
	}
	if reason == "" {
		t.Error("reason should not be empty")
	}
}

func TestRouter_Route_NilRouter(t *testing.T) {
	var r *Router
	model, reason := r.Route(TaskTypeVision)
	if model != "" {
		t.Errorf("nil router should return empty model, got %q", model)
	}
	if reason == "" {
		t.Error("nil router should return a non-empty reason")
	}
}

func TestRouter_HasRoutes(t *testing.T) {
	noRoutes := NewRouter(nil, "x")
	if noRoutes.HasRoutes() {
		t.Error("expected HasRoutes() == false for empty routes")
	}

	withRoutes := NewRouter(map[string]string{"coding": "model-x"}, "x")
	if !withRoutes.HasRoutes() {
		t.Error("expected HasRoutes() == true when routes are set")
	}
}

func TestRouter_CaseInsensitiveRoutes(t *testing.T) {
	r := NewRouter(map[string]string{
		"CODING":    "coding-model",
		"SUMMARIZE": "sum-model",
	}, "default")

	model, _ := r.Route(TaskTypeCoding)
	if model != "coding-model" {
		t.Errorf("expected coding-model (case-insensitive), got %q", model)
	}
}

func TestDetectTaskType_ExplicitTag(t *testing.T) {
	tests := []struct {
		message  string
		wantType TaskType
	}{
		{"[task:vision] describe this image", TaskTypeVision},
		{"please [task:summarize] this document", TaskTypeSummarize},
		{"[task:reasoning] solve this logic puzzle", TaskTypeReasoning},
		{"[task:coding] write a Go function", TaskTypeCoding},
		{"no tag here", TaskTypeDefault},
		{"", TaskTypeDefault},
	}

	for _, tt := range tests {
		got := DetectTaskType(tt.message)
		if got != tt.wantType {
			t.Errorf("DetectTaskType(%q) = %q, want %q", tt.message, got, tt.wantType)
		}
	}
}

func TestDetectTaskType_CaseInsensitiveTag(t *testing.T) {
	got := DetectTaskType("[TASK:VISION] image here")
	if got != TaskTypeVision {
		t.Errorf("expected TaskTypeVision for uppercased tag, got %q", got)
	}
}

func TestStripTaskTag(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"[task:coding] write a function", "write a function"},
		{"[task:vision] describe this", "describe this"},
		{"no tag here", "no tag here"},
		{"[task:summarize] text [task:coding] more", "text  more"},
		{"[TASK:VISION] uppercase tag", "uppercase tag"},
	}

	for _, tt := range tests {
		got := StripTaskTag(tt.input)
		if got != tt.want {
			t.Errorf("StripTaskTag(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
