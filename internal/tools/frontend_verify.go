package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"ok-gobot/internal/ai"
	artifactview "ok-gobot/internal/artifacts"
	"ok-gobot/internal/browser"
	"ok-gobot/internal/logger"
	jobruntime "ok-gobot/internal/runtime"
)

const (
	frontendVerifyDefaultWaitTimeout = 30 * time.Second
	frontendVerifyDefaultMaxRetries  = 3
	frontendVerifyRetryDelay         = 2 * time.Second
	frontendVerifyOpTimeout          = 60 * time.Second
	frontendVerifyHealthInterval     = 500 * time.Millisecond
	frontendVerifyStartupGrace       = 500 * time.Millisecond
	frontendVerifyOutputLimit        = 32 * 1024

	frontendVerifyStatusPassed           = "passed"
	frontendVerifyStatusFailed           = "failed"
	frontendVerifyStatusUnreachable      = "unreachable"
	frontendVerifyStatusScreenshotFailed = "screenshot_failed"
	frontendVerifyStatusComparisonFailed = "comparison_failed"
)

// FrontendVerifyTool starts a local dev server, captures a screenshot via CDP,
// and uses LLM vision to verify the UI matches an expected description.
// It is designed to be called in a loop by an agent: the agent makes code changes,
// the dev server hot-reloads, and calling this tool again re-screenshots to check.
type FrontendVerifyTool struct {
	manager       *browser.Manager
	aiClient      ai.Client // nil = no LLM comparison, returns screenshot path only
	screenshotDir string
	artifactRoots []string

	mu         sync.Mutex
	devServers map[string]*devServerProc // key: workDir
}

type devServerProc struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	command string
	output  *boundedBuffer
	done    chan struct{}

	mu      sync.Mutex
	exited  bool
	exitErr error
}

// FrontendVerifyResult is the structured response from the tool.
type FrontendVerifyResult struct {
	Match              bool   `json:"match"`
	Score              string `json:"score,omitempty"`
	Feedback           string `json:"feedback"`
	Suggestions        string `json:"suggestions,omitempty"`
	ScreenshotPath     string `json:"screenshot_path"`
	ScreenshotURI      string `json:"screenshot_uri,omitempty"`
	TargetURL          string `json:"target_url,omitempty"`
	VerificationStatus string `json:"verification_status,omitempty"`
	TextReport         string `json:"text_report,omitempty"`
	ServerRunning      bool   `json:"server_running"`
	DevCommand         string `json:"dev_command,omitempty"`
}

// NewFrontendVerifyTool creates a FrontendVerifyTool with its own browser.Manager instance.
// The manager uses the ephemeral headless profile so screenshots are clean and isolated.
func NewFrontendVerifyTool(browserProfile, chromePath, debugURL string, aiClient ai.Client) *FrontendVerifyTool {
	mgr := browser.NewManager(browserProfile)
	mgr.Headless = true
	if chromePath != "" {
		mgr.ChromePath = chromePath
	}
	if debugURL != "" {
		mgr.RemoteDebugURL = debugURL
	}
	return &FrontendVerifyTool{
		manager:    mgr,
		aiClient:   aiClient,
		devServers: make(map[string]*devServerProc),
	}
}

// SetArtifactRoots configures the safe local roots where screenshots may be
// written. When unset, frontend_verify uses the same default artifact roots as
// Mission Control/API artifact display.
func (t *FrontendVerifyTool) SetArtifactRoots(roots []string) {
	t.artifactRoots = artifactview.NormalizeRoots(roots)
}

func (t *FrontendVerifyTool) Name() string { return "frontend_verify" }

func (t *FrontendVerifyTool) Description() string {
	return "Verify frontend UI output: optionally start a local dev server, take a CDP screenshot, " +
		"and compare the result against an expected description using LLM vision. " +
		"Call repeatedly after code changes to iterate until the UI matches."
}

func (t *FrontendVerifyTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: frontend_verify <url> [description]")
	}
	params := map[string]string{"url": args[0]}
	if len(args) >= 2 {
		params["description"] = args[1]
	}
	return t.ExecuteJSON(ctx, params)
}

