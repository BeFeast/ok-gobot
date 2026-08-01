package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunPreflightPass(t *testing.T) {
	repo := newPreflightRepo(t)
	opts := passingPreflightOptions(repo)

	report := RunPreflight(context.Background(), opts)
	if !report.OK {
		t.Fatalf("preflight failed: %s", report.Summary())
	}
	if !containsString(report.TestCommands, "go test ./...") {
		t.Fatalf("test commands = %v, want go test ./...", report.TestCommands)
	}
	if !containsString(report.TestCommands, "go build ./cmd/ok-gobot/") {
		t.Fatalf("test commands = %v, want go build ./cmd/ok-gobot/", report.TestCommands)
	}
	if !hasCheck(report, "github auth", PreflightPassed) {
		t.Fatalf("expected github auth to pass: %+v", report.Checks)
	}
}

func TestDiscoverTestCommandsRequiresConfiguredBuildTargets(t *testing.T) {
	repo := newPreflightRepo(t)

	commands := DiscoverTestCommands(repo)
	if containsString(commands, "go build ./cmd/ok-gobot/") {
		t.Fatalf("unexpected ok-gobot build command without configured target: %v", commands)
	}

	commands = DiscoverTestCommands(repo, "./cmd/ok-gobot/")
	if !containsString(commands, "go build ./cmd/ok-gobot/") {
		t.Fatalf("test commands = %v, want configured go build target", commands)
	}
}

func TestRunPreflightMissingTool(t *testing.T) {
	repo := newPreflightRepo(t)
	opts := passingPreflightOptions(repo)
	opts.LookPath = func(name string) (string, error) {
		if name == "go" {
			return "", errors.New("missing go")
		}
		return "/usr/bin/" + filepath.Base(name), nil
	}

	report := RunPreflight(context.Background(), opts)
	if report.OK {
		t.Fatalf("preflight unexpectedly passed")
	}
	if !hasCheck(report, "tool: go", PreflightFailed) {
		t.Fatalf("expected missing go failure: %+v", report.Checks)
	}
	check, ok := findCheckByID(report, "tool.go")
	if !ok {
		t.Fatalf("expected stable tool.go check id: %+v", report.Checks)
	}
	if check.Reason != "go is not available in PATH" || !strings.Contains(check.Remediation, "Install go") {
		t.Fatalf("unexpected missing tool check: %+v", check)
	}
	summary := report.Summary()
	for _, want := range []string{"preflight failed: [tool.go] go is not available in PATH", "Hint: Install go"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q: %s", want, summary)
		}
	}
	if strings.Count(summary, "preflight failed") != 1 {
		t.Fatalf("summary repeated preflight prefix: %s", summary)
	}
}

func TestRunPreflightMissingAuth(t *testing.T) {
	repo := newPreflightRepo(t)
	secret := "ghp_abcdefghijklmnopqrstuvwxyz123456"
	opts := passingPreflightOptions(repo)
	opts.CommandRunner = func(_ context.Context, _ string, name string, args ...string) CommandResult {
		if name == "gh" {
			return CommandResult{Stderr: "not logged in token=" + secret, Err: errors.New("exit status 1")}
		}
		return passingCommandResult(repo, name, args...)
	}

	report := RunPreflight(context.Background(), opts)
	if report.OK {
		t.Fatalf("preflight unexpectedly passed")
	}
	if !hasCheck(report, "github auth", PreflightFailed) {
		t.Fatalf("expected github auth failure: %+v", report.Checks)
	}
	check, ok := findCheckByID(report, "github.auth")
	if !ok {
		t.Fatalf("expected stable github.auth check id: %+v", report.Checks)
	}
	if check.Reason != "GitHub authentication is missing or invalid" || !strings.Contains(check.Remediation, "gh auth login") {
		t.Fatalf("unexpected auth check: %+v", check)
	}
	if strings.Contains(report.Summary(), secret) || strings.Contains(report.EvidenceJSON(), secret) {
		t.Fatalf("preflight output leaked secret: %s", report.EvidenceJSON())
	}
	for _, want := range []string{"[github.auth] GitHub authentication is missing or invalid", "Hint: Run gh auth login", `"id": "github.auth"`, `"reason": "GitHub authentication is missing or invalid"`} {
		combined := report.Summary() + "\n" + report.EvidenceJSON()
		if !strings.Contains(combined, want) {
			t.Fatalf("preflight output missing %q:\n%s", want, combined)
		}
	}
}

func TestRunPreflightNonWritableWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod write-bit test is POSIX-specific")
	}
	repo := newPreflightRepo(t)
	if err := os.Chmod(repo, 0o555); err != nil {
		t.Fatalf("chmod repo read-only: %v", err)
	}
	defer os.Chmod(repo, 0o755) //nolint:errcheck

	report := RunPreflight(context.Background(), passingPreflightOptions(repo))
	if report.OK {
		t.Fatalf("preflight unexpectedly passed")
	}
	if !hasCheck(report, "worktree path writable", PreflightFailed) {
		t.Fatalf("expected worktree writable failure: %+v", report.Checks)
	}
	check, ok := findCheckByID(report, "path.worktree.writable")
	if !ok {
		t.Fatalf("expected stable path.worktree.writable check id: %+v", report.Checks)
	}
	if check.Reason != "worktree path is not writable" || !strings.Contains(check.Remediation, "writable") {
		t.Fatalf("unexpected writable path check: %+v", check)
	}
}

