package tools

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"ok-gobot/internal/redact"
)

// ExecTool runs host shell commands with a deny-by-default policy. Commands that
// match the read-only allowlist run immediately; everything else is routed to an
// operator approval gate (Telegram inline buttons via the bot's ApprovalFunc).
//
// Per SOUL.md the main agent must never execute: exec is registered only for
// sub-agents (the resolver drops it from the main-agent registry and gives the
// main agent the host_task spawner instead). Because the approval prompt must
// reach the operator's chat, the tool is ChatScoped — the resolver binds the
// chatID into each sub-agent's copy so ApprovalFunc knows where to ask.
//
// ExecTool manages its own deadline (OwnsTimeout) so the generic per-tool timeout
// in the agent loop does not spawn it as a subagent mid-command.
type ExecTool struct {
	// WorkDir is the default working directory for commands (usually the home dir).
	WorkDir string
	// Allowlist holds command prefixes that run without approval. Matching is
	// word-boundary aware and rejects any command containing shell chaining or
	// redirection metacharacters (see isAllowlisted).
	Allowlist []string
	// DefaultTimeout is applied when the caller does not specify one.
	DefaultTimeout time.Duration
	// MaxTimeout caps any caller-supplied timeout.
	MaxTimeout time.Duration
	// MaxOutputBytes caps the combined output returned to the model.
	MaxOutputBytes int
	// ApprovalFunc gates non-allowlisted commands. It receives the chat that owns
	// the run and the command, and returns whether the command was approved plus a
	// short label for who approved it (for the audit trail). When nil, or when the
	// chat is unknown, non-allowlisted commands fail closed.
	ApprovalFunc func(chatID int64, command string) (approved bool, approvedBy string, err error)
	// AuditSink, when set, receives a structured record of every executed or
	// denied command. Best-effort: implementations must never block or panic.
	AuditSink ExecAuditSink
	// boundChatID identifies the chat this copy serves; set by BindChat at resolve
	// time. Zero means unbound (no operator to approve → deny-by-default).
	boundChatID int64
}

// ExecAuditSink records exec invocations for durable audit (e.g. job_events).
type ExecAuditSink interface {
	RecordExec(chatID int64, command, approvedBy string, exitCode int, dur time.Duration, denied bool)
}

// execChainMetachars are shell control operators that let one command spawn or
// redirect into another. Their presence disqualifies a command from the
// allowlist fast path (it must go through approval), which closes the classic
// "ls; rm -rf /" prefix-match bypass.
var execChainMetachars = []string{";", "&", "|", "`", "$(", "${", ">", "<", "\n", "\\"}

// defaultExecAllowlist is a conservative set of read-only command prefixes.
// Anything not listed here — or listed but carrying chaining/redirection — is
// denied unless the operator approves it interactively.
var defaultExecAllowlist = []string{
	// Filesystem inspection
	"ls", "cat", "head", "tail", "wc", "stat", "file", "find", "tree", "readlink", "realpath",
	// Search
	"grep", "rg", "fd",
	// Host identity / resources
	"pwd", "whoami", "hostname", "hostnamectl", "uname", "id", "uptime", "date",
	"env", "printenv", "df", "du", "free", "lsblk", "lscpu", "ps", "which", "type", "echo",
	"ip a", "ip addr", "ss",
	// Service inspection (read-only subcommands only)
	"systemctl status", "systemctl is-active", "systemctl is-enabled",
	"systemctl list-units", "systemctl list-unit-files", "systemctl show", "systemctl --version",
	"journalctl",
	// Git inspection
	"git status", "git log", "git diff", "git show", "git branch", "git remote -v",
	"git rev-parse", "git config --get", "git describe",
	// Toolchain versions
	"go version", "node --version", "npm --version", "python3 --version", "docker ps",
	// Loopback health checks
	"curl -s http://localhost", "curl -s http://127.0.0.1",
	"curl -sS http://localhost", "curl -sS http://127.0.0.1",
}

// NewExecTool builds an ExecTool with conservative defaults.
func NewExecTool(workDir string) *ExecTool {
	if workDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			workDir = home
		}
	}
	return &ExecTool{
		WorkDir:        workDir,
		Allowlist:      append([]string(nil), defaultExecAllowlist...),
		DefaultTimeout: 60 * time.Second,
		MaxTimeout:     10 * time.Minute,
		MaxOutputBytes: 8000,
	}
}

func (t *ExecTool) Name() string { return "exec" }

func (t *ExecTool) Description() string {
	return "Execute a shell command on the host. Read-only commands (ls, cat, systemctl status, " +
		"journalctl, git status, curl localhost, …) run immediately; anything else asks Oleg for " +
		"approval in Telegram before running. Use this to perform host operations yourself instead " +
		"of writing a runbook. Available only inside a host_task worker, not the main session."
}

// OwnsTimeout exempts exec from the agent loop's generic per-tool timeout: it
// enforces its own deadline (default 60s, cap 10m) around the command.
func (t *ExecTool) OwnsTimeout() bool { return true }

// BindChat implements ChatScoped: the resolver calls it so each sub-agent's exec
// copy knows which chat to ask for approval. The MediaSender is unused (exec
// prompts via ApprovalFunc, not media delivery).
func (t *ExecTool) BindChat(_ MediaSender, chatID int64) Tool {
	bound := *t
	bound.boundChatID = chatID
	return &bound
}

