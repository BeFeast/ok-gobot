package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// PreflightStatus is the outcome for one preflight check.
type PreflightStatus string

const (
	PreflightPassed  PreflightStatus = "passed"
	PreflightFailed  PreflightStatus = "failed"
	PreflightSkipped PreflightStatus = "skipped"
)

// PreflightCheck is one redacted readiness check result.
type PreflightCheck struct {
	Name        string          `json:"name"`
	Status      PreflightStatus `json:"status"`
	Detail      string          `json:"detail,omitempty"`
	Remediation string          `json:"remediation,omitempty"`
}

// PreflightReport is the session evidence emitted before a worker starts.
type PreflightReport struct {
	OK           bool             `json:"ok"`
	Backend      string           `json:"backend,omitempty"`
	Model        string           `json:"model,omitempty"`
	SourceDir    string           `json:"source_dir,omitempty"`
	WorkDir      string           `json:"work_dir,omitempty"`
	TestCommands []string         `json:"test_commands,omitempty"`
	Checks       []PreflightCheck `json:"checks"`
	StartedAt    time.Time        `json:"started_at"`
	CompletedAt  time.Time        `json:"completed_at"`
}

// FailureReasons returns concise, redacted failure details for status events.
func (r PreflightReport) FailureReasons() []string {
	reasons := make([]string, 0)
	for _, check := range r.Checks {
		if check.Status != PreflightFailed {
			continue
		}
		reason := check.Name
		if check.Detail != "" {
			reason += ": " + check.Detail
		}
		reasons = append(reasons, RedactSecrets(reason))
	}
	return reasons
}

// Summary returns a short redacted summary suitable for job errors.
func (r PreflightReport) Summary() string {
	if r.OK {
		return "preflight passed"
	}
	reasons := r.FailureReasons()
	if len(reasons) == 0 {
		return "preflight failed"
	}
	if len(reasons) > 3 {
		reasons = append(reasons[:3], fmt.Sprintf("%d more failure(s)", len(reasons)-3))
	}
	return "preflight failed: " + strings.Join(reasons, "; ")
}