func TestRunPreflightNetworkFailureHasRemediation(t *testing.T) {
	repo := newPreflightRepo(t)
	opts := passingPreflightOptions(repo)
	opts.NetworkAllowlist = []string{"github.com"}

	report := RunPreflight(context.Background(), opts)
	if report.OK {
		t.Fatalf("preflight unexpectedly passed")
	}
	check, ok := findCheckByID(report, "network.allowlist")
	if !ok {
		t.Fatalf("expected network.allowlist failure: %+v", report.Checks)
	}
	if check.Status != PreflightFailed || check.Reason != "required network target is not allowed by the network allowlist" || !strings.Contains(check.Remediation, "network allowlist") {
		t.Fatalf("unexpected network check: %+v", check)
	}
}

func TestRunPreflightMissingTestCommandHasSkippedRemediation(t *testing.T) {
	repo := t.TempDir()
	opts := WorkerPreflightOptions("claude", "claude", "claude-sonnet-test", repo, nil)
	opts.SourceDir = repo
	opts.RequireGitHubAuth = false
	opts.LookPath = func(name string) (string, error) {
		return "/usr/bin/" + filepath.Base(name), nil
	}
	opts.CommandRunner = func(_ context.Context, _ string, name string, args ...string) CommandResult {
		return passingCommandResult(repo, name, args...)
	}

	report := RunPreflight(context.Background(), opts)
	if !report.OK {
		t.Fatalf("preflight failed: %s", report.Summary())
	}
	check, ok := findCheckByID(report, "repo.test_command")
	if !ok {
		t.Fatalf("expected repo.test_command check: %+v", report.Checks)
	}
	if check.Status != PreflightSkipped || check.Reason != "no repo-specific test command was detected" || !strings.Contains(check.Remediation, "go.mod") {
		t.Fatalf("unexpected test command check: %+v", check)
	}
}

func TestRedactSecrets(t *testing.T) {
	secret := "github_pat_abcdefghijklmnopqrstuvwxyz1234567890"
	input := "Authorization: Bearer sk-abcdefghijklmnopqrstuvwxyz123456 token=" + secret
	redacted := RedactSecrets(input)
	if strings.Contains(redacted, secret) || strings.Contains(redacted, "sk-abcdefghijklmnopqrstuvwxyz123456") {
		t.Fatalf("redaction leaked secret: %q", redacted)
	}
	if !strings.Contains(redacted, "[REDACTED]") {
		t.Fatalf("redaction should include marker, got %q", redacted)
	}
}

func TestCleanupPreflightTempReportsRemoveFailure(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing")
	detail := cleanupPreflightTemp(missingPath)
	if detail == "" {
		t.Fatalf("expected cleanup failure detail")
	}
	if strings.Contains(detail, filepath.Dir(missingPath)) {
		t.Fatalf("cleanup detail exposed local path: %q", detail)
	}
}

func newPreflightRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/preflight\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "cmd", "ok-gobot"), 0o755); err != nil {
		t.Fatalf("mkdir cmd/ok-gobot: %v", err)
	}
	return repo
}

func passingPreflightOptions(repo string) PreflightOptions {
	opts := WorkerPreflightOptions("claude", "claude", "claude-sonnet-test", repo, []string{"api.anthropic.com"}, "./cmd/ok-gobot/")
	opts.SourceDir = repo
	opts.NetworkAllowlist = []string{"github.com", "api.github.com", "api.anthropic.com"}
	opts.LookPath = func(name string) (string, error) {
		return "/usr/bin/" + filepath.Base(name), nil
	}
	opts.CommandRunner = func(_ context.Context, _ string, name string, args ...string) CommandResult {
		return passingCommandResult(repo, name, args...)
	}
	return opts
}

func passingCommandResult(repo, name string, args ...string) CommandResult {
	switch name {
	case "gh":
		return CommandResult{Stdout: "Logged in to github.com\nToken scopes: 'repo', 'read:org'\n"}
	case "git":
		if len(args) > 0 && args[len(args)-1] == "--show-toplevel" {
			return CommandResult{Stdout: repo + "\n"}
		}
		return CommandResult{}
	default:
		return CommandResult{}
	}
}

func hasCheck(report PreflightReport, name string, status PreflightStatus) bool {
	for _, check := range report.Checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}

func findCheckByID(report PreflightReport, id string) (PreflightCheck, bool) {
	for _, check := range report.Checks {
		if check.ID == id {
			return check, true
		}
	}
	return PreflightCheck{}, false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