// GetSchema returns the JSON Schema for exec parameters.
func (t *ExecTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The shell command to run (executed with `bash -c`).",
			},
			"cwd": map[string]interface{}{
				"type":        "string",
				"description": "Working directory (optional; defaults to the home directory).",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Timeout in seconds (optional; default 60, maximum 600).",
			},
		},
		"required": []string{"command"},
	}
}

// Execute implements the positional Tool interface. All args are joined into the
// command; cwd/timeout use defaults. Prefer ExecuteJSON for full control.
func (t *ExecTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("no command specified")
	}
	return t.run(ctx, strings.Join(args, " "), "", 0)
}

// ExecuteJSON implements the structured-params path (named command/cwd/timeout,
// with tolerant aliases).
func (t *ExecTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	command := firstNonEmptyParam(params, "command", "cmd", "input")
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("no command specified")
	}
	cwd := firstNonEmptyParam(params, "cwd", "dir", "directory")

	var timeout time.Duration
	if raw := firstNonEmptyParam(params, "timeout", "timeout_seconds", "timeout_s"); raw != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}
	return t.run(ctx, command, cwd, timeout)
}

func firstNonEmptyParam(params map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := params[k]; ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// isAllowlisted reports whether the command may run without approval. It fails
// closed on any shell chaining/redirection metacharacter and requires a
// word-boundary match against an allowlist prefix.
func (t *ExecTool) isAllowlisted(command string) bool {
	c := strings.TrimSpace(command)
	if c == "" {
		return false
	}
	for _, m := range execChainMetachars {
		if strings.Contains(c, m) {
			return false
		}
	}
	for _, prefix := range t.Allowlist {
		if prefixWithBoundary(c, prefix) {
			return true
		}
	}
	return false
}

// prefixWithBoundary reports whether c begins with prefix followed by a token
// boundary (space, tab, '/', ':' or end of string). The boundary check prevents
// "cat" from matching "catastrophe" while still allowing "curl -s http://localhost:8080/health".
func prefixWithBoundary(c, prefix string) bool {
	if c == prefix {
		return true
	}
	if !strings.HasPrefix(c, prefix) {
		return false
	}
	b := c[len(prefix)]
	return b == ' ' || b == '\t' || b == '/' || b == ':'
}

func (t *ExecTool) run(parent context.Context, command, cwd string, timeout time.Duration) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("no command specified")
	}

	if timeout <= 0 {
		timeout = t.DefaultTimeout
	}
	if t.MaxTimeout > 0 && timeout > t.MaxTimeout {
		timeout = t.MaxTimeout
	}

	approvedBy := "allowlist"
	if !t.isAllowlisted(command) {
		if t.ApprovalFunc == nil {
			// Deny-by-default: without a wired approval gate, nothing outside the
			// allowlist may run.
			return "", fmt.Errorf("exec approval is not configured; command not on allowlist (deny-by-default)")
		}
		approved, by, err := t.ApprovalFunc(t.boundChatID, command)
		if err != nil {
			return "", err
		}
		if !approved {
			t.audit(command, "operator", -1, 0, true)
			return "DENIED: command was not approved by the operator.", nil
		}
		approvedBy = strings.TrimSpace(by)
		if approvedBy == "" {
			approvedBy = "operator"
		}
	}

	workDir := t.WorkDir
	if strings.TrimSpace(cwd) != "" {
		if filepath.IsAbs(cwd) {
			workDir = filepath.Clean(cwd)
		} else {
			workDir = filepath.Join(t.WorkDir, cwd)
		}
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}
	outBytes, runErr := cmd.CombinedOutput()
	dur := time.Since(start)

	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	rc := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			rc = exitErr.ExitCode()
		} else {
			rc = -1
		}
	}

	t.audit(command, approvedBy, rc, dur, false)

	output := t.truncate(string(outBytes))

	var b strings.Builder
	if timedOut {
		fmt.Fprintf(&b, "⏱️ command timed out after %s (killed)\n", formatDuration(timeout))
	}
	b.WriteString(output)
	if output != "" && !strings.HasSuffix(output, "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "[exec] exit=%d duration=%s", rc, formatDuration(dur))
	return b.String(), nil
}

// audit emits the durable structured log line and forwards to the sink. The
// command is redacted so secrets passed inline never reach the journal or DB.
func (t *ExecTool) audit(command, approvedBy string, rc int, dur time.Duration, denied bool) {
	safe := redact.Redact(redact.Assignments(command))
	if denied {
		log.Printf("[exec] cmd=%q approved_by=%s denied=true", safe, approvedBy)
	} else {
		log.Printf("[exec] cmd=%q approved_by=%s rc=%d dur=%dms", safe, approvedBy, rc, dur.Milliseconds())
	}
	if t.AuditSink != nil {
		t.AuditSink.RecordExec(t.boundChatID, safe, approvedBy, rc, dur, denied)
	}
}

func (t *ExecTool) truncate(s string) string {
	max := t.MaxOutputBytes
	if max <= 0 {
		max = 8000
	}
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n… [output truncated at %d bytes]", max)
}
