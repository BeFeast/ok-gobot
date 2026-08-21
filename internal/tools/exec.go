package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// ExecTool runs host shell commands in yolo mode: every command runs immediately,
// no per-command approval. The safety model is not "ask first" but "revertable by
// design + kill switch":
//
//   - estop hard-blocks the whole exec family (the operator's panic button).
//   - Before a state-changing command, existing files it names are backed up to a
//     timestamped directory, so config edits and overwrites can be rolled back.
//   - Every command is logged (redacted) and mirrored to job_events for traceability.
//
// Per SOUL.md the main agent never executes: exec is registered only for
// sub-agents (the resolver drops it from the main-agent registry and gives the
// main agent the host_task spawner instead). The tool stays ChatScoped so the
// resolver can bind the owning chatID for audit attribution.
//
// ExecTool manages its own deadline (OwnsTimeout) so the generic per-tool timeout
// in the agent loop does not spawn it as a subagent mid-command.
type ExecTool struct {
	// WorkDir is the default working directory for commands (usually the home dir).
	WorkDir string
	// ReadOnlyPrefixes are command prefixes treated as non-mutating: they run
	// without taking a pre-command backup. Matching is word-boundary aware.
	ReadOnlyPrefixes []string
	// DefaultTimeout is applied when the caller does not specify one.
	DefaultTimeout time.Duration
	// MaxTimeout caps any caller-supplied timeout.
	MaxTimeout time.Duration
	// MaxOutputBytes caps the combined output returned to the model.
	MaxOutputBytes int
	// BackupDir is where pre-command file backups are written. Empty disables
	// backups (only read-only commands are safe to run then).
	BackupDir string
	// MaxBackupFileBytes skips backing up any single file larger than this, so a
	// command that merely reads a huge file does not trigger a huge copy.
	MaxBackupFileBytes int64
	// AuditSink, when set, receives a structured record of every command. Best-effort.
	AuditSink ExecAuditSink
	// boundChatID identifies the chat this copy serves; set by BindChat at resolve
	// time. Used only for audit attribution (job_events).
	boundChatID int64
}

// ExecAuditSink records exec invocations for durable audit (e.g. job_events).
type ExecAuditSink interface {
	RecordExec(chatID int64, command string, exitCode int, dur time.Duration, backup string)
}

// execReadOnlyPrefixes are commands that only inspect state, so no pre-command
// backup is taken for them. This is a backup optimization, NOT a security gate —
// everything runs regardless of whether it is on this list.
var execReadOnlyPrefixes = []string{
	"ls", "cat", "head", "tail", "wc", "stat", "file", "find", "tree", "readlink", "realpath",
	"grep", "rg", "fd",
	"pwd", "whoami", "hostname", "hostnamectl", "uname", "id", "uptime", "date",
	"env", "printenv", "df", "du", "free", "lsblk", "lscpu", "ps", "which", "type", "echo",
	"ip a", "ip addr", "ss",
	"systemctl status", "systemctl is-active", "systemctl is-enabled",
	"systemctl list-units", "systemctl list-unit-files", "systemctl show", "systemctl --version",
	"journalctl",
	"git status", "git log", "git diff", "git show", "git branch", "git remote -v",
	"git rev-parse", "git config --get", "git describe",
	"go version", "node --version", "npm --version", "python3 --version", "docker ps",
	"curl -s http://localhost", "curl -s http://127.0.0.1",
	"curl -sS http://localhost", "curl -sS http://127.0.0.1",
}

// NewExecTool builds a yolo ExecTool with conservative defaults.
func NewExecTool(workDir string) *ExecTool {
	if workDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			workDir = home
		}
	}
	backupDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		backupDir = filepath.Join(home, ".ok-gobot", "exec-backups")
	}
	return &ExecTool{
		WorkDir:            workDir,
		ReadOnlyPrefixes:   append([]string(nil), execReadOnlyPrefixes...),
		DefaultTimeout:     60 * time.Second,
		MaxTimeout:         10 * time.Minute,
		MaxOutputBytes:     8000,
		BackupDir:          backupDir,
		MaxBackupFileBytes: 50 << 20, // 50 MiB
	}
}

func (t *ExecTool) Name() string { return "exec" }

func (t *ExecTool) Description() string {
	return "Execute a shell command on the host. Commands run immediately (yolo) — there is no " +
		"approval prompt. Before a state-changing command, existing files it names are backed up " +
		"so the change can be reverted, and every command is logged. Use this to perform host " +
		"operations yourself instead of writing a runbook. Available only inside a host_task worker."
}

// OwnsTimeout exempts exec from the agent loop's generic per-tool timeout: it
// enforces its own deadline (default 60s, cap 10m) around the command.
func (t *ExecTool) OwnsTimeout() bool { return true }

// BindChat implements ChatScoped: the resolver binds the owning chatID so audit
// records attribute to the right run. The MediaSender is unused.
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

