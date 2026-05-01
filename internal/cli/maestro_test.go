package cli

import (
	"bytes"
	"strings"
	"testing"

	"ok-gobot/internal/maestro"
)

func TestRenderMaestroDecisionExplainsNoWorker(t *testing.T) {
	t.Parallel()

	decision := maestro.Decision{
		Policy: maestro.Policy{ReadyLabel: "ready"},
		Skipped: []maestro.CandidateDecision{{
			Issue:       maestro.Issue{Number: 7, Title: "blocked task"},
			SkipReasons: []string{`hard-exclude label "blocked"`},
		}},
	}
	var out bytes.Buffer
	renderMaestroDecision(&out, decision, "status")

	got := out.String()
	for _, want := range []string{
		"No worker running: no eligible issue after strict intake policy.",
		"Skipped candidates:",
		`#7 blocked task - hard-exclude label "blocked"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %q", want, got)
		}
	}
}

func TestRenderMaestroDecisionShowsOverride(t *testing.T) {
	t.Parallel()

	decision := maestro.Decision{
		Policy: maestro.Policy{ReadyLabel: "ready", Override: true, OverrideReason: "ops reviewed"},
		Next: &maestro.CandidateDecision{
			Issue:           maestro.Issue{Number: 8, Title: "force task"},
			OverrideUsed:    true,
			OverrideReasons: []string{`missing ready label "ready"`},
		},
	}
	var out bytes.Buffer
	renderMaestroDecision(&out, decision, "dry-run")

	got := out.String()
	for _, want := range []string{
		"Override: ENABLED (maintainer override: ops reviewed)",
		"Selected by maintainer override.",
		`Override bypassed: missing ready label "ready"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %q", want, got)
		}
	}
}
