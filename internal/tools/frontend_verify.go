package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
)

const (
	frontendVerifyDefaultWaitTimeout = 30 * time.Second
	frontendVerifyDefaultMaxRetries  = 3
	frontendVerifyRetryDelay         = 2 * time.Second
	frontendVerifyOpTimeout          = 60 * time.Second
	frontendVerifyHealthInterval     = 500 * time.Millisecond
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
	lookPath      func(string) (string, error)

	mu         sync.Mutex
	devServers map[string]*devServerProc // key: workDir
}

type devServerProc struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	done    chan error
	command string
}

// FrontendVerifyResult is the structured response from the tool.
type FrontendVerifyResult struct {
	Match          bool   `json:"match"`
	Score          string `json:"score,omitempty"`
	Feedback       string `json:"feedback"`
	Suggestions    string `json:"suggestions,omitempty"`
	ScreenshotPath string `json:"screenshot_path"`
	ScreenshotURI  string `json:"screenshot_uri,omitempty"`
	URL            string `json:"url,omitempty"`
	Status         string `json:"status,omitempty"`
	TextReport     string `json:"text_report,omitempty"`
	ServerRunning  bool   `json:"server_running"`
}

type frontendDevCommand struct {
	Command string
	Auto    bool
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
		lookPath:   exec.LookPath,
		devServers: make(map[string]*devServerProc),
	}
}

// SetArtifactRoots configures where frontend_verify writes screenshots. The
// first safe root is used, with a frontend_verify subdirectory beneath it.
func (t *FrontendVerifyTool) SetArtifactRoots(roots []string) {
	if t == nil {
		return
	}
	t.artifactRoots = append([]string(nil), roots...)
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
	rawURL := params["url"]
	if rawURL == "" {
		return "", fmt.Errorf("url is required")
	}
	if denial := CheckNetworkTarget("frontend_verify", rawURL, NetworkPolicyFromContext(ctx)); denial != nil {
		return "", denial
	}

	description := params["description"]
	command := params["command"]
	workDir := params["work_dir"]
	autoStart := params["auto_start"] == "true"
	autoDetectFromWorkDir := command == "" && workDir != ""

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

	// Start dev server if requested. With a work_dir and no explicit command,
	// first preserve the old "server already running" behavior, then detect the
	// available package manager from package.json if the URL is not yet reachable.
	if command != "" || autoStart {
		if err := t.ensureDevServer(ctx, command, workDir); err != nil {
			return "", fmt.Errorf("failed to start dev server: %w", err)
		}
	} else if autoDetectFromWorkDir {
		probeTimeout := waitTimeout
		if probeTimeout > time.Second {
			probeTimeout = time.Second
		}
		if err := t.waitForURL(ctx, rawURL, probeTimeout); err != nil {
			if err := t.ensureDevServer(ctx, "", workDir); err != nil {
				return "", fmt.Errorf("failed to start dev server: %w", err)
			}
		}
	}

	serverRunning := t.isDevServerRunning(workDir)

	// Wait for the URL to become accessible.
	if err := t.waitForURL(ctx, rawURL, waitTimeout); err != nil {
		result := FrontendVerifyResult{
			Match:         false,
			Feedback:      fmt.Sprintf("URL %s did not become accessible within %s: %v", rawURL, waitTimeout, err),
			ServerRunning: t.isDevServerRunning(workDir),
		}
		return marshalResult(finalizeFrontendVerifyResult(rawURL, result))
	}
	serverRunning = t.isDevServerRunning(workDir)

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
				Match:         false,
				Feedback:      fmt.Sprintf("screenshot failed: %v", err),
				ServerRunning: t.isDevServerRunning(workDir),
			}
			continue
		}

		lastResult = FrontendVerifyResult{
			ScreenshotPath: path,
			ScreenshotURI:  fileURI(path),
			ServerRunning:  serverRunning,
		}

		if description == "" || t.aiClient == nil {
			// No comparison requested — just return the screenshot path.
			lastResult.Match = true
			lastResult.Feedback = fmt.Sprintf("Screenshot saved to %s", path)
			break
		}

		cmpResult, err := t.compareWithLLM(ctx, imgData, description)
		if err != nil {
			logger.Debugf("frontend_verify: LLM comparison failed: %v", err)
			lastResult.Match = false
			lastResult.Feedback = fmt.Sprintf("Visual comparison failed: %v", err)
			continue
		}

		lastResult.Match = cmpResult.Match
		lastResult.Score = cmpResult.Score
		lastResult.Feedback = cmpResult.Feedback
		lastResult.Suggestions = cmpResult.Suggestions

		if lastResult.Match {
			break
		}
	}

	return marshalResult(finalizeFrontendVerifyResult(rawURL, lastResult))
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

	resolved, err := t.resolveDevCommand(command, workDir)
	if err != nil {
		return err
	}

	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, "sh", "-c", resolved.Command)
	if workDir != "" {
		cmd.Dir = workDir
	}
	// Discard output — agent should check via URL health check.
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start dev server command %q: %w", resolved.Command, err)
	}

	proc := &devServerProc{cmd: cmd, cancel: cancel, done: make(chan error, 1), command: resolved.Command}
	t.devServers[key] = proc
	go func() {
		proc.done <- cmd.Wait()
		close(proc.done)
	}()
	logger.Debugf("frontend_verify: started dev server pid=%d command=%q workDir=%q", cmd.Process.Pid, resolved.Command, workDir)
	return nil
}