func (t *FrontendVerifyTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rawURL := strings.TrimSpace(params["url"])
	if rawURL == "" {
		return "", fmt.Errorf("url is required")
	}

	description := params["description"]
	command := strings.TrimSpace(params["command"])
	workDir := strings.TrimSpace(params["work_dir"])
	resolvedCommand := ""

	waitTimeout := frontendVerifyDefaultWaitTimeout
	if s := params["wait_timeout"]; s != "" {
		var secs int
		if _, err := fmt.Sscanf(s, "%d", &secs); err == nil && secs > 0 {
			waitTimeout = time.Duration(secs) * time.Second
		}
	}

	maxRetries := frontendVerifyDefaultMaxRetries
	if s := params["max_retries"]; s != "" {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			maxRetries = n
		}
	}

	// Handle stop_server command.
	if params["stop_server"] == "true" {
		t.stopDevServer(workDir)
		return `{"stopped":true}`, nil
	}

	// Start dev server if a command was provided.
	if command != "" {
		if policy := NetworkPolicyFromContext(ctx); policy != nil && !policy.Shell {
			return "", &ToolDenial{
				ToolName:    t.Name(),
				Family:      "shell",
				Reason:      "frontend_verify dev server startup requires shell execution, which is denied by agent policy",
				Remediation: "Ask the operator to allow shell execution or start the dev server outside frontend_verify.",
			}
		}

		resolution, err := t.resolveDevCommand(command, workDir)
		if err != nil {
			return "", err
		}
		resolvedCommand = resolution.Command
		if err := t.ensureDevServer(ctx, resolvedCommand, workDir); err != nil {
			return "", fmt.Errorf("failed to start dev server: %w", err)
		}
	}

	serverRunning := t.isDevServerRunning(workDir)

	// Wait for the URL to become accessible.
	if err := t.waitForURL(ctx, rawURL, waitTimeout); err != nil {
		feedback := fmt.Sprintf("URL %s did not become accessible within %s: %v", rawURL, waitTimeout, err)
		if exited, exitErr, output := t.devServerExit(workDir); exited && resolvedCommand != "" {
			feedback = fmt.Sprintf("dev server command %q exited before %s became reachable: %v", resolvedCommand, rawURL, exitErr)
			if output != "" {
				feedback += fmt.Sprintf("; output: %s", output)
			}
		}
		result := FrontendVerifyResult{
			Match:              false,
			Feedback:           feedback,
			TargetURL:          rawURL,
			VerificationStatus: frontendVerifyStatusUnreachable,
			ServerRunning:      serverRunning,
			DevCommand:         resolvedCommand,
		}
		return marshalResult(finalizeFrontendVerifyResult(result))
	}

	// Iterate: take screenshot(s), compare, retry if hot-reload is still settling.
	var lastResult FrontendVerifyResult
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			// Wait for hot-reload to settle before re-screenshotting.
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(frontendVerifyRetryDelay):
			}
		}

		imgData, path, err := t.captureScreenshot(ctx, rawURL)
		if err != nil {
			lastResult = FrontendVerifyResult{
				Match:              false,
				Feedback:           fmt.Sprintf("screenshot failed: %v", err),
				TargetURL:          rawURL,
				VerificationStatus: frontendVerifyStatusScreenshotFailed,
				ServerRunning:      serverRunning,
				DevCommand:         resolvedCommand,
			}
			continue
		}

		lastResult = FrontendVerifyResult{
			ScreenshotPath: path,
			ScreenshotURI:  path,
			TargetURL:      rawURL,
			ServerRunning:  serverRunning,
			DevCommand:     resolvedCommand,
		}

		if description == "" || t.aiClient == nil {
			// No comparison requested — just return the screenshot path.
			lastResult.Match = true
			lastResult.VerificationStatus = frontendVerifyStatusPassed
			lastResult.Feedback = fmt.Sprintf("Screenshot saved to %s", path)
			break
		}

		cmpResult, err := t.compareWithLLM(ctx, imgData, description)
		if err != nil {
			logger.Debugf("frontend_verify: LLM comparison failed: %v", err)
			lastResult.Match = false
			lastResult.VerificationStatus = frontendVerifyStatusComparisonFailed
			lastResult.Feedback = fmt.Sprintf("Visual comparison failed: %v", err)
			continue
		}

		lastResult.Match = cmpResult.Match
		if cmpResult.Match {
			lastResult.VerificationStatus = frontendVerifyStatusPassed
		} else {
			lastResult.VerificationStatus = frontendVerifyStatusFailed
		}
		lastResult.Score = cmpResult.Score
		lastResult.Feedback = cmpResult.Feedback
		lastResult.Suggestions = cmpResult.Suggestions

		if lastResult.Match {
			break
		}
	}

	return marshalResult(finalizeFrontendVerifyResult(lastResult))
}

