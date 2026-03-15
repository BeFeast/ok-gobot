package bot

import (
	"testing"
)

func TestParseTaskArgs(t *testing.T) {
	tests := []struct {
		name         string
		payload      string
		wantDesc     string
		wantModel    string
		wantThink    string
		wantMaxTools int
		wantMaxDur   string
		wantOutput   string
		wantSchema   string
		wantMemory   string
		wantErrMsg   string
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
			name:       "flags only, no description",
			payload:    "--model sonnet --thinking high",
			wantErrMsg: "no task description provided",
		},
		{
			name:      "multi-word description with both flags",
			payload:   "analyse the code and suggest improvements --model claude-3-5-sonnet --thinking low",
			wantDesc:  "analyse the code and suggest improvements",
			wantModel: "claude-3-5-sonnet",
			wantThink: "low",
		},
		{
			name:         "explicit budgets and contract",
			payload:      "summarise logs --max-tools 7 --max-duration 2m --output json --schema report_v1 --memory allow_writes",
			wantDesc:     "summarise logs",
			wantMaxTools: 7,
			wantMaxDur:   "2m0s",
			wantOutput:   "json",
			wantSchema:   "report_v1",
			wantMemory:   "allow_writes",
		},
		{
			name:       "invalid max tools",
			payload:    "some task --max-tools nope",
			wantErrMsg: "--max-tools must be a positive integer",
		},
		{
			name:       "invalid max duration",
			payload:    "some task --max-duration later",
			wantErrMsg: "--max-duration must be a valid positive duration",
		},
		{
			name:       "invalid output format",
			payload:    "some task --output xml",
			wantErrMsg: "--output must be one of: text, markdown, json",
		},
		{
			name:       "invalid memory policy",
			payload:    "some task --memory yes",
			wantErrMsg: "--memory must be one of: inherit, read_only, allow_writes",
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
			if req.MaxToolCalls != tc.wantMaxTools {
				t.Errorf("MaxToolCalls: got %d, want %d", req.MaxToolCalls, tc.wantMaxTools)
			}
			if tc.wantMaxDur != "" && req.MaxDuration.String() != tc.wantMaxDur {
				t.Errorf("MaxDuration: got %q, want %q", req.MaxDuration, tc.wantMaxDur)
			}
			if req.OutputFormat != tc.wantOutput {
				t.Errorf("OutputFormat: got %q, want %q", req.OutputFormat, tc.wantOutput)
			}
			if req.OutputSchema != tc.wantSchema {
				t.Errorf("OutputSchema: got %q, want %q", req.OutputSchema, tc.wantSchema)
			}
			if req.MemoryPolicy != tc.wantMemory {
				t.Errorf("MemoryPolicy: got %q, want %q", req.MemoryPolicy, tc.wantMemory)
			}
		})
	}
}
