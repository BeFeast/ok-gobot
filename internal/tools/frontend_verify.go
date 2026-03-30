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
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"ok-gobot/internal/ai"
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

	mu         sync.Mutex
	devServers map[string]*devServerProc // key: workDir
}

type devServerProc struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// FrontendVerifyResult is the structured response from the tool.
type FrontendVerifyResult struct {
	Match          bool   `json:"match"`
	Score          string `json:"score,omitempty"`
	Feedback       string `json:"feedback"`
	Suggestions    string `json:"suggestions,omitempty"`
	ScreenshotPath string `json:"screenshot_path"`
	ServerRunning  bool   `json:"server_running"`
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
	rawURL := params["url"]
	if rawURL == "" {
		return "", fmt.Errorf("url is required")
	}

	description := params["description"]
	command := params["command"]
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

	// Start dev server if a command was provided.
	if command != "" {
		if err := t.ensureDevServer(ctx, command, workDir); err != nil {
			return "", fmt.Errorf("failed to start dev server: %w", err)
		}
	}

	serverRunning := command != "" || t.isDevServerRunning(workDir)

	// Wait for the URL to become accessible.
	if err := t.waitForURL(ctx, rawURL, waitTimeout); err != nil {
		result := FrontendVerifyResult{
			Match:         false,
			Feedback:      fmt.Sprintf("URL %s did not become accessible within %s: %v", rawURL, waitTimeout, err),
			ServerRunning: serverRunning,
		}
		return marshalResult(result)
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
				Match:         false,
				Feedback:      fmt.Sprintf("screenshot failed: %v", err),
				ServerRunning: serverRunning,
			}
			continue
		}

		lastResult = FrontendVerifyResult{
			ScreenshotPath: path,
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
		if proc.cmd.ProcessState == nil {
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

	t.devServers[key] = &devServerProc{cmd: cmd, cancel: cancel}
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
	_, ok := t.devServers[workDir]
	return ok
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

	dir := t.screenshotDir
	if dir == "" {
		homeDir, _ := os.UserHomeDir()
		dir = filepath.Join(homeDir, ".ok-gobot", "screenshots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("failed to create screenshot dir: %w", err)
	}

	filename := fmt.Sprintf("frontend_verify_%s.png", time.Now().Format("20060102_150405"))
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return nil, "", fmt.Errorf("failed to save screenshot: %w", err)
	}

	return buf, path, nil
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
				"description": "Shell command to start the dev server, e.g. 'bun run dev' or 'npm start'. Omit if server is already running.",
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