// ensureDevServer starts a dev server for the given command+workDir if one is not already running.
func (t *FrontendVerifyTool) ensureDevServer(ctx context.Context, command, workDir string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := workDir
	if proc, ok := t.devServers[key]; ok {
		// Already running — check if process is still alive.
		if proc.isRunning() {
			logger.Debugf("frontend_verify: dev server already running for %s", key)
			return nil
		}
		// Dead — clean up and restart.
		proc.cancel()
		delete(t.devServers, key)
	}

	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, "sh", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}
	output := newBoundedBuffer(frontendVerifyOutputLimit)
	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start dev server command %q: %w", command, err)
	}
	proc := &devServerProc{
		cmd:     cmd,
		cancel:  cancel,
		command: command,
		output:  output,
		done:    make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		proc.mu.Lock()
		proc.exited = true
		proc.exitErr = err
		proc.mu.Unlock()
		close(proc.done)
	}()

	select {
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	case <-proc.done:
		cancel()
		msg := fmt.Sprintf("dev server command %q exited immediately", command)
		_, exitErr := proc.exitStatus()
		if exitErr != nil {
			msg = fmt.Sprintf("%s: %v", msg, exitErr)
		}
		if out := output.String(); out != "" {
			msg = fmt.Sprintf("%s; output: %s", msg, out)
		}
		return fmt.Errorf("%s", msg)
	case <-time.After(frontendVerifyStartupGrace):
	}

	t.devServers[key] = proc
	logger.Debugf("frontend_verify: started dev server pid=%d for workDir=%q", cmd.Process.Pid, workDir)
	return nil
}

// stopDevServer kills the dev server for workDir.
func (t *FrontendVerifyTool) stopDevServer(workDir string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := workDir
	if proc, ok := t.devServers[key]; ok {
		proc.cancel()
		delete(t.devServers, key)
		logger.Debugf("frontend_verify: stopped dev server for workDir=%q", workDir)
	}
}

// isDevServerRunning reports whether a dev server is tracked for workDir.
func (t *FrontendVerifyTool) isDevServerRunning(workDir string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	proc, ok := t.devServers[workDir]
	if !ok {
		return false
	}
	if proc.isRunning() {
		return true
	}
	return false
}

func (t *FrontendVerifyTool) devServerExit(workDir string) (bool, error, string) {
	t.mu.Lock()
	proc, ok := t.devServers[workDir]
	t.mu.Unlock()
	if !ok {
		return false, nil, ""
	}
	exited, err := proc.exitStatus()
	return exited, err, proc.output.String()
}

func (p *devServerProc) isRunning() bool {
	exited, _ := p.exitStatus()
	return !exited
}

func (p *devServerProc) exitStatus() (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exited, p.exitErr
}

// waitForURL polls the URL with HTTP GET until it responds or timeout.
func (t *FrontendVerifyTool) waitForURL(ctx context.Context, rawURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	if policy := NetworkPolicyFromContext(ctx); policy != nil {
		if denial := CheckNetworkTarget(t.Name(), rawURL, policy); denial != nil {
			return denial
		}
		client.Transport = SSRFSafeTransport(policy.AllowInternalNetworks)
		client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
			if denial := CheckNetworkTarget(t.Name(), req.URL.String(), policy); denial != nil {
				return denial
			}
			return nil
		}
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		lastErr = err
		time.Sleep(frontendVerifyHealthInterval)
	}
	if lastErr == nil {
		return fmt.Errorf("timed out waiting for %s", rawURL)
	}
	return fmt.Errorf("timed out waiting for %s: %w", rawURL, lastErr)
}

// captureScreenshot navigates to rawURL using the ephemeral browser profile and
// captures a full-page screenshot under a safe artifact root.
// Localhost/loopback URLs are allowed unless an agent network policy disallows
// internal networks.
func (t *FrontendVerifyTool) captureScreenshot(ctx context.Context, rawURL string) ([]byte, string, error) {
	if err := t.manager.StartProfile(browser.ProfileEphemeral); err != nil {
		return nil, "", fmt.Errorf("failed to start ephemeral browser: %w", err)
	}

	tabCtx, cancel, err := t.manager.NewTabForProfile(browser.ProfileEphemeral)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create browser tab: %w", err)
	}
	defer cancel()

	opCtx, opCancel := context.WithTimeout(tabCtx, frontendVerifyOpTimeout)
	defer opCancel()

	var buf []byte
	if err := chromedp.Run(opCtx,
		chromedp.Navigate(rawURL),
		chromedp.WaitReady("body"),
		chromedp.FullScreenshot(&buf, 90),
	); err != nil {
		return nil, "", fmt.Errorf("chromedp screenshot failed: %w", err)
	}

	path, err := t.nextScreenshotPath(time.Now())
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return nil, "", fmt.Errorf("failed to save screenshot: %w", err)
	}

	return buf, path, nil
}

