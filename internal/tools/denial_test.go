package tools

import (
	"fmt"
	"strings"
	"testing"
)

func TestToolDenialError(t *testing.T) {
	t.Parallel()
	d := &ToolDenial{
		ToolName:    "local",
		Family:      "local",
		Reason:      "estop active",
		Remediation: "Run `/estop off` to re-enable.",
	}
	got := d.Error()
	if !strings.Contains(got, `"local"`) || !strings.Contains(got, "estop active") {
		t.Fatalf("Error() = %q, want tool name and reason", got)
	}
}

func TestToolDenialFormatMarkdown(t *testing.T) {
	t.Parallel()
	d := &ToolDenial{
		ToolName:    "local",
		Family:      "local",
		Reason:      "estop active",
		Remediation: "Run `/estop off` or `ok-gobot estop off` to re-enable.",
	}
	md := d.FormatMarkdown()
	if !strings.Contains(md, "🚫") {
		t.Error("FormatMarkdown should contain 🚫 emoji")
	}
	if !strings.Contains(md, `"local"`) {
		t.Error("FormatMarkdown should contain tool name")
	}
	if !strings.Contains(md, "estop active") {
		t.Error("FormatMarkdown should contain reason")
	}
	if !strings.Contains(md, "/estop off") {
		t.Error("FormatMarkdown should contain remediation")
	}
}

func TestToolDenialFormatPlain(t *testing.T) {
	t.Parallel()
	d := &ToolDenial{
		ToolName:    "browser",
		Family:      "browser",
		Reason:      "estop active",
		Remediation: "Run `/estop off` to re-enable.",
	}
	plain := d.FormatPlain()
	if !strings.HasPrefix(plain, "DENIED:") {
		t.Errorf("FormatPlain should start with DENIED:, got %q", plain)
	}
	if !strings.Contains(plain, "browser") {
		t.Error("FormatPlain should contain tool name")
	}
}

func TestToolDenialFormatMarkdownNoRemediation(t *testing.T) {
	t.Parallel()
	d := &ToolDenial{
		ToolName: "ssh",
		Family:   "ssh",
		Reason:   "policy block",
	}
	md := d.FormatMarkdown()
	if strings.Contains(md, "\n") {
		t.Error("FormatMarkdown without remediation should be a single line")
	}
}

func TestIsToolDenial(t *testing.T) {
	t.Parallel()
	d := &ToolDenial{ToolName: "cron", Family: "cron", Reason: "estop active"}

	got, ok := IsToolDenial(d)
	if !ok || got != d {
		t.Fatal("IsToolDenial should match *ToolDenial directly")
	}

	_, ok = IsToolDenial(fmt.Errorf("some other error"))
	if ok {
		t.Fatal("IsToolDenial should not match non-ToolDenial errors")
	}

	_, ok = IsToolDenial(nil)
	if ok {
		t.Fatal("IsToolDenial should not match nil")
	}
}

func TestEstopGuardReturnsToolDenial(t *testing.T) {
	t.Parallel()

	reg := NewRegistryWithEmergencyStop(stubEmergencyStopProvider{enabled: true})
	tool := &stubTool{name: "local"}
	reg.Register(tool)

	_, err := reg.Execute(nil, "local", "ls")
	if err == nil {
		t.Fatal("expected estop to block tool")
	}

	denial, ok := IsToolDenial(err)
	if !ok {
		t.Fatalf("expected *ToolDenial, got %T: %v", err, err)
	}
	if denial.ToolName != "local" {
		t.Errorf("ToolName = %q, want %q", denial.ToolName, "local")
	}
	if denial.Family != "local" {
		t.Errorf("Family = %q, want %q", denial.Family, "local")
	}
	if denial.Reason != "estop active" {
		t.Errorf("Reason = %q, want %q", denial.Reason, "estop active")
	}
	if denial.Remediation == "" {
		t.Error("Remediation should not be empty")
	}
	if tool.called != 0 {
		t.Fatalf("tool should not have been called, called=%d", tool.called)
	}
}
