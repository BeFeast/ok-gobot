package app

import (
	"errors"
	"strings"
	"testing"
)

func TestDescribeDegradedStartNamesConfiguredFallbacks(t *testing.T) {
	t.Parallel()

	got := describeDegradedStart(errors.New("boom"), []string{"model-b", "model-c"})
	for _, want := range []string{"DEGRADED", "boom", "model-b", "model-c"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q is missing %q", got, want)
		}
	}
}

// The 2026-08-23 outages happened on exactly this branch: preflight failed with
// no fallbacks configured, startup exited, and systemd gave up after its start
// limit. Starting degraded is the behaviour under test.
func TestDescribeDegradedStartWithoutFallbacksStillStarts(t *testing.T) {
	t.Parallel()

	got := describeDegradedStart(errors.New("boom"), nil)
	for _, want := range []string{"DEGRADED", "boom", "no fallback_models"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, "fallbacks ") {
		t.Errorf("message must not name fallbacks it does not have: %q", got)
	}
}
