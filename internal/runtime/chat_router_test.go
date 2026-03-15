package runtime

import "testing"

func TestDecideChatRoute(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantAction  ChatAction
		wantReason  string
		wantClarify bool
	}{
		{
			name:       "simple question stays on reply path",
			input:      "What does queue mode do?",
			wantAction: ChatActionReply,
			wantReason: "default_reply",
		},
		{
			name:       "forced job prefix launches job",
			input:      "job: investigate failing tests in internal/runtime and open a PR",
			wantAction: ChatActionLaunchJob,
			wantReason: "forced_job_prefix:job:",
		},
		{
			name:        "forced job prefix without scope asks clarification",
			input:       "task: fix it",
			wantAction:  ChatActionClarify,
			wantReason:  "forced_job_missing_scope",
			wantClarify: true,
		},
		{
			name:        "underspecified action asks clarification",
			input:       "can you investigate this?",
			wantAction:  ChatActionClarify,
			wantReason:  "ambiguous_task_request",
			wantClarify: true,
		},
		{
			name:       "obvious repo work launches job",
			input:      "Investigate the failing tests in the repo, check the logs, and open a PR with the fix.",
			wantAction: ChatActionLaunchJob,
			wantReason: "heavy_work_request",
		},
		{
			name:       "multi-line implementation request launches job",
			input:      "Implement this in the codebase:\n- inspect the repo\n- fix the bug\n- run tests",
			wantAction: ChatActionLaunchJob,
			wantReason: "heavy_work_request",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideChatRoute(tc.input)
			if got.Action != tc.wantAction {
				t.Fatalf("Action = %q, want %q", got.Action, tc.wantAction)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if tc.wantClarify && got.Clarification == "" {
				t.Fatal("expected clarification text, got empty string")
			}
			if !tc.wantClarify && got.Clarification != "" {
				t.Fatalf("expected empty clarification, got %q", got.Clarification)
			}
		})
	}
}
