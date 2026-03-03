package bot

import (
	"testing"
)

func TestParseTaskArgs(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		wantDesc    string
		wantModel   string
		wantThink   string
		wantErrMsg  string
	}{
		{
			name:       "empty payload",
			payload:    "",
			wantErrMsg: "no task description provided",
		},
		{
			name:       "whitespace only",
			payload:    "   ",
			wantErrMsg: "no task description provided",
		},
		{
			name:     "description only",
			payload:  "write a hello world program",
			wantDesc: "write a hello world program",
		},
		{
			name:      "with model flag",
			payload:   "summarise this --model sonnet",
			wantDesc:  "summarise this",
			wantModel: "sonnet",
		},
		{
			name:      "with thinking flag",
			payload:   "solve this hard problem --thinking high",
			wantDesc:  "solve this hard problem",
			wantThink: "high",
		},
		{
			name:      "model and thinking flags",
			payload:   "do the task --model opus --thinking medium",
			wantDesc:  "do the task",
			wantModel: "opus",
			wantThink: "medium",
		},
		{
			name:      "flags before description words",
			payload:   "--model haiku write a poem",
			wantDesc:  "write a poem",
			wantModel: "haiku",
		},
		{
			name:       "model flag without value",
			payload:    "some task --model",
			wantErrMsg: "--model requires a value",
		},
		{
			name:       "thinking flag without value",
			payload:    "some task --thinking",
			wantErrMsg: "--thinking requires a value",
		},
		{
			name:       "invalid thinking level",
			payload:    "some task --thinking ultra",
			wantErrMsg: "--thinking must be one of: off, low, medium, high",
		},
		{
			name:      "thinking level off",
			payload:   "some task --thinking off",
			wantDesc:  "some task",
			wantThink: "off",
		},
		{
			name:      "thinking level low",
			payload:   "some task --thinking low",
			wantDesc:  "some task",
			wantThink: "low",
		},
		{
			name:      "flags only, no description",
			payload:   "--model sonnet --thinking high",
			wantErrMsg: "no task description provided",
		},
		{
			name:      "multi-word description with both flags",
			payload:   "analyse the code and suggest improvements --model claude-3-5-sonnet --thinking low",
			wantDesc:  "analyse the code and suggest improvements",
			wantModel: "claude-3-5-sonnet",
			wantThink: "low",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := parseTaskArgs(tc.payload)

			if tc.wantErrMsg != "" {
				if err == nil {
					t.Fatalf("expected error %q, got nil", tc.wantErrMsg)
				}
				if err.Error() != tc.wantErrMsg {
					t.Fatalf("expected error %q, got %q", tc.wantErrMsg, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if req.Description != tc.wantDesc {
				t.Errorf("Description: got %q, want %q", req.Description, tc.wantDesc)
			}
			if req.Model != tc.wantModel {
				t.Errorf("Model: got %q, want %q", req.Model, tc.wantModel)
			}
			if req.ThinkLevel != tc.wantThink {
				t.Errorf("ThinkLevel: got %q, want %q", req.ThinkLevel, tc.wantThink)
			}
		})
	}
}
