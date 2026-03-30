package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHookScript writes a bash script to dir/<name>.sh that appends the
// named environment variable to a sentinel file.
func writeHookScript(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name+".sh")
	content := "#!/bin/bash\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("writeHookScript %s: %v", path, err)
	}
}

// TestNewHookRunner_MissingDir verifies that NewHookRunner returns nil when
// the directory does not exist (hooks are optional).
func TestNewHookRunner_MissingDir(t *testing.T) {
	hr := NewHookRunner("/nonexistent/path/that/does/not/exist")
	if hr != nil {
		t.Error("expected nil HookRunner for missing directory, got non-nil")
	}
}

// TestNewHookRunner_ValidDir verifies that NewHookRunner returns a non-nil
// runner when the directory exists.
func TestNewHookRunner_ValidDir(t *testing.T) {
	dir := t.TempDir()
	hr := NewHookRunner(dir)
	if hr == nil {
		t.Error("expected non-nil HookRunner for existing directory")
	}
}

// TestNilHookRunner_NoOp verifies that all Run* methods on a nil *HookRunner
// do not panic.
func TestNilHookRunner_NoOp(t *testing.T) {
	var hr *HookRunner
	// None of these should panic.
	hr.RunSessionStart("hello")
	hr.RunPreToolUse("web_search", `{"query":"test"}`)
	hr.RunPostToolUse("web_search", `{"query":"test"}`, "result", nil)
	hr.RunPostToolUse("web_search", `{"query":"test"}`, "result", errors.New("oops"))
	hr.RunSessionEnd("done", []string{"web_search"})
}

// TestHookRunner_SessionStart verifies that the session_start hook fires with
// the correct HOOK_EVENT and HOOK_MESSAGE environment variables.
func TestHookRunner_SessionStart(t *testing.T) {
	dir := t.TempDir()
	sentinelFile := filepath.Join(dir, "session_start.out")

	writeHookScript(t, dir, "session_start", fmt.Sprintf(
		`echo "$HOOK_EVENT:$HOOK_MESSAGE" >> %s`, sentinelFile,
	))

	hr := NewHookRunner(dir)
	if hr == nil {
		t.Fatal("expected non-nil HookRunner")
	}

	hr.RunSessionStart("hello world")

	data, err := os.ReadFile(sentinelFile)
	if err != nil {
		t.Fatalf("sentinel file not created: %v", err)
	}
	line := strings.TrimSpace(string(data))
	if line != "session_start:hello world" {
		t.Errorf("unexpected output: %q", line)
	}
}

// TestHookRunner_PreToolUse verifies that the pre_tool_use hook receives
// HOOK_TOOL_NAME and HOOK_TOOL_INPUT.
func TestHookRunner_PreToolUse(t *testing.T) {
	dir := t.TempDir()
	sentinelFile := filepath.Join(dir, "pre_tool_use.out")

	writeHookScript(t, dir, "pre_tool_use", fmt.Sprintf(
		`echo "$HOOK_TOOL_NAME|$HOOK_TOOL_INPUT" >> %s`, sentinelFile,
	))

	hr := NewHookRunner(dir)
	hr.RunPreToolUse("web_search", `{"query":"go"}`)

	data, err := os.ReadFile(sentinelFile)
	if err != nil {
		t.Fatalf("sentinel file not created: %v", err)
	}
	line := strings.TrimSpace(string(data))
	if line != `web_search|{"query":"go"}` {
		t.Errorf("unexpected output: %q", line)
	}
}

// TestHookRunner_PostToolUse_Success verifies that the post_tool_use hook
// receives the expected vars when the tool succeeded.
func TestHookRunner_PostToolUse_Success(t *testing.T) {
	dir := t.TempDir()
	sentinelFile := filepath.Join(dir, "post_tool_use.out")

	writeHookScript(t, dir, "post_tool_use", fmt.Sprintf(
		`echo "$HOOK_TOOL_NAME|$HOOK_TOOL_ERROR" >> %s`, sentinelFile,
	))

	hr := NewHookRunner(dir)
	hr.RunPostToolUse("bash", `{"command":"ls"}`, "file.txt\n", nil)

	data, err := os.ReadFile(sentinelFile)
	if err != nil {
		t.Fatalf("sentinel file not created: %v", err)
	}
	line := strings.TrimSpace(string(data))
	// HOOK_TOOL_ERROR should be empty on success.
	if line != "bash|" {
		t.Errorf("unexpected output: %q", line)
	}
}

// TestHookRunner_PostToolUse_Error verifies that HOOK_TOOL_ERROR is set when
// the tool returned an error.
func TestHookRunner_PostToolUse_Error(t *testing.T) {
	dir := t.TempDir()
	sentinelFile := filepath.Join(dir, "post_tool_use.out")

	writeHookScript(t, dir, "post_tool_use", fmt.Sprintf(
		`echo "$HOOK_TOOL_ERROR" >> %s`, sentinelFile,
	))

	hr := NewHookRunner(dir)
	hr.RunPostToolUse("bash", `{"command":"false"}`, "", errors.New("exit status 1"))

	data, err := os.ReadFile(sentinelFile)
	if err != nil {
		t.Fatalf("sentinel file not created: %v", err)
	}
	line := strings.TrimSpace(string(data))
	if line != "exit status 1" {
		t.Errorf("unexpected output: %q", line)
	}
}

// TestHookRunner_SessionEnd verifies that the session_end hook receives
// HOOK_TOOLS_USED as a comma-separated list.
func TestHookRunner_SessionEnd(t *testing.T) {
	dir := t.TempDir()
	sentinelFile := filepath.Join(dir, "session_end.out")

	writeHookScript(t, dir, "session_end", fmt.Sprintf(
		`echo "$HOOK_EVENT|$HOOK_TOOLS_USED" >> %s`, sentinelFile,
	))

	hr := NewHookRunner(dir)
	hr.RunSessionEnd("all done", []string{"web_search", "bash"})

	data, err := os.ReadFile(sentinelFile)
	if err != nil {
		t.Fatalf("sentinel file not created: %v", err)
	}
	line := strings.TrimSpace(string(data))
	if line != "session_end|web_search,bash" {
		t.Errorf("unexpected output: %q", line)
	}
}

// TestHookRunner_MissingScript verifies that a missing script for an event
// does not cause an error or panic.
func TestHookRunner_MissingScript(t *testing.T) {
	dir := t.TempDir()
	// No scripts written — all runs should be no-ops.
	hr := NewHookRunner(dir)
	hr.RunSessionStart("msg")
	hr.RunPreToolUse("tool", "{}")
	hr.RunPostToolUse("tool", "{}", "out", nil)
	hr.RunSessionEnd("resp", nil)
}

// TestHookRunner_NonZeroExitDoesNotPanic verifies that a hook script that exits
// non-zero is handled gracefully.
func TestHookRunner_NonZeroExitDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	writeHookScript(t, dir, "session_start", "exit 1")

	hr := NewHookRunner(dir)
	// Should not panic even though the script exits non-zero.
	hr.RunSessionStart("test")
}

// TestTruncate verifies the truncate helper.
func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
	if got := truncate("hello world", 5); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
	if got := truncate("", 5); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