// isReadOnly reports whether the command only inspects state (word-boundary match
// against ReadOnlyPrefixes and free of shell chaining/redirection). It gates
// whether a pre-command backup is taken, not whether the command runs.
func (t *ExecTool) isReadOnly(command string) bool {
	c := strings.TrimSpace(command)
	if c == "" {
		return true
	}
	for _, m := range []string{";", "&", "|", "`", "$(", "${", ">", "<", "\n", "\\"} {
		if strings.Contains(c, m) {
			return false
		}
	}
	for _, prefix := range t.ReadOnlyPrefixes {
		if prefixWithBoundary(c, prefix) {
			return true
		}
	}
	return false
}

// prefixWithBoundary reports whether c begins with prefix followed by a token
// boundary (space, tab, '/', ':' or end of string).
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

	workDir := t.WorkDir
	if strings.TrimSpace(cwd) != "" {
		if filepath.IsAbs(cwd) {
			workDir = filepath.Clean(cwd)
		} else {
			workDir = filepath.Join(t.WorkDir, cwd)
		}
	}

	// Revertability: back up existing files the command names before running it.
	// Read-only commands are skipped as an optimization only.
	backupNote := "readonly"
	if !t.isReadOnly(command) {
		backupNote = t.backup(command, workDir)
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

	t.audit(command, rc, dur, backupNote)

	output := t.truncate(string(outBytes))

	var b strings.Builder
	if timedOut {
		fmt.Fprintf(&b, "⏱️ command timed out after %s (killed)\n", formatDuration(timeout))
	}
	b.WriteString(output)
	if output != "" && !strings.HasSuffix(output, "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "[exec] exit=%d duration=%s backup=%s", rc, formatDuration(dur), backupNote)
	return b.String(), nil
}

// backup copies existing files named in the command to a timestamped directory
// under BackupDir, mirroring their absolute paths so a restore is a plain copy.
// It over-backs-up (a file that is only read is still copied) — harmless — but
// never claims safety it did not deliver: directories and oversized files are
// skipped and reported. Returns a short human-readable note for the audit line.
func (t *ExecTool) backup(command, workDir string) string {
	if strings.TrimSpace(t.BackupDir) == "" {
		return "disabled"
	}
	paths := candidateBackupPaths(command, workDir, t.WorkDir)
	if len(paths) == 0 {
		return "none"
	}

	stamp := time.Now().Format("20060102T150405.000")
	dest := filepath.Join(t.BackupDir, stamp)

	saved := 0
	skipped := 0
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			// New file, directory, device, or unreadable — nothing to snapshot.
			if err == nil {
				skipped++
			}
			continue
		}
		if t.MaxBackupFileBytes > 0 && info.Size() > t.MaxBackupFileBytes {
			skipped++
			continue
		}
		target := filepath.Join(dest, p)
		if err := copyFilePreserving(p, target); err != nil {
			log.Printf("[exec] backup: failed to copy %s: %v", redact.Redact(p), err)
			skipped++
			continue
		}
		saved++
	}

	if saved == 0 && skipped == 0 {
		return "none"
	}
	note := fmt.Sprintf("%d file(s)->%s", saved, dest)
	if skipped > 0 {
		note += fmt.Sprintf(" (%d skipped: dir/oversized)", skipped)
	}
	log.Printf("[exec] backup: %s", note)
	return note
}

// candidateBackupPaths extracts tokens from the command that resolve to existing
// paths. It strips quotes, common redirection/assignment prefixes, expands ~, and
// resolves relatives against the command's working dir.
func candidateBackupPaths(command, workDir, homeBase string) []string {
	home, _ := os.UserHomeDir()
	seen := map[string]struct{}{}
	var out []string

	fields := strings.FieldsFunc(command, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '=' || r == '"' || r == '\''
	})
	for _, tok := range fields {
		tok = strings.TrimLeft(tok, "<>|&;")
		tok = strings.Trim(tok, "\"'")
		if tok == "" || strings.HasPrefix(tok, "-") {
			continue
		}
		if tok == "~" {
			tok = home
		} else if strings.HasPrefix(tok, "~/") && home != "" {
			tok = filepath.Join(home, tok[2:])
		}

		var abs string
		if filepath.IsAbs(tok) {
			abs = filepath.Clean(tok)
		} else {
			base := workDir
			if base == "" {
				base = homeBase
			}
			if base == "" {
				continue
			}
			abs = filepath.Join(base, tok)
		}
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		if _, dup := seen[abs]; dup {
			continue
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	return out
}

func copyFilePreserving(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// audit emits the durable structured log line and forwards to the sink. The
// command is redacted so secrets passed inline never reach the journal or DB.
func (t *ExecTool) audit(command string, rc int, dur time.Duration, backup string) {
	safe := redact.Redact(redact.Assignments(command))
	log.Printf("[exec] cmd=%q rc=%d dur=%dms backup=%s", safe, rc, dur.Milliseconds(), backup)
	if t.AuditSink != nil {
		t.AuditSink.RecordExec(t.boundChatID, safe, rc, dur, backup)
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