func (p *devServerProc) isRunning() bool {
	if p == nil || p.done == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (t *FrontendVerifyTool) resolveDevCommand(command, workDir string) (frontendDevCommand, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return t.detectDevCommand(workDir)
	}
	if commandUsesShellFeatures(command) {
		return frontendDevCommand{Command: command}, nil
	}

	fields := strings.Fields(command)
	if len(fields) == 0 {
		return frontendDevCommand{}, fmt.Errorf("dev command is empty")
	}
	executable := commandExecutable(fields)
	if executable == "" {
		return frontendDevCommand{Command: command}, nil
	}
	if t.executableAvailable(executable) {
		return frontendDevCommand{Command: command}, nil
	}

	if isSupportedFrontendExecutable(executable) {
		fallback, err := t.detectDevCommand(workDir)
		if err == nil {
			logger.Debugf("frontend_verify: command %q unavailable; using detected command %q", command, fallback.Command)
			return fallback, nil
		}
		return frontendDevCommand{}, fmt.Errorf("dev command %q requires %q, but it is not in PATH; %w", command, executable, err)
	}

	return frontendDevCommand{}, fmt.Errorf("dev command executable %q is not available in PATH; install it or provide a supported frontend dev command (npm, pnpm, yarn, or bun)", executable)
}

func commandUsesShellFeatures(command string) bool {
	for _, token := range []string{"&&", "||", ";", "|", "$(", "`", ">", "<"} {
		if strings.Contains(command, token) {
			return true
		}
	}
	return false
}

func commandExecutable(fields []string) string {
	for _, field := range fields {
		if isEnvAssignment(field) {
			continue
		}
		return field
	}
	return ""
}

func isEnvAssignment(field string) bool {
	idx := strings.Index(field, "=")
	if idx <= 0 {
		return false
	}
	name := field[:idx]
	for i, r := range name {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func (t *FrontendVerifyTool) detectDevCommand(workDir string) (frontendDevCommand, error) {
	dir := strings.TrimSpace(workDir)
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return frontendDevCommand{}, fmt.Errorf("no dev command provided and current directory could not be resolved: %w", err)
		}
	}

	pkg, err := readPackageJSON(dir)
	if err != nil {
		return frontendDevCommand{}, err
	}
	script := frontendScriptName(pkg)
	if script == "" {
		return frontendDevCommand{}, fmt.Errorf("package.json in %s has no supported dev server script; add a \"dev\" or \"start\" script, or pass command explicitly", dir)
	}

	for _, manager := range packageManagerCandidates(dir, pkg.PackageManager) {
		if !t.executableAvailable(manager) {
			continue
		}
		return frontendDevCommand{Command: packageManagerCommand(manager, script), Auto: true}, nil
	}

	return frontendDevCommand{}, fmt.Errorf("no supported frontend dev command available for package.json in %s; install npm, pnpm, yarn, or bun, or pass command explicitly", dir)
}

type frontendPackageJSON struct {
	Scripts        map[string]string `json:"scripts"`
	PackageManager string            `json:"packageManager"`
}

func readPackageJSON(dir string) (frontendPackageJSON, error) {
	path := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return frontendPackageJSON{}, fmt.Errorf("no dev command provided and no package.json found in %s; start the server yourself or pass command explicitly", dir)
		}
		return frontendPackageJSON{}, fmt.Errorf("failed to read package.json in %s: %w", dir, err)
	}
	var pkg frontendPackageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return frontendPackageJSON{}, fmt.Errorf("failed to parse package.json in %s: %w", dir, err)
	}
	return pkg, nil
}

func frontendScriptName(pkg frontendPackageJSON) string {
	for _, name := range []string{"dev", "start"} {
		if strings.TrimSpace(pkg.Scripts[name]) != "" {
			return name
		}
	}
	return ""
}

func packageManagerCandidates(dir, packageManager string) []string {
	var candidates []string
	add := func(name string) {
		name = packageManagerName(name)
		if !isSupportedFrontendExecutable(name) {
			return
		}
		for _, existing := range candidates {
			if existing == name {
				return
			}
		}
		candidates = append(candidates, name)
	}

	add(packageManager)
	for _, item := range []struct {
		file    string
		manager string
	}{
		{file: "bun.lockb", manager: "bun"},
		{file: "bun.lock", manager: "bun"},
		{file: "pnpm-lock.yaml", manager: "pnpm"},
		{file: "yarn.lock", manager: "yarn"},
		{file: "package-lock.json", manager: "npm"},
		{file: "npm-shrinkwrap.json", manager: "npm"},
	} {
		if _, err := os.Stat(filepath.Join(dir, item.file)); err == nil {
			add(item.manager)
		}
	}
	for _, manager := range []string{"npm", "pnpm", "yarn", "bun"} {
		add(manager)
	}
	return candidates
}

