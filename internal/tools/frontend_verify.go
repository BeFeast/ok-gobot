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
	frontendVerifyAutoCommand        = "auto"
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
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan error

	mu     sync.Mutex
	exited bool
	err    error
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
	DevCommand         string `json:"dev_command,omitempty"`
	ServerRunning      bool   `json:"server_running"`
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

func (t *FrontendVerifyTool) Name() string { return "frontend_verify" }

func (t *FrontendVerifyTool) Description() string {
	return "Verify frontend UI output: optionally start a local dev server, take a CDP screenshot, " +
		"and compare the result against an expected description using LLM vision. " +
		"Use command=auto to detect npm, pnpm, yarn, or bun when practical. " +
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
	if err := validateFrontendVerifyURL(ctx, rawURL); err != nil {
		return "", err
	}

	description := params["description"]
	command := strings.TrimSpace(params["command"])
	workDir := params["work_dir"]

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

	if command == "" && parseFrontendVerifyBool(params["auto_start"]) {
		command = frontendVerifyAutoCommand
	}

	// Start dev server if a command was provided or auto-detection was requested.
	resolvedCommand := ""
	if command != "" {
		var err error
		resolvedCommand, err = resolveFrontendDevCommand(command, workDir)
		if err != nil {
			return "", err
		}
		if err := t.ensureDevServer(ctx, resolvedCommand, workDir); err != nil {
			return "", fmt.Errorf("failed to start dev server: %w", err)
		}
	}

	serverRunning := t.isDevServerRunning(workDir)

	// Wait for the URL to become accessible.
	if err := t.waitForURL(ctx, rawURL, waitTimeout); err != nil {
		result := FrontendVerifyResult{
			Match:         false,
			Feedback:      fmt.Sprintf("URL %s did not become accessible within %s: %v", rawURL, waitTimeout, err),
			TargetURL:     rawURL,
			DevCommand:    resolvedCommand,
			ServerRunning: t.isDevServerRunning(workDir),
		}
		return marshalResult(result)
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

		imgData, path, screenshotURI, err := t.captureScreenshot(ctx, rawURL)
		if err != nil {
			lastResult = FrontendVerifyResult{
				Match:         false,
				Feedback:      fmt.Sprintf("screenshot failed: %v", err),
				TargetURL:     rawURL,
				DevCommand:    resolvedCommand,
				ServerRunning: serverRunning,
			}
			continue
		}

		lastResult = FrontendVerifyResult{
			ScreenshotPath: path,
			ScreenshotURI:  screenshotURI,
			TargetURL:      rawURL,
			DevCommand:     resolvedCommand,
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

	return marshalResult(lastResult)
}

// ensureDevServer starts a dev server for the given command+workDir if one is not already running.
func (t *FrontendVerifyTool) ensureDevServer(ctx context.Context, command, workDir string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := workDir
	if proc, ok := t.devServers[key]; ok {
		// Already running — check if process is still alive.
		if proc.running() {
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
	// Discard output — agent should check via URL health check.
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start dev server command %q: %w", command, err)
	}

	proc := &devServerProc{cmd: cmd, cancel: cancel, done: make(chan error, 1)}
	go func() {
		proc.done <- cmd.Wait()
	}()

	t.devServers[key] = proc
	logger.Debugf("frontend_verify: started dev server pid=%d for workDir=%q", cmd.Process.Pid, workDir)
	return nil
}

func (p *devServerProc) running() bool {
	if p == nil {
		return false
	}
	select {
	case err := <-p.done:
		p.mu.Lock()
		p.exited = true
		p.err = err
		p.mu.Unlock()
		return false
	default:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.exited
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
	return ok && proc.running()
}

// waitForURL polls the URL with HTTP GET until it responds or timeout.
func (t *FrontendVerifyTool) waitForURL(ctx context.Context, rawURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(rawURL) //nolint:noctx // intentional poll; context checked above
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

func validateFrontendVerifyURL(ctx context.Context, rawURL string) error {
	if policy := NetworkPolicyFromContext(ctx); policy != nil {
		if denial := CheckNetworkTarget("frontend_verify", rawURL, policy); denial != nil {
			return denial
		}
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed == nil || parsed.Hostname() == "" {
		return fmt.Errorf("frontend_verify requires a full http or https URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported URL scheme: %s", scheme)
	}
	return nil
}

func parseFrontendVerifyBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func resolveFrontendDevCommand(command, workDir string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", nil
	}
	if isFrontendAutoCommand(command) {
		return detectFrontendDevCommand(workDir)
	}

	exe := firstShellExecutable(command)
	if exe == "" || isShellBuiltin(exe) || containsShellControl(command) {
		return command, nil
	}
	if _, err := exec.LookPath(exe); err == nil {
		return command, nil
	}
	if isSupportedFrontendPackageManager(exe) {
		fallback, err := detectFrontendDevCommand(workDir)
		if err == nil {
			logger.Debugf("frontend_verify: using detected dev command %q because %q is not in PATH", fallback, exe)
			return fallback, nil
		}
		return "", fmt.Errorf("dev server command %q requires %q, but %q is not in PATH; %v", command, exe, exe, err)
	}
	return "", fmt.Errorf("dev server command executable %q is not in PATH; install it or pass command=auto in a frontend project with package.json", exe)
}

func isFrontendAutoCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case frontendVerifyAutoCommand, "detect", "auto-detect":
		return true
	default:
		return false
	}
}

func detectFrontendDevCommand(workDir string) (string, error) {
	dir, err := frontendWorkDir(workDir)
	if err != nil {
		return "", err
	}

	scripts, err := frontendPackageScripts(dir)
	if err != nil {
		return "", err
	}
	script, ok := preferredFrontendDevScript(scripts)
	if !ok {
		return "", fmt.Errorf("no supported frontend dev command available in %s: package.json has no dev, start, serve, or preview script; pass a dev server command explicitly", dir)
	}

	for _, manager := range frontendPackageManagerOrder(dir) {
		if _, err := exec.LookPath(manager); err == nil {
			return frontendRunScriptCommand(manager, script), nil
		}
	}

	return "", fmt.Errorf("no supported frontend dev command available in %s: package.json has a %q script, but none of npm, pnpm, yarn, or bun are available in PATH; install one of them or pass a dev server command explicitly", dir, script)
}

func frontendWorkDir(workDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return os.Getwd()
	}
	return filepath.Abs(workDir)
}

func frontendPackageScripts(dir string) (map[string]string, error) {
	path := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no supported frontend dev command available in %s: package.json not found; pass a dev server command explicitly", dir)
		}
		return nil, fmt.Errorf("read package.json: %w", err)
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	return pkg.Scripts, nil
}

func preferredFrontendDevScript(scripts map[string]string) (string, bool) {
	for _, name := range []string{"dev", "start", "serve", "preview"} {
		if strings.TrimSpace(scripts[name]) != "" {
			return name, true
		}
	}
	return "", false
}

func frontendPackageManagerOrder(dir string) []string {
	var order []string
	add := func(name string) {
		for _, existing := range order {
			if existing == name {
				return
			}
		}
		order = append(order, name)
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
	if fileExists(filepath.Join(dir, "package-lock.json")) {
		add("npm")
	}
	for _, name := range []string{"npm", "pnpm", "yarn", "bun"} {
		add(name)
	}
	return order
}

func frontendRunScriptCommand(manager, script string) string {
	switch manager {
	case "npm":
		if script == "start" {
			return "npm start"
		}
		return "npm run " + script
	case "yarn":
		return "yarn " + script
	default:
		return manager + " run " + script
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func firstShellExecutable(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	i := 0
	if fields[i] == "env" {
		i++
	}
	for i < len(fields) && strings.Contains(fields[i], "=") && !strings.HasPrefix(fields[i], "=") {
		i++
	}
	if i >= len(fields) {
		return ""
	}
	return strings.Trim(fields[i], "'\"")
}

func isShellBuiltin(name string) bool {
	switch name {
	case "cd", "exec", "source", ".", "export", "ulimit":
		return true
	default:
		return false
	}
}

func containsShellControl(command string) bool {
	return strings.ContainsAny(command, "|&;()<>")
}

func isSupportedFrontendPackageManager(name string) bool {
	switch name {
	case "npm", "pnpm", "yarn", "bun":
		return true
	default:
		return false
	}
}

// captureScreenshot navigates to rawURL using the ephemeral browser profile and
// captures a full-page screenshot, saving it to screenshotDir.
// Localhost/loopback URLs are explicitly allowed for dev server use.
func (t *FrontendVerifyTool) captureScreenshot(ctx context.Context, rawURL string) ([]byte, string, string, error) {
	if err := t.manager.StartProfile(browser.ProfileEphemeral); err != nil {
		return nil, "", "", fmt.Errorf("failed to start ephemeral browser: %w", err)
	}

	tabCtx, cancel, err := t.manager.NewTabForProfile(browser.ProfileEphemeral)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to create browser tab: %w", err)
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
		return nil, "", "", fmt.Errorf("chromedp screenshot failed: %w", err)
	}

	path, err := t.newScreenshotPath(time.Now())
	if err != nil {
		return nil, "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, "", "", fmt.Errorf("failed to create screenshot dir: %w", err)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return nil, "", "", fmt.Errorf("failed to save screenshot: %w", err)
	}

	return buf, path, localFileURI(path), nil
}

func (t *FrontendVerifyTool) newScreenshotPath(now time.Time) (string, error) {
	filename := fmt.Sprintf("frontend_verify_%s_%09d.png", now.Format("20060102_150405"), now.Nanosecond())
	return artifactview.GeneratedLocalPath(t.screenshotRoots(), filename)
}

func (t *FrontendVerifyTool) screenshotRoots() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if strings.TrimSpace(t.screenshotDir) != "" {
		return []string{t.screenshotDir}
	}
	return append([]string(nil), t.artifactRoots...)
}

// SetArtifactRoots configures the safe local roots used for generated screenshots.
func (t *FrontendVerifyTool) SetArtifactRoots(roots []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.artifactRoots = append([]string(nil), roots...)
}

func localFileURI(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
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
	r = completeFrontendVerifyResult(r)
	b, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("failed to encode result: %w", err)
	}
	return string(b), nil
}

func completeFrontendVerifyResult(r FrontendVerifyResult) FrontendVerifyResult {
	if strings.TrimSpace(r.VerificationStatus) == "" {
		if r.Match {
			r.VerificationStatus = "passed"
		} else {
			r.VerificationStatus = "failed"
		}
	}
	if strings.TrimSpace(r.TextReport) == "" {
		r.TextReport = frontendVerifyTextReport(r)
	}
	return r
}

func frontendVerifyTextReport(r FrontendVerifyResult) string {
	status := strings.TrimSpace(r.VerificationStatus)
	if status == "" {
		if r.Match {
			status = "passed"
		} else {
			status = "failed"
		}
	}
	target := strings.TrimSpace(r.TargetURL)
	if target == "" {
		target = "target URL"
	}

	feedback := compactFrontendVerifyText(r.Feedback)
	if feedback == "" {
		feedback = "verification completed"
	}
	if score := strings.TrimSpace(r.Score); score != "" {
		feedback = "score " + score + "; " + feedback
	}
	if suggestions := compactFrontendVerifyText(r.Suggestions); suggestions != "" && !r.Match {
		feedback += "; suggestions: " + suggestions
	}

	return truncateFrontendVerifyText(fmt.Sprintf("frontend_verify %s for %s: %s", status, target, feedback), 600)
}

func compactFrontendVerifyText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func truncateFrontendVerifyText(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
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
				"description": "Shell command to start the dev server, e.g. 'npm run dev'. Use 'auto' to detect npm, pnpm, yarn, or bun from package.json. Omit if server is already running.",
			},
			"auto_start": map[string]interface{}{
				"type":        "string",
				"description": "Set to 'true' to auto-detect and start a package.json dev/start/serve/preview script when command is omitted.",
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