func (t *FrontendVerifyTool) nextScreenshotPath(now time.Time) (string, error) {
	roots := t.screenshotRoots()
	if len(roots) == 0 {
		return "", fmt.Errorf("no safe artifact root is configured for frontend_verify screenshots")
	}

	dir := roots[0]
	if strings.TrimSpace(t.screenshotDir) == "" && len(t.artifactRoots) > 0 {
		dir = filepath.Join(dir, "frontend_verify")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create screenshot dir: %w", err)
	}
	safeDir, ok := artifactview.SafeLocalPath(dir, roots)
	if !ok {
		return "", fmt.Errorf("screenshot dir %q is outside configured artifact roots", dir)
	}

	filename := fmt.Sprintf("frontend_verify_%s_%d.png", now.Format("20060102_150405"), now.UnixNano())
	return filepath.Join(safeDir, filename), nil
}

func (t *FrontendVerifyTool) screenshotRoots() []string {
	if strings.TrimSpace(t.screenshotDir) != "" {
		return artifactview.NormalizeRoots([]string{t.screenshotDir})
	}
	if len(t.artifactRoots) > 0 {
		return artifactview.NormalizeRoots(t.artifactRoots)
	}
	return artifactview.DefaultRoots()
}

type devCommandResolution struct {
	Command string
	Source  string
}

func (t *FrontendVerifyTool) resolveDevCommand(command, workDir string) (devCommandResolution, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return devCommandResolution{}, fmt.Errorf("dev server command is required")
	}
	if isAutoDevCommand(command) {
		return detectDevCommand(workDir)
	}

	executable := commandExecutable(command)
	if executable == "" {
		return devCommandResolution{}, fmt.Errorf("dev server command %q has no executable", command)
	}
	if _, err := exec.LookPath(executable); err == nil {
		return devCommandResolution{Command: command, Source: "explicit"}, nil
	}

	manager := filepath.Base(executable)
	if isSupportedPackageManager(manager) {
		if detected, err := detectDevCommand(workDir); err == nil {
			return devCommandResolution{Command: detected.Command, Source: "detected_fallback"}, nil
		} else {
			return devCommandResolution{}, fmt.Errorf("dev server command %q requires %q, but it was not found in PATH; install %s, pass another command, or use command=\"auto\" with an available package manager: %w", command, executable, executable, err)
		}
	}

	return devCommandResolution{}, fmt.Errorf("dev server command executable %q was not found in PATH; install it or pass a supported frontend dev command such as command=\"auto\", \"npm run dev\", \"pnpm run dev\", \"yarn run dev\", or \"bun run dev\"", executable)
}

func isAutoDevCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "auto", "detect", "detected":
		return true
	default:
		return false
	}
}

func commandExecutable(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	i := 0
	if fields[i] == "env" {
		i++
	}
	for i < len(fields) && isEnvAssignment(fields[i]) {
		i++
	}
	if i >= len(fields) {
		return ""
	}
	return fields[i]
}

func isEnvAssignment(token string) bool {
	idx := strings.Index(token, "=")
	return idx > 0 && !strings.HasPrefix(token, "-")
}

func detectDevCommand(workDir string) (devCommandResolution, error) {
	dir := strings.TrimSpace(workDir)
	if dir == "" {
		dir = "."
	}
	scripts, err := readPackageScripts(dir)
	if err != nil {
		return devCommandResolution{}, err
	}
	script := preferredDevScript(scripts)
	if script == "" {
		return devCommandResolution{}, fmt.Errorf("package.json in %s has no supported frontend dev script (expected \"dev\" or \"start\"); add one or pass an explicit command", dir)
	}

	for _, manager := range packageManagerCandidates(dir) {
		if _, err := exec.LookPath(manager); err == nil {
			return devCommandResolution{Command: packageManagerCommand(manager, script), Source: "detected"}, nil
		}
	}
	return devCommandResolution{}, fmt.Errorf("no supported frontend dev command is available in %s: package.json has script %q, but none of bun, pnpm, yarn, or npm were found in PATH; install one or pass an explicit command", dir, script)
}