func packageManagerName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, "@"); i > 0 {
		raw = raw[:i]
	}
	return strings.ToLower(raw)
}

func isSupportedFrontendExecutable(name string) bool {
	switch packageManagerName(name) {
	case "npm", "pnpm", "yarn", "bun":
		return true
	default:
		return false
	}
}

func packageManagerCommand(manager, script string) string {
	switch manager {
	case "npm", "pnpm", "bun":
		return fmt.Sprintf("%s run %s", manager, script)
	case "yarn":
		return fmt.Sprintf("yarn run %s", script)
	default:
		return ""
	}
}

func (t *FrontendVerifyTool) executableAvailable(name string) bool {
	lookPath := t.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath(name)
	return err == nil
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
	delete(t.devServers, workDir)
	return false
}

// waitForURL polls the URL with HTTP GET until it responds or timeout.
func (t *FrontendVerifyTool) waitForURL(ctx context.Context, rawURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	if policy := NetworkPolicyFromContext(ctx); policy != nil {
		client.Transport = SSRFSafeTransport(policy.AllowInternalNetworks)
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
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
// captures a full-page screenshot, saving it to screenshotDir.
// Localhost/loopback URLs are explicitly allowed for dev server use.
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("failed to create screenshot dir: %w", err)
	}

	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return nil, "", fmt.Errorf("failed to save screenshot: %w", err)
	}

	return buf, path, nil
}

func (t *FrontendVerifyTool) nextScreenshotPath(now time.Time) (string, error) {
	if strings.TrimSpace(t.screenshotDir) != "" {
		dir, err := filepath.Abs(t.screenshotDir)
		if err != nil {
			return "", fmt.Errorf("failed to resolve screenshot dir: %w", err)
		}
		filename := fmt.Sprintf("frontend_verify_%s.png", now.Format("20060102_150405.000000000"))
		return filepath.Join(filepath.Clean(dir), filename), nil
	}

	roots := artifactview.NormalizeRoots(t.artifactRoots)
	if len(roots) == 0 {
		roots = artifactview.DefaultRoots()
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("no safe artifact root configured for frontend_verify screenshots")
	}
	dir := filepath.Join(roots[0], "frontend_verify")
	filename := fmt.Sprintf("frontend_verify_%s.png", now.Format("20060102_150405.000000000"))
	path := filepath.Clean(filepath.Join(dir, filename))
	if pathInsideAnyRoot(path, roots) {
		return path, nil
	}
	return "", fmt.Errorf("generated screenshot path is outside configured artifact roots")
}

func pathInsideAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return true
		}
	}
	return false
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

func finalizeFrontendVerifyResult(rawURL string, r FrontendVerifyResult) FrontendVerifyResult {
	if r.URL == "" {
		r.URL = rawURL
	}
	if r.ScreenshotURI == "" && r.ScreenshotPath != "" {
		r.ScreenshotURI = fileURI(r.ScreenshotPath)
	}
	if r.Status == "" {
		if r.Match {
			r.Status = "passed"
		} else {
			r.Status = "failed"
		}
	}
	if strings.TrimSpace(r.TextReport) == "" {
		r.TextReport = buildFrontendVerifyTextReport(r)
	}
	return r
}

func buildFrontendVerifyTextReport(r FrontendVerifyResult) string {
	status := strings.TrimSpace(r.Status)
	if status == "" {
		if r.Match {
			status = "passed"
		} else {
			status = "failed"
		}
	}

	var parts []string
	if strings.TrimSpace(r.URL) != "" {
		parts = append(parts, fmt.Sprintf("frontend_verify %s for %s", status, r.URL))
	} else {
		parts = append(parts, fmt.Sprintf("frontend_verify %s", status))
	}
	if strings.TrimSpace(r.Score) != "" {
		parts = append(parts, "Score: "+strings.TrimSpace(r.Score))
	}
	if strings.TrimSpace(r.Feedback) != "" {
		parts = append(parts, "Feedback: "+strings.TrimSpace(r.Feedback))
	}
	if strings.TrimSpace(r.Suggestions) != "" {
		parts = append(parts, "Suggestions: "+strings.TrimSpace(r.Suggestions))
	}
	if r.ScreenshotPath != "" || r.ScreenshotURI != "" {
		parts = append(parts, "Screenshot: captured")
	}
	return truncateFrontendVerifyReport(strings.Join(parts, "\n"), 1200)
}

func truncateFrontendVerifyReport(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return strings.TrimSpace(s[:max-3]) + "..."
}

func fileURI(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
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
				"description": "Command to start the dev server, e.g. 'npm run dev', 'pnpm run dev', 'yarn run dev', or 'bun run dev'. Omit if server is already running or work_dir should be auto-detected.",
			},
			"work_dir": map[string]interface{}{
				"type":        "string",
				"description": "Working directory for the dev server command. If command is omitted, package.json is inspected here to choose npm/pnpm/yarn/bun.",
			},
			"auto_start": map[string]interface{}{
				"type":        "string",
				"description": "Set to 'true' to auto-detect and start a package.json dev/start script when command is omitted.",
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
