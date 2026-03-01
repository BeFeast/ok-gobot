package bot

import (
	"strings"
	"testing"
)

func TestToolStatusTracker_Empty(t *testing.T) {
	var tr ToolStatusTracker
	out := tr.Format()
	if out != "💭 Working…" {
		t.Errorf("expected default placeholder, got %q", out)
	}
	if tr.HasAny() {
		t.Error("expected HasAny=false on empty tracker")
	}
}

func TestToolStatusTracker_StartedLine(t *testing.T) {
	var tr ToolStatusTracker
	tr.OnStarted("search")
	out := tr.Format()
	if !strings.Contains(out, "⚙️ search…") {
		t.Errorf("expected in-progress line, got %q", out)
	}
	if !tr.HasAny() {
		t.Error("expected HasAny=true after OnStarted")
	}
}

func TestToolStatusTracker_FinishedSuccess(t *testing.T) {
	var tr ToolStatusTracker
	tr.OnStarted("search")
	tr.OnFinished("search", false)
	out := tr.Format()
	if !strings.Contains(out, "✅ search") {
		t.Errorf("expected success line, got %q", out)
	}
}

func TestToolStatusTracker_FinishedError(t *testing.T) {
	var tr ToolStatusTracker
	tr.OnStarted("patch")
	tr.OnFinished("patch", true)
	out := tr.Format()
	if !strings.Contains(out, "❌ patch") {
		t.Errorf("expected error line, got %q", out)
	}
}

func TestToolStatusTracker_MaxLines(t *testing.T) {
	var tr ToolStatusTracker
	// Add more than maxToolStatusLines entries
	tools := []string{"a", "b", "c", "d", "e", "f", "g"}
	for _, name := range tools {
		tr.OnStarted(name)
		tr.OnFinished(name, false)
	}
	out := tr.Format()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > maxToolStatusLines {
		t.Errorf("expected at most %d lines, got %d: %s", maxToolStatusLines, len(lines), out)
	}
	// Last entry should always be visible
	if !strings.Contains(out, "g") {
		t.Errorf("expected last tool 'g' to be visible, got %q", out)
	}
}

func TestToolStatusTracker_MultipleConcurrent(t *testing.T) {
	var tr ToolStatusTracker
	tr.OnStarted("fetch")
	tr.OnStarted("parse")
	out := tr.Format()
	if !strings.Contains(out, "⚙️ fetch…") {
		t.Errorf("expected fetch in-progress, got %q", out)
	}
	if !strings.Contains(out, "⚙️ parse…") {
		t.Errorf("expected parse in-progress, got %q", out)
	}
	tr.OnFinished("fetch", false)
	out = tr.Format()
	if !strings.Contains(out, "✅ fetch") {
		t.Errorf("expected fetch done, got %q", out)
	}
	if !strings.Contains(out, "⚙️ parse…") {
		t.Errorf("expected parse still in-progress, got %q", out)
	}
}