func readPackageScripts(dir string) (map[string]string, error) {
	path := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no package.json found in %s; pass an explicit dev server command or set command=\"auto\" in a frontend project directory", dir)
		}
		return nil, fmt.Errorf("read package.json in %s: %w", dir, err)
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("parse package.json in %s: %w", dir, err)
	}
	return pkg.Scripts, nil
}

func preferredDevScript(scripts map[string]string) string {
	if strings.TrimSpace(scripts["dev"]) != "" {
		return "dev"
	}
	if strings.TrimSpace(scripts["start"]) != "" {
		return "start"
	}
	return ""
}

func packageManagerCandidates(dir string) []string {
	var candidates []string
	add := func(manager string) {
		for _, existing := range candidates {
			if existing == manager {
				return
			}
		}
		candidates = append(candidates, manager)
	}
	if fileExists(filepath.Join(dir, "bun.lock")) || fileExists(filepath.Join(dir, "bun.lockb")) {
		add("bun")
	}
	if fileExists(filepath.Join(dir, "pnpm-lock.yaml")) {
		add("pnpm")
	}
	if fileExists(filepath.Join(dir, "yarn.lock")) {
		add("yarn")
	}
	if fileExists(filepath.Join(dir, "package-lock.json")) || fileExists(filepath.Join(dir, "npm-shrinkwrap.json")) {
		add("npm")
	}
	for _, manager := range []string{"npm", "pnpm", "yarn", "bun"} {
		add(manager)
	}
	return candidates
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func isSupportedPackageManager(manager string) bool {
	switch manager {
	case "bun", "pnpm", "yarn", "npm":
		return true
	default:
		return false
	}
}

func packageManagerCommand(manager, script string) string {
	return fmt.Sprintf("%s run %s", manager, script)
}

type boundedBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if b.limit > 0 && len(b.buf) > b.limit {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.limit:]...)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

// JobArtifacts converts a frontend_verify result into durable artifacts that a
// role job can persist when it wants to expose the proof in Mission Control.
func (r FrontendVerifyResult) JobArtifacts() []jobruntime.JobArtifactSpec {
	r = finalizeFrontendVerifyResult(r)
	metadata := map[string]any{
		"tool":                "frontend_verify",
		"target_url":          r.TargetURL,
		"verification_status": r.VerificationStatus,
		"match":               r.Match,
	}
	if r.Score != "" {
		metadata["score"] = r.Score
	}

	var out []jobruntime.JobArtifactSpec
	if uri := strings.TrimSpace(firstNonEmpty(r.ScreenshotURI, r.ScreenshotPath)); uri != "" {
		out = append(out, jobruntime.JobArtifactSpec{
			Name:     "frontend_verify screenshot",
			Type:     "screenshot",
			MimeType: "image/png",
			URI:      uri,
			Metadata: metadata,
		})
	}
	if strings.TrimSpace(r.TargetURL) != "" {
		out = append(out, jobruntime.JobArtifactSpec{
			Name:     "frontend_verify target",
			Type:     artifactview.KindURL,
			MimeType: "text/uri-list",
			URI:      r.TargetURL,
			Metadata: metadata,
		})
	}
	if strings.TrimSpace(r.TextReport) != "" {
		out = append(out, jobruntime.JobArtifactSpec{
			Name:     "frontend_verify report",
			Type:     artifactview.KindTextReport,
			MimeType: "text/plain",
			Content:  r.TextReport,
			Metadata: metadata,
		})
	}
	return out
}

func finalizeFrontendVerifyResult(r FrontendVerifyResult) FrontendVerifyResult {
	if r.VerificationStatus == "" {
		if r.Match {
			r.VerificationStatus = frontendVerifyStatusPassed
		} else {
			r.VerificationStatus = frontendVerifyStatusFailed
		}
	}
	if r.ScreenshotURI == "" {
		r.ScreenshotURI = r.ScreenshotPath
	}
	if strings.TrimSpace(r.TextReport) == "" {
		r.TextReport = frontendVerifyTextReport(r)
	}
	return r
}

