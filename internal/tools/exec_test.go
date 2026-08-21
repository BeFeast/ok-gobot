package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestExecTool(t *testing.T) *ExecTool {
	t.Helper()
	dir := t.TempDir()
	tool := NewExecTool(dir)
	tool.BackupDir = filepath.Join(dir, "backups")
	return tool
}

// Yolo: a non-read-only command runs immediately, no approval, no gate.
func TestExecYoloRunsImmediately(t *testing.T) {
	tool := newTestExecTool(t)
	out, err := tool.ExecuteJSON(context.Background(), map[string]string{"command": "echo yolo-hello && true"})
	if err != nil {
		t.Fatalf("command errored: %v", err)
	}
	if !strings.Contains(out, "yolo-hello") {
		t.Errorf("expected command output, got: %q", out)
	}
	if !strings.Contains(out, "exit=0") {
		t.Errorf("expected exit=0 footer, got: %q", out)
	}
}

// isReadOnly gates backups (not execution) and must be metachar- and
// boundary-aware.
func TestExecReadOnlyClassification(t *testing.T) {
	tool := newTestExecTool(t)
	readOnly := []string{"ls -la /home", "cat /etc/hostname", "systemctl status ok-gobot", "git log --oneline"}
	for _, c := range readOnly {
		if !tool.isReadOnly(c) {
			t.Errorf("%q should be read-only", c)
		}
	}
	mutating := []string{"ls; rm -rf /", "cat x | tee y", "echo z > file", "apt install foo", "catastrophe"}
	for _, c := range mutating {
		if tool.isReadOnly(c) {
			t.Errorf("%q must NOT be classified read-only", c)
		}
	}
}

// A mutating command that overwrites an existing file must back it up first, so
// the change is revertable.
func TestExecBacksUpBeforeOverwrite(t *testing.T) {
	tool := newTestExecTool(t)
	target := filepath.Join(tool.WorkDir, "conf.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := tool.ExecuteJSON(context.Background(), map[string]string{"command": ": > " + target})
	if err != nil {
		t.Fatalf("command errored: %v", err)
	}
	// The command truncated the file (mutation happened).
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Errorf("expected file truncated, still has %d bytes", len(b))
	}
	if !strings.Contains(out, "backup=") || !strings.Contains(out, "file(s)") {
		t.Errorf("expected a backup note in output, got: %q", out)
	}
	// A backup copy with the ORIGINAL content must exist under BackupDir.
	found := false
	_ = filepath.WalkDir(tool.BackupDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(p); string(b) == "ORIGINAL" {
			found = true
		}
		return nil
	})
	if !found {
		t.Error("no backup copy with the original content was created before overwrite")
	}
}

// Read-only commands skip the backup step.
func TestExecReadOnlySkipsBackup(t *testing.T) {
	tool := newTestExecTool(t)
	out, err := tool.ExecuteJSON(context.Background(), map[string]string{"command": "echo hi"})
	if err != nil {
		t.Fatalf("command errored: %v", err)
	}
	if !strings.Contains(out, "backup=readonly") {
		t.Errorf("expected backup=readonly, got: %q", out)
	}
}

// A command exceeding its timeout is killed and reported, not left hanging.
func TestExecTimeout(t *testing.T) {
	tool := newTestExecTool(t)
	start := time.Now()
	out, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"command": "sleep 30",
		"timeout": "1",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("timeout path should not error, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("command was not killed promptly: took %s", elapsed)
	}
	if !strings.Contains(out, "timed out") {
		t.Errorf("expected timed-out notice, got: %q", out)
	}
}

// Estop blocks the whole tool family before the command runs.
func TestExecBlockedByEmergencyStop(t *testing.T) {
	reg := NewRegistryWithEmergencyStop(stubEmergencyStopProvider{enabled: true})
	reg.Register(NewExecTool(""))

	_, err := reg.Execute(context.Background(), "exec", "echo blocked")
	if err == nil {
		t.Fatal("expected estop to block exec")
	}
	denial, ok := IsToolDenial(err)
	if !ok {
		t.Fatalf("expected *ToolDenial, got %T: %v", err, err)
	}
	if denial.Family != "exec" {
		t.Errorf("Family = %q, want %q", denial.Family, "exec")
	}
}

// The estop guard must forward schema + JSON so named params survive.
func TestExecEstopGuardForwardsJSON(t *testing.T) {
	reg := NewRegistryWithEmergencyStop(stubEmergencyStopProvider{enabled: false})
	reg.Register(NewExecTool(""))
	tool, _ := reg.Get("exec")
	if _, ok := tool.(interface {
		ExecuteJSON(context.Context, map[string]string) (string, error)
	}); !ok {
		t.Fatal("estop-wrapped exec tool lost its ExecuteJSON method")
	}
	if _, ok := tool.(ToolSchema); !ok {
		t.Fatal("estop-wrapped exec tool lost its GetSchema method")
	}
}

// exec must be ChatScoped so the resolver can bind the chatID for audit.
func TestExecIsChatScoped(t *testing.T) {
	var _ ChatScoped = NewExecTool("")
}

// BindChat produces a chat-bound copy; the audit sink receives that chatID.
func TestExecBindChatAttributesAudit(t *testing.T) {
	base := newTestExecTool(t)
	sink := &captureAuditSink{}
	base.AuditSink = sink
	bound, ok := base.BindChat(nil, 77).(*ExecTool)
	if !ok {
		t.Fatal("BindChat did not return an *ExecTool")
	}
	if _, err := bound.ExecuteJSON(context.Background(), map[string]string{"command": "echo hi"}); err != nil {
		t.Fatalf("bound exec errored: %v", err)
	}
	if sink.chatID != 77 {
		t.Errorf("audit sink got chatID=%d, want 77", sink.chatID)
	}
	if base.boundChatID != 0 {
		t.Errorf("BindChat mutated the base tool: boundChatID=%d", base.boundChatID)
	}
}

type captureAuditSink struct {
	chatID  int64
	command string
	backup  string
}

func (c *captureAuditSink) RecordExec(chatID int64, command string, exitCode int, dur time.Duration, backup string) {
	c.chatID = chatID
	c.command = command
	c.backup = backup
}
