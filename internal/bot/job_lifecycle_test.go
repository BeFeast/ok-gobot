package bot

import (
	"strings"
	"testing"
)

func TestNewTelegramJobIDHasPrefix(t *testing.T) {
	jobID := newTelegramJobID(42, 99)
	if !strings.HasPrefix(jobID, "tg-99-") {
		t.Fatalf("expected message-based job ID prefix, got %q", jobID)
	}
}

func TestFormatTelegramJobStatusIncludesLifecycle(t *testing.T) {
	out := formatTelegramJobStatus("tg-123", jobStatusCompleted, "Result delivered below.")
	for _, want := range []string{"Job tg-123", "Status: completed", "Result delivered below."} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
}

func TestLiveStreamEditorIncludesJobHeader(t *testing.T) {
	e := &LiveStreamEditor{
		jobID:     "tg-123",
		lifecycle: jobStatusRunning,
	}

	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()

	for _, want := range []string{"Job tg-123", "Status: running", "💭 Working…"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
}
