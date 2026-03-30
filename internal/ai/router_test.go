package ai

import "testing"

func TestParseTaskType(t *testing.T) {
	tests := []struct {
		input string
		want  TaskType
	}{
		{"vision", TaskTypeVision},
		{"summarize", TaskTypeSummarize},
		{"reasoning", TaskTypeReasoning},
		{"coding", TaskTypeCoding},
		{"default", TaskTypeDefault},
		{"unknown", TaskTypeDefault},
		{"", TaskTypeDefault},
		{"VISION", TaskTypeDefault}, // case-sensitive
	}
	for _, tt := range tests {
		got := ParseTaskType(tt.input)
		if got != tt.want {
			t.Errorf("ParseTaskType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestModelRouter_Route_PerTypeRule(t *testing.T) {
	routing := RoutingConfig{
		Vision:    "ollama/llava",
		Summarize: "openai/gpt-4o-mini",
		Reasoning: "anthropic/claude-opus-4-5",
		Coding:    "moonshotai/kimi-k2.5",
	}
	router := NewModelRouter(routing, "default-model")

	cases := []struct {
		taskType   TaskType
		wantModel  string
		wantReason string
	}{
		{TaskTypeVision, "ollama/llava", "routing_rule:vision"},
		{TaskTypeSummarize, "openai/gpt-4o-mini", "routing_rule:summarize"},
		{TaskTypeReasoning, "anthropic/claude-opus-4-5", "routing_rule:reasoning"},
		{TaskTypeCoding, "moonshotai/kimi-k2.5", "routing_rule:coding"},
	}

	for _, tc := range cases {
		model, reason := router.Route(tc.taskType)
		if model != tc.wantModel {
			t.Errorf("Route(%q) model = %q, want %q", tc.taskType, model, tc.wantModel)
		}
		if reason != tc.wantReason {
			t.Errorf("Route(%q) reason = %q, want %q", tc.taskType, reason, tc.wantReason)
		}
	}
}

func TestModelRouter_Route_FallbackToRoutingDefault(t *testing.T) {
	routing := RoutingConfig{
		Default: "cheap-fallback-model",
		// No per-type rules set
	}
	router := NewModelRouter(routing, "global-model")

	for _, tt := range []TaskType{TaskTypeVision, TaskTypeSummarize, TaskTypeReasoning, TaskTypeCoding, TaskTypeDefault} {
		model, reason := router.Route(tt)
		if model != "cheap-fallback-model" {
			t.Errorf("Route(%q) = %q, want cheap-fallback-model", tt, model)
		}
		if reason != "routing_default" {
			t.Errorf("Route(%q) reason = %q, want routing_default", tt, reason)
		}
	}
}

func TestModelRouter_Route_FallbackToGlobalDefault(t *testing.T) {
	routing := RoutingConfig{} // nothing configured
	router := NewModelRouter(routing, "global-model")

	for _, tt := range []TaskType{TaskTypeVision, TaskTypeSummarize, TaskTypeReasoning, TaskTypeCoding, TaskTypeDefault} {
		model, reason := router.Route(tt)
		if model != "global-model" {
			t.Errorf("Route(%q) = %q, want global-model", tt, model)
		}
		if reason != "global_default" {
			t.Errorf("Route(%q) reason = %q, want global_default", tt, reason)
		}
	}
}

func TestModelRouter_Route_PerTypeOverridesDefault(t *testing.T) {
	// Per-type rules take priority over routing.default
	routing := RoutingConfig{
		Coding:  "kimi-for-coding",
		Default: "cheap-fallback",
	}
	router := NewModelRouter(routing, "global")

	model, reason := router.Route(TaskTypeCoding)
	if model != "kimi-for-coding" {
		t.Errorf("coding route = %q, want kimi-for-coding", model)
	}
	if reason != "routing_rule:coding" {
		t.Errorf("coding reason = %q, want routing_rule:coding", reason)
	}

	// other types should use routing.default
	model, reason = router.Route(TaskTypeSummarize)
	if model != "cheap-fallback" {
		t.Errorf("summarize route = %q, want cheap-fallback", model)
	}
	if reason != "routing_default" {
		t.Errorf("summarize reason = %q, want routing_default", reason)
	}
}

func TestModelRouter_RouteString(t *testing.T) {
	routing := RoutingConfig{
		Coding: "kimi",
	}
	router := NewModelRouter(routing, "global")

	model, _ := router.RouteString("coding")
	if model != "kimi" {
		t.Errorf("RouteString(coding) = %q, want kimi", model)
	}

	model, reason := router.RouteString("unknown_type")
	if model != "global" {
		t.Errorf("RouteString(unknown) = %q, want global", model)
	}
	if reason != "global_default" {
		t.Errorf("RouteString(unknown) reason = %q, want global_default", reason)
	}
}

func TestModelRouter_EmptyGlobalDefault(t *testing.T) {
	routing := RoutingConfig{}
	router := NewModelRouter(routing, "")

	model, reason := router.Route(TaskTypeCoding)
	if model != "" {
		t.Errorf("Route with empty defaults = %q, want empty string", model)
	}
	if reason != "global_default" {
		t.Errorf("reason = %q, want global_default", reason)
	}
}