// EvidenceJSON returns the redacted report as formatted JSON.
func (r PreflightReport) EvidenceJSON() string {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"error":%q}`, RedactSecrets(err.Error()))
	}
	return string(b)
}

// CommandResult captures stdout/stderr without exposing command details directly.
type CommandResult struct {
	Stdout string
	Stderr string
	Err    error
}

// CommandRunner runs a command for a preflight probe.
type CommandRunner func(ctx context.Context, dir, name string, args ...string) CommandResult

// PreflightOptions configures worker readiness checks.
type PreflightOptions struct {
	Backend              string
	Model                string
	BinaryPath           string
	SourceDir            string
	WorkDir              string
	RequiredTools        []string
	RequireGitHubAuth    bool
	RequiredGitHubScopes []string
	RequiredNetworkHosts []string
	NetworkDisabled      bool
	NetworkAllowlist     []string
	RequireBrowser       bool
	CommandRunner        CommandRunner
	LookPath             func(string) (string, error)
	Now                  func() time.Time
}

// PreflightPlanner is implemented by adapters that know their backend checks.
type PreflightPlanner interface {
	PreflightOptions(req Request) PreflightOptions
}

// WorkerPreflightOptions returns the mandatory baseline checks for a worker CLI.
func WorkerPreflightOptions(backend, binaryPath, model, workDir string, networkHosts []string) PreflightOptions {
	return PreflightOptions{
		Backend:              backend,
		Model:                model,
		BinaryPath:           binaryPath,
		WorkDir:              workDir,
		RequiredTools:        []string{"git", "sh"},
		RequireGitHubAuth:    true,
		RequiredGitHubScopes: []string{"repo"},
		RequiredNetworkHosts: append([]string{"github.com", "api.github.com"}, networkHosts...),
	}
}

// RunAdapterPreflight runs preflight for adapters that declare requirements.
func RunAdapterPreflight(ctx context.Context, adapter Adapter, req Request) (PreflightReport, bool) {
	planner, ok := adapter.(PreflightPlanner)
	if !ok {
		return PreflightReport{}, false
	}
	return RunPreflight(ctx, planner.PreflightOptions(req)), true
}

// RunPreflight checks worker readiness without invoking the worker backend.
func RunPreflight(ctx context.Context, opts PreflightOptions) PreflightReport {
	if ctx == nil {
		ctx = context.Background()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	runner := opts.CommandRunner
	if runner == nil {
		runner = defaultCommandRunner
	}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	started := now()
	workDir := strings.TrimSpace(opts.WorkDir)
	if workDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workDir = wd
		}
	}
	sourceDir := strings.TrimSpace(opts.SourceDir)
	if sourceDir == "" {
		sourceDir = workDir
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = "backend default"
	}

	report := PreflightReport{
		Backend:   strings.TrimSpace(opts.Backend),
		Model:     RedactSecrets(model),
		SourceDir: RedactSecrets(sourceDir),
		WorkDir:   RedactSecrets(workDir),
		StartedAt: started,
	}

	add := func(name string, status PreflightStatus, detail, remediation string) {
		report.Checks = append(report.Checks, PreflightCheck{
			Name:        name,
			Status:      status,
			Detail:      RedactSecrets(detail),
			Remediation: RedactSecrets(remediation),
		})
	}

	tools := requiredTools(opts, sourceDir)
	missingTools := map[string]bool{}
	for _, tool := range tools {
		if _, err := lookPath(tool); err != nil {
			missingTools[tool] = true
			add("tool: "+tool, PreflightFailed, "not found in PATH", fmt.Sprintf("Install %s or configure its binary path before starting the worker.", tool))
			continue
		}
		add("tool: "+tool, PreflightPassed, "available", "")
	}

	if opts.RequireBrowser {
		if browser := firstAvailable(lookPath, []string{"google-chrome", "chromium", "chromium-browser", "chrome"}); browser == "" {
			add("browser tooling", PreflightFailed, "no Chrome/Chromium binary found", "Install Chrome/Chromium or configure browser.chrome_path before running browser tasks.")
		} else {
			add("browser tooling", PreflightPassed, browser+" available", "")
		}
	}

	checkGitHubAuth(ctx, opts, runner, workDir, missingTools, add)
	checkWritablePath("source path writable", sourceDir, add)
	checkWritablePath("worktree path writable", workDir, add)
	checkGitTrust(ctx, runner, workDir, missingTools, add)
	checkNetwork(opts, add)

	testCommands := DiscoverTestCommands(sourceDir)
	report.TestCommands = testCommands
	if len(testCommands) == 0 {
		add("repo test command discovery", PreflightSkipped, "no repo-specific test command detected", "")
	} else {
		add("repo test command discovery", PreflightPassed, strings.Join(testCommands, "; "), "")
	}

	report.CompletedAt = now()
	report.OK = true
	for _, check := range report.Checks {
		if check.Status == PreflightFailed {
			report.OK = false
			break
		}
	}
	return report
}

// DiscoverTestCommands returns deterministic repo-specific verification commands.
func DiscoverTestCommands(repoDir string) []string {
	var commands []string
	if fileExists(filepath.Join(repoDir, "go.mod")) {
		commands = append(commands, "go test ./...", "go vet ./...")
		if dirExists(filepath.Join(repoDir, "cmd", "ok-gobot")) {
			commands = append(commands, "go build ./cmd/ok-gobot/")
		}
	}
	if fileExists(filepath.Join(repoDir, "package.json")) {
		commands = append(commands, "npm test")
	}
	if fileExists(filepath.Join(repoDir, "bun.lock")) || fileExists(filepath.Join(repoDir, "bun.lockb")) {
		commands = append(commands, "bun test")
	}
	return commands
}

var secretRedactors = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)Bearer\s+[^\s'\"]+`), "Bearer [REDACTED]"},
	{regexp.MustCompile(`(?i)oauth:[A-Za-z0-9._\-]+`), "oauth:[REDACTED]"},
	{regexp.MustCompile(`(?i)github_pat_[A-Za-z0-9_]+`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)gh[pousr]_[A-Za-z0-9_]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)sk-[A-Za-z0-9_\-]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)xox[baprs]-[A-Za-z0-9\-]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)((?:api[_-]?key|token|authorization|password|secret)\s*[:=]\s*)[^\s,;]+`), "${1}[REDACTED]"},
}

// RedactSecrets masks token-shaped values before writing preflight evidence.
func RedactSecrets(s string) string {
	out := s
	for _, redactor := range secretRedactors {
		out = redactor.re.ReplaceAllString(out, redactor.repl)
	}
	return out
}

func defaultCommandRunner(ctx context.Context, dir, name string, args ...string) CommandResult {
	cmd := exec.CommandContext(ctx, name, args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
}

func requiredTools(opts PreflightOptions, repoDir string) []string {
	seen := map[string]bool{}
	var tools []string
	add := func(tool string) {
		tool = strings.TrimSpace(tool)
		if tool == "" || seen[tool] {
			return
		}
		seen[tool] = true
		tools = append(tools, tool)
	}
	add(opts.BinaryPath)
	for _, tool := range opts.RequiredTools {
		add(tool)
	}
	if opts.RequireGitHubAuth {
		add("gh")
	}
	if fileExists(filepath.Join(repoDir, "go.mod")) {
		add("go")
	}
	if fileExists(filepath.Join(repoDir, "package.json")) {
		add("node")
	}
	if fileExists(filepath.Join(repoDir, "bun.lock")) || fileExists(filepath.Join(repoDir, "bun.lockb")) {
		add("bun")
	}
	sort.Strings(tools)
	return tools
}

func checkGitHubAuth(ctx context.Context, opts PreflightOptions, runner CommandRunner, workDir string, missingTools map[string]bool, add func(string, PreflightStatus, string, string)) {
	if !opts.RequireGitHubAuth {
		add("github auth", PreflightSkipped, "not required", "")
		return
	}
	if missingTools["gh"] {
		add("github auth", PreflightFailed, "gh CLI is not available", "Install gh and run gh auth login with PR/check/review permissions.")
		return
	}

	res := runner(ctx, workDir, "gh", "auth", "status", "-h", "github.com")
	combined := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
	if res.Err != nil {
		detail := strings.TrimSpace(combined)
		if detail == "" {
			detail = res.Err.Error()
		}
		add("github auth", PreflightFailed, detail, "Run gh auth login and ensure the token can create PRs, checks, and reviews.")
		return
	}

	scopes, foundScopes := parseGitHubScopes(combined)
	missingScopes := missingGitHubScopes(scopes, opts.RequiredGitHubScopes)
	if foundScopes && len(missingScopes) > 0 {
		add("github auth", PreflightFailed, "missing required scope(s): "+strings.Join(missingScopes, ", "), "Refresh gh auth with the required GitHub scopes.")
		return
	}
	if foundScopes {
		add("github auth", PreflightPassed, "authenticated with required scopes", "")
		return
	}
	add("github auth", PreflightPassed, "authenticated; scope header unavailable", "")
}

func checkWritablePath(name, path string, add func(string, PreflightStatus, string, string)) {
	path = strings.TrimSpace(path)
	if path == "" {
		add(name, PreflightFailed, "path is empty", "Configure a valid source/worktree path before starting the worker.")
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		add(name, PreflightFailed, err.Error(), "Create the path or fix permissions before starting the worker.")
		return
	}
	if !info.IsDir() {
		add(name, PreflightFailed, "path is not a directory", "Configure a directory path before starting the worker.")
		return
	}
	if info.Mode().Perm()&0o222 == 0 {
		add(name, PreflightFailed, "directory has no write permission bits", "Make the directory writable before starting the worker.")
		return
	}
	tmp, err := os.CreateTemp(path, ".ok-gobot-preflight-*")
	if err != nil {
		add(name, PreflightFailed, err.Error(), "Make the directory writable before starting the worker.")
		return
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		add(name, PreflightFailed, err.Error(), "Fix filesystem permissions before starting the worker.")
		return
	}
	_ = os.Remove(tmpName)
	add(name, PreflightPassed, "writable", "")
}

func checkGitTrust(ctx context.Context, runner CommandRunner, workDir string, missingTools map[string]bool, add func(string, PreflightStatus, string, string)) {
	if missingTools["git"] {
		add("git trust", PreflightFailed, "git is not available", "Install git before starting the worker.")
		return
	}
	root := runner(ctx, workDir, "git", "-C", workDir, "rev-parse", "--show-toplevel")
	if root.Err != nil {
		addGitFailure("git trust", root, add)
		return
	}
	status := runner(ctx, workDir, "git", "-C", workDir, "status", "--short")
	if status.Err != nil {
		addGitFailure("git trust", status, add)
		return
	}
	add("git trust", PreflightPassed, "git status succeeded", "")
}

func addGitFailure(name string, result CommandResult, add func(string, PreflightStatus, string, string)) {
	detail := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if detail == "" && result.Err != nil {
		detail = result.Err.Error()
	}
	remediation := "Fix the repository or git configuration before starting the worker."
	if strings.Contains(strings.ToLower(detail), "safe.directory") || strings.Contains(strings.ToLower(detail), "dubious ownership") {
		remediation = "Add the repository to git safe.directory after verifying ownership."
	}
	add(name, PreflightFailed, detail, remediation)
}

func checkNetwork(opts PreflightOptions, add func(string, PreflightStatus, string, string)) {
	if opts.NetworkDisabled {
		add("network allowlist", PreflightFailed, "network operations are disabled", "Enable network access for GitHub and model backend operations.")
		return
	}
	requiredHosts := uniqueStrings(opts.RequiredNetworkHosts)
	allowlist := uniqueStrings(opts.NetworkAllowlist)
	if len(requiredHosts) == 0 {
		add("network allowlist", PreflightSkipped, "no required hosts configured", "")
		return
	}
	if len(allowlist) == 0 {
		add("network allowlist", PreflightPassed, "all public hosts allowed", "")
		return
	}
	var blocked []string
	for _, host := range requiredHosts {
		if !hostMatchesAllowlist(host, allowlist) {
			blocked = append(blocked, host)
		}
	}
	if len(blocked) > 0 {
		add("network allowlist", PreflightFailed, "missing host(s): "+strings.Join(blocked, ", "), "Add the required GitHub/backend hosts to network_allowlist.")
		return
	}
	add("network allowlist", PreflightPassed, "required hosts allowed", "")
}

func parseGitHubScopes(output string) ([]string, bool) {
	var scopes []string
	for _, line := range strings.Split(output, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "token scopes") && !strings.Contains(lower, "x-oauth-scopes") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		raw := strings.NewReplacer("'", " ", "\"", " ", ",", " ").Replace(line[idx+1:])
		for _, field := range strings.Fields(raw) {
			field = strings.TrimSpace(field)
			if field != "" {
				scopes = append(scopes, field)
			}
		}
		return uniqueStrings(scopes), true
	}
	return nil, false
}

func missingGitHubScopes(scopes, required []string) []string {
	if len(required) == 0 {
		required = []string{"repo"}
	}
	var missing []string
	for _, req := range required {
		req = strings.TrimSpace(req)
		if req == "" {
			continue
		}
		if !hasGitHubScope(scopes, req) {
			missing = append(missing, req)
		}
	}
	return missing
}

func hasGitHubScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
		if required == "repo" && scope == "public_repo" {
			return true
		}
	}
	return false
}

func firstAvailable(lookPath func(string) (string, error), names []string) string {
	for _, name := range names {
		if _, err := lookPath(name); err == nil {
			return name
		}
	}
	return ""
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(strings.ToLower(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func hostMatchesAllowlist(host string, allowlist []string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, pattern := range allowlist {
		pattern = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
		if pattern == "" {
			continue
		}
		if strings.HasPrefix(pattern, "*.") {
			suffix := strings.TrimPrefix(pattern, "*.")
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == pattern {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
