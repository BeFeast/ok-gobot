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

func TestFormatTelegramJobStatusIsHumanReadable(t *testing.T) {
	out := formatTelegramJobStatus(jobStatusCompleted, "Extra detail.")
	if !strings.Contains(out, "✅ Done") || !strings.Contains(out, "Extra detail.") {
		t.Fatalf("expected human phrase with detail, got %q", out)
	}
	// The internal job id must never leak into chat-facing status text.
	for _, forbidden := range []string{"Job ", "tg-", "Status:"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("internal marker %q leaked into %q", forbidden, out)
		}
	}
}

func TestFormatTelegramJobStatusCoversAllStates(t *testing.T) {
	for _, status := range []telegramJobStatus{
		jobStatusAccepted, jobStatusQueued, jobStatusRunning,
		jobStatusCompleted, jobStatusFailed, jobStatusCancelled,
	} {
		out := formatTelegramJobStatus(status, "")
		if out == "" {
			t.Fatalf("status %q rendered empty", status)
		}
		if strings.Contains(out, string(status)) && status != jobStatusQueued {
			// Raw status words like "accepted"/"running" are internal vocabulary.
			t.Fatalf("raw status word leaked for %q: %q", status, out)
		}
	}
}

func TestLiveStreamEditorRunningHeaderIsAnimatedAndHuman(t *testing.T) {
	e := &LiveStreamEditor{}

	e.mu.Lock()
	first := e.formatLocked()
	e.frame++
	second := e.formatLocked()
	e.mu.Unlock()

	if !strings.Contains(first, "Working…") {
		t.Fatalf("expected running phrase in %q", first)
	}
	if strings.Contains(first, "tg-123") || strings.Contains(first, "Job ") {
		t.Fatalf("job id leaked into placeholder: %q", first)
	}
	if first == second {
		t.Fatalf("spinner frame did not advance: %q", first)
	}
}

func TestLiveStreamEditorHumanizesToolLines(t *testing.T) {
	e := &LiveStreamEditor{}
	e.toolLines = []toolStatusLine{
		{name: "memory_search", done: true},
		{name: "image_gen"},
		{name: "some_custom_tool", failed: true},
	}

	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()

	for _, want := range []string{"searching memory ✓", "painting an image…", "⚙️ some_custom_tool ✗"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
	if strings.Contains(out, "memory_search") {
		t.Fatalf("raw tool name leaked into %q", out)
	}
}
