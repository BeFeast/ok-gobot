package tools

import (
	"errors"
	"strings"
	"testing"
)

func TestToolDenial_Error(t *testing.T) {
	t.Parallel()

	d := &ToolDenial{
		ToolName: "local",
		Family:   "local",
		Reason:   "estop active",
		Hint:     "Run `/estop off` to re-enable.",
	}

	got := d.Error()
	if !strings.Contains(got, `"local"`) {
		t.Errorf("Error() should contain tool name, got %q", got)
	}
	if !strings.Contains(got, "estop active") {
		t.Errorf("Error() should contain reason, got %q", got)
	}
}

func TestToolDenial_FormatTelegram(t *testing.T) {
	t.Parallel()

	d := &ToolDenial{
		ToolName: "browser",
		Family:   "browser",
		Reason:   "estop active",
		Hint:     "Run `/estop off` or `ok-gobot estop off` to re-enable.",
	}

	got := d.FormatTelegram()
	if !strings.Contains(got, "🚫") {
		t.Errorf("FormatTelegram() should contain prohibited emoji, got %q", got)
	}
	if !strings.Contains(got, `"browser"`) {
		t.Errorf("FormatTelegram() should contain tool name, got %q", got)
	}
	if !strings.Contains(got, "estop active") {
		t.Errorf("FormatTelegram() should contain reason, got %q", got)
	}
	if !strings.Contains(got, "/estop off") {
		t.Errorf("FormatTelegram() should contain re-enable hint, got %q", got)
	}
}

func TestToolDenial_FormatPlain(t *testing.T) {
	t.Parallel()

	d := &ToolDenial{
		ToolName: "ssh",
		Family:   "ssh",
		Reason:   "estop active",
		Hint:     "Run `/estop off` to re-enable.",
	}

	got := d.FormatPlain()
	if !strings.HasPrefix(got, "DENIED:") {
		t.Errorf("FormatPlain() should start with DENIED:, got %q", got)
	}
	if !strings.Contains(got, "ssh") {
		t.Errorf("FormatPlain() should contain tool family, got %q", got)
	}
	if !strings.Contains(got, "/estop off") {
		t.Errorf("FormatPlain() should contain hint, got %q", got)
	}
}

func TestIsToolDenial_WithDenial(t *testing.T) {
	t.Parallel()

	d := &ToolDenial{ToolName: "cron", Reason: "estop active"}
	got := IsToolDenial(d)
	if got == nil {
		t.Fatal("expected IsToolDenial to return non-nil for *ToolDenial")
	}
	if got.ToolName != "cron" {
		t.Errorf("expected ToolName=cron, got %s", got.ToolName)
	}
}

func TestIsToolDenial_WithOtherError(t *testing.T) {
	t.Parallel()

	err := errors.New("something else")
	got := IsToolDenial(err)
	if got != nil {
		t.Fatal("expected IsToolDenial to return nil for non-denial error")
	}
}

func TestIsToolDenial_Nil(t *testing.T) {
	t.Parallel()

	got := IsToolDenial(nil)
	if got != nil {
		t.Fatal("expected IsToolDenial to return nil for nil error")
	}
}

func TestEstopGuardReturnsDenial(t *testing.T) {
	t.Parallel()

	reg := NewRegistryWithEmergencyStop(stubEmergencyStopProvider{enabled: true})
	tool := &stubTool{name: "local"}
	reg.Register(tool)

	_, err := reg.Execute(nil, "local", "ls")
	if err == nil {
		t.Fatal("expected error from blocked tool")
	}

	denial := IsToolDenial(err)
	if denial == nil {
		t.Fatal("expected ToolDenial error from estop guard")
	}
	if denial.ToolName != "local" {
		t.Errorf("expected ToolName=local, got %s", denial.ToolName)
	}
	if denial.Family != "local" {
		t.Errorf("expected Family=local, got %s", denial.Family)
	}
	if denial.Reason != "estop active" {
		t.Errorf("expected Reason='estop active', got %s", denial.Reason)
	}
	if denial.Hint == "" {
		t.Error("expected non-empty Hint")
	}
}