func frontendVerifyTextReport(r FrontendVerifyResult) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("frontend_verify %s", r.VerificationStatus))
	if r.TargetURL != "" {
		parts = append(parts, fmt.Sprintf("url: %s", r.TargetURL))
	}
	if r.Score != "" {
		parts = append(parts, fmt.Sprintf("score: %s", r.Score))
	}
	if r.Feedback != "" {
		parts = append(parts, fmt.Sprintf("feedback: %s", r.Feedback))
	}
	if r.Suggestions != "" {
		parts = append(parts, fmt.Sprintf("suggestions: %s", r.Suggestions))
	}
	if r.ScreenshotURI != "" {
		parts = append(parts, fmt.Sprintf("screenshot: %s", r.ScreenshotURI))
	}
	return strings.Join(parts, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type llmCompareResult struct {
	Match       bool
	Score       string
	Feedback    string
	Suggestions string
}

// compareWithLLM sends the screenshot + description to the LLM for visual comparison.
// It uses the ai.Client.CompleteWithTools path with multimodal ContentBlocks so it works
// with both AnthropicClient (native vision) and OpenAI-compatible clients.
func (t *FrontendVerifyTool) compareWithLLM(ctx context.Context, imgData []byte, description string) (*llmCompareResult, error) {
	encoded := base64.StdEncoding.EncodeToString(imgData)

	prompt := fmt.Sprintf(`You are a frontend UI reviewer. Analyze the screenshot and compare it against the expected description.

Expected description:
%s

Respond in JSON with this exact structure:
{
  "match": true or false,
  "score": "X/10",
  "feedback": "concise summary of what matches and what does not",
  "suggestions": "specific code changes needed to fix mismatches (empty string if match is true)"
}

Be strict but fair. Only set "match": true if the UI substantially matches the description.`, description)

	// Build a multimodal ChatMessage using ContentBlocks. This is handled by both
	// AnthropicClient (via translateMessages → toAnthropicUserBlocks) and
	// OpenAICompatibleClient (via ChatMessage.MarshalJSON → image_url parts).
	msg := ai.ChatMessage{
		Role: ai.RoleUser,
		ContentBlocks: []ai.ContentBlock{
			{
				Type: "image",
				Source: &ai.ContentSource{
					Type:      "base64",
					MediaType: "image/png",
					Data:      encoded,
				},
			},
			{
				Type: "text",
				Text: prompt,
			},
		},
	}

	resp, err := t.aiClient.CompleteWithTools(ctx, []ai.ChatMessage{msg}, nil)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("LLM returned empty response")
	}

	return parseLLMCompareResult(resp.Choices[0].Message.Content)
}

// parseLLMCompareResult extracts the structured comparison result from the LLM response.
func parseLLMCompareResult(raw string) (*llmCompareResult, error) {
	// Find JSON object in the response (model may include preamble).
	start := -1
	end := -1
	depth := 0
	for i, ch := range raw {
		switch ch {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
		if end > 0 {
			break
		}
	}

	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON found in LLM response: %.200s", raw)
	}

	var out struct {
		Match       bool   `json:"match"`
		Score       string `json:"score"`
		Feedback    string `json:"feedback"`
		Suggestions string `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(raw[start:end]), &out); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	return &llmCompareResult{
		Match:       out.Match,
		Score:       out.Score,
		Feedback:    out.Feedback,
		Suggestions: out.Suggestions,
	}, nil
}

func marshalResult(r FrontendVerifyResult) (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("failed to encode result: %w", err)
	}
	return string(b), nil
}

func (t *FrontendVerifyTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to verify, e.g. http://localhost:3000 or http://localhost:5173",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Expected UI description for LLM visual comparison. If omitted, tool only captures a screenshot.",
			},
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Shell command to start the dev server, e.g. 'npm run dev', 'pnpm run dev', 'yarn run dev', 'bun run dev', or 'auto' to detect from package.json. Omit if server is already running.",
			},
			"work_dir": map[string]interface{}{
				"type":        "string",
				"description": "Working directory for the dev server command. Defaults to current directory.",
			},
			"wait_timeout": map[string]interface{}{
				"type":        "string",
				"description": "Seconds to wait for the URL to become accessible after starting the server (default: 30).",
			},
			"max_retries": map[string]interface{}{
				"type":        "string",
				"description": "Max screenshot+compare iterations to allow hot-reload to settle (default: 3).",
			},
			"stop_server": map[string]interface{}{
				"type":        "string",
				"description": "Set to 'true' to stop the running dev server for work_dir.",
			},
		},
		"required": []string{"url"},
	}
}

// Stop shuts down all tracked dev server processes. Call during tool teardown.
func (t *FrontendVerifyTool) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, proc := range t.devServers {
		proc.cancel()
		delete(t.devServers, key)
	}
}
