package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"ok-gobot/internal/browser"
	"ok-gobot/internal/logger"
)

// BrowserTool provides browser automation capabilities
type BrowserTool struct {
	manager *browser.Manager

	mu      sync.Mutex
	tabs    map[string]*tabEntry // targetID -> entry
	active  string               // targetID of the focused tab
	profile string               // current profile name

	screenshotDir string
}

type tabEntry struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewBrowserTool creates a new browser tool
func NewBrowserTool(profilePath, chromePath string) *BrowserTool {
	mgr := browser.NewManager(profilePath)
	if chromePath != "" {
		mgr.ChromePath = chromePath
	}
	return &BrowserTool{
		manager: mgr,
		tabs:    make(map[string]*tabEntry),
		profile: browser.ProfileOpenclaw,
	}
}

func (b *BrowserTool) Name() string {
	return "browser"
}

func (b *BrowserTool) Description() string {
	return "Control a real Chrome browser. Commands: open [url], navigate <url>, screenshot, snapshot, click, type, fill, tabs, focus <target_id>, close [target_id], stop."
}

// Execute runs browser commands
func (b *BrowserTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: browser <open|navigate|snapshot|click|type|fill|screenshot|tabs|focus|close|stop>")
	}

	command := args[0]

	switch command {
	case "open", "start":
		url := ""
		if len(args) >= 2 {
			url = args[1]
		}
		return b.open(url)
	case "stop":
		return b.stop()
	case "navigate":
		if len(args) < 2 {
			return "", fmt.Errorf("URL required")
		}
		return b.navigate(args[1])
	case "snapshot":
		return b.snapshot()
	case "click":
		return b.clickDispatch(args[1:])
	case "type", "fill":
		return b.typeDispatch(args[1:])
	case "screenshot":
		return b.screenshotCmd()
	case "wait":
		if len(args) < 2 {
			return "", fmt.Errorf("selector required")
		}
		return b.wait(args[1])
	case "text":
		if len(args) < 2 {
			return "", fmt.Errorf("selector required")
		}
		return b.getText(args[1])
	case "tabs":
		return b.listTabs()
	case "focus":
		if len(args) < 2 {
			return "", fmt.Errorf("target_id required")
		}
		return b.focusTab(args[1])
	case "close":
		targetID := ""
		if len(args) >= 2 {
			targetID = args[1]
		}
		return b.closeTab(targetID)
	default:
		return "", fmt.Errorf("unknown command: %s", command)
	}
}

// ExecuteJSON runs a browser command with structured JSON parameters.
func (b *BrowserTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	command := params["command"]
	if command == "" {
		return "", fmt.Errorf("command is required")
	}

	switch command {
	case "open", "start":
		return b.open(params["url"])
	case "stop":
		return b.stop()
	case "navigate":
		url := params["url"]
		if url == "" {
			return "", fmt.Errorf("url is required for navigate")
		}
		return b.navigate(url)
	case "snapshot":
		return b.snapshot()
	case "click":
		snapshotID := params["snapshot_id"]
		ref := params["ref"]
		selector := params["selector"]
		if snapshotID != "" && ref != "" {
			return b.clickByRef(snapshotID, ref)
		}
		if selector != "" {
			return b.clickCSS(selector)
		}
		return "", fmt.Errorf("click requires snapshot_id+ref or selector")
	case "type", "fill":
		value := params["value"]
		if value == "" {
			return "", fmt.Errorf("value is required for %s", command)
		}
		snapshotID := params["snapshot_id"]
		ref := params["ref"]
		selector := params["selector"]
		if snapshotID != "" && ref != "" {
			return b.typeByRef(snapshotID, ref, value)
		}
		if selector != "" {
			return b.fillCSS(selector, value)
		}
		return "", fmt.Errorf("%s requires snapshot_id+ref or selector", command)
	case "screenshot":
		return b.screenshotCmd()
	case "wait":
		selector := params["selector"]
		if selector == "" {
			return "", fmt.Errorf("selector is required for wait")
		}
		return b.wait(selector)
	case "text":
		selector := params["selector"]
		if selector == "" {
			return "", fmt.Errorf("selector is required for text")
		}
		return b.getText(selector)
	case "tabs":
		return b.listTabs()
	case "focus":
		targetID := params["target_id"]
		if targetID == "" {
			return "", fmt.Errorf("target_id is required for focus")
		}
		return b.focusTab(targetID)
	case "close":
		return b.closeTab(params["target_id"])
	default:
		return "", fmt.Errorf("unknown command: %s", command)
	}
}

func (b *BrowserTool) clickDispatch(args []string) (string, error) {
	switch len(args) {
	case 1:
		return b.clickCSS(args[0])
	case 2:
		return b.clickByRef(args[0], args[1])
	default:
		return "", fmt.Errorf("usage: browser click <selector> OR browser click <snapshot_id> <ref>")
	}
}

func (b *BrowserTool) typeDispatch(args []string) (string, error) {
	switch len(args) {
	case 2:
		return b.fillCSS(args[0], args[1])
	case 3:
		return b.typeByRef(args[0], args[1], args[2])
	default:
		return "", fmt.Errorf("usage: browser type <selector> <value> OR browser type <snapshot_id> <ref> <value>")
	}
}

// ensureRunning auto-starts browser and returns the active tab context.
func (b *BrowserTool) ensureRunning() (context.Context, error) {
	if !b.manager.IsRunning() {
		if !b.manager.IsChromeInstalled() {
			return nil, fmt.Errorf("Chrome not found. Please install Google Chrome.")
		}
		logger.Debugf("Browser: auto-starting Chrome")
		if err := b.manager.Start(); err != nil {
			return nil, fmt.Errorf("failed to start browser: %w", err)
		}
		// Browser restarted — drop stale tabs.
		b.mu.Lock()
		b.clearTabsLocked()
		b.mu.Unlock()
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.active != "" {
		if entry, ok := b.tabs[b.active]; ok {
			return entry.ctx, nil
		}
		// Stale active reference.
		b.active = ""
	}

	// Create an initial tab.
	ctx, cancel, err := b.manager.NewTab()
	if err != nil {
		return nil, err
	}

	targetID := b.targetIDFromCtx(ctx)
	b.tabs[targetID] = &tabEntry{ctx: ctx, cancel: cancel}
	b.active = targetID
	return ctx, nil
}

func (b *BrowserTool) open(url string) (string, error) {
	if !b.manager.IsChromeInstalled() {
		return "", fmt.Errorf("Chrome not found. Please install Google Chrome.")
	}

	if err := b.manager.Start(); err != nil {
		return "", err
	}

	// Reset stale tab state after (re-)start.
	b.mu.Lock()
	b.clearTabsLocked()
	b.mu.Unlock()

	if url != "" {
		return b.navigate(url)
	}
	return "Browser opened", nil
}

func (b *BrowserTool) stop() (string, error) {
	b.mu.Lock()
	b.clearTabsLocked()
	b.mu.Unlock()

	b.manager.Stop()
	return "Browser stopped", nil
}

// NOTE: No context.WithTimeout — chromedp treats context cancellation as "close tab".
// The persistent activeCtx must never be cancelled between tool calls.

// validateBrowserURL blocks dangerous URL schemes and private/loopback destinations.
func validateBrowserURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "file" {
		return fmt.Errorf("file:// URLs are not allowed in the browser tool")
	}
	if scheme != "http" && scheme != "https" && scheme != "" {
		return fmt.Errorf("unsupported URL scheme: %s", scheme)
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "0.0.0.0" || hostname == "::1" || hostname == "[::1]" {
		return fmt.Errorf("navigation to localhost/loopback is not allowed")
	}
	if strings.HasSuffix(hostname, ".internal") || strings.HasSuffix(hostname, ".local") {
		return fmt.Errorf("navigation to internal/local hostnames is not allowed")
	}
	return nil
}

func (b *BrowserTool) navigate(navURL string) (string, error) {
	if err := validateBrowserURL(navURL); err != nil {
		return "", err
	}

	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}

	if err := chromedp.Run(ctx, chromedp.Navigate(navURL)); err != nil {
		return "", fmt.Errorf("failed to navigate: %w", err)
	}

	// Wait briefly for page to settle
	if err := chromedp.Run(ctx, chromedp.WaitReady("body")); err != nil {
		logger.Debugf("Browser: WaitReady after navigate: %v", err)
	}

	return fmt.Sprintf("Navigated to %s", navURL), nil
}

func (b *BrowserTool) snapshot() (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}

	snapshotID, nodes, err := b.manager.Snapshot(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to create accessibility snapshot: %w", err)
	}

	payload, err := json.Marshal(map[string]interface{}{
		"snapshot_id": snapshotID,
		"nodes":       nodes,
	})
	if err != nil {
		return "", fmt.Errorf("failed to encode snapshot response: %w", err)
	}

	return string(payload), nil
}

func (b *BrowserTool) clickByRef(snapshotID, ref string) (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}
	if err := b.manager.ClickByRef(ctx, snapshotID, ref); err != nil {
		return "", fmt.Errorf("click failed: %w", err)
	}
	return fmt.Sprintf("Clicked ref %s (snapshot %s)", ref, snapshotID), nil
}

func (b *BrowserTool) clickCSS(selector string) (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(selector),
		chromedp.Click(selector),
	); err != nil {
		return "", fmt.Errorf("failed to click: %w", err)
	}
	return fmt.Sprintf("Clicked %s", selector), nil
}

func (b *BrowserTool) typeByRef(snapshotID, ref, value string) (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}
	if err := b.manager.TypeByRef(ctx, snapshotID, ref, value); err != nil {
		return "", fmt.Errorf("type failed: %w", err)
	}
	return fmt.Sprintf("Typed into ref %s (snapshot %s)", ref, snapshotID), nil
}

func (b *BrowserTool) fillCSS(selector, value string) (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(selector),
		chromedp.SendKeys(selector, value),
	); err != nil {
		return "", fmt.Errorf("failed to fill: %w", err)
	}
	return fmt.Sprintf("Filled %s", selector), nil
}

func (b *BrowserTool) screenshotCmd() (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}

	var buf []byte
	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90)); err != nil {
		return "", fmt.Errorf("failed to take screenshot: %w", err)
	}

	dir := b.screenshotDir
	if dir == "" {
		homeDir, _ := os.UserHomeDir()
		dir = filepath.Join(homeDir, ".ok-gobot", "screenshots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create screenshot directory: %w", err)
	}

	filename := fmt.Sprintf("screenshot_%s.png", time.Now().Format("20060102_150405"))
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", fmt.Errorf("failed to save screenshot: %w", err)
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"path":       path,
		"size_bytes": len(buf),
		"base64":     base64.StdEncoding.EncodeToString(buf),
	})
	return string(payload), nil
}

func (b *BrowserTool) wait(selector string) (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}

	if err := chromedp.Run(ctx, chromedp.WaitVisible(selector)); err != nil {
		return "", fmt.Errorf("timeout waiting for element: %w", err)
	}

	return fmt.Sprintf("Element %s is visible", selector), nil
}

func (b *BrowserTool) getText(selector string) (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}

	var text string
	if err := chromedp.Run(ctx, chromedp.Text(selector, &text)); err != nil {
		return "", fmt.Errorf("failed to get text: %w", err)
	}

	return text, nil
}

// --- Tab management ---

func (b *BrowserTool) listTabs() (string, error) {
	if !b.manager.IsRunning() {
		return "", fmt.Errorf("browser is not running; use 'open' first")
	}

	tabs, err := b.manager.ListTabs(b.profile)
	if err != nil {
		return "", err
	}

	b.mu.Lock()
	activeID := b.active
	b.mu.Unlock()

	type tabOut struct {
		TargetID string `json:"target_id"`
		Title    string `json:"title"`
		URL      string `json:"url"`
		Active   bool   `json:"active"`
	}
	out := make([]tabOut, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, tabOut{
			TargetID: t.TargetID,
			Title:    t.Title,
			URL:      t.URL,
			Active:   t.TargetID == activeID,
		})
	}

	payload, _ := json.Marshal(out)
	return string(payload), nil
}

func (b *BrowserTool) focusTab(targetID string) (string, error) {
	if !b.manager.IsRunning() {
		return "", fmt.Errorf("browser is not running; use 'open' first")
	}

	if err := b.manager.FocusTab(b.profile, targetID); err != nil {
		return "", fmt.Errorf("failed to focus tab: %w", err)
	}

	b.mu.Lock()
	// If we don't already have a context for this tab, create one.
	if _, ok := b.tabs[targetID]; !ok {
		ctx, cancel, err := b.manager.ContextForTarget(b.profile, targetID)
		if err != nil {
			b.mu.Unlock()
			return "", fmt.Errorf("failed to attach to tab: %w", err)
		}
		b.tabs[targetID] = &tabEntry{ctx: ctx, cancel: cancel}
	}
	b.active = targetID
	b.mu.Unlock()

	return fmt.Sprintf("Focused tab %s", targetID), nil
}

func (b *BrowserTool) closeTab(targetID string) (string, error) {
	if !b.manager.IsRunning() {
		return "", fmt.Errorf("browser is not running; use 'open' first")
	}

	b.mu.Lock()
	if targetID == "" {
		targetID = b.active
	}
	b.mu.Unlock()

	if targetID == "" {
		return "", fmt.Errorf("no active tab to close; specify a target_id")
	}

	if err := b.manager.CloseTab(b.profile, targetID); err != nil {
		return "", fmt.Errorf("failed to close tab: %w", err)
	}

	b.mu.Lock()
	if entry, ok := b.tabs[targetID]; ok {
		entry.cancel()
		delete(b.tabs, targetID)
	}
	if b.active == targetID {
		b.active = ""
	}
	b.mu.Unlock()

	return fmt.Sprintf("Closed tab %s", targetID), nil
}

// clearTabsLocked cancels all tab contexts and resets state. Must hold b.mu.
func (b *BrowserTool) clearTabsLocked() {
	for _, entry := range b.tabs {
		entry.cancel()
	}
	b.tabs = make(map[string]*tabEntry)
	b.active = ""
}

func (b *BrowserTool) targetIDFromCtx(ctx context.Context) string {
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil {
		return ""
	}
	return string(c.Target.TargetID)
}

// GetSchema returns the JSON Schema for browser tool parameters
func (b *BrowserTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Browser command to execute",
				"enum":        []string{"open", "navigate", "snapshot", "click", "type", "fill", "screenshot", "text", "wait", "tabs", "focus", "close", "stop"},
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to navigate to (for 'open' or 'navigate')",
			},
			"snapshot_id": map[string]interface{}{
				"type":        "string",
				"description": "Snapshot ID returned by browser snapshot (for ref-based actions)",
			},
			"ref": map[string]interface{}{
				"type":        "string",
				"description": "Node ref from snapshot tree",
			},
			"selector": map[string]interface{}{
				"type":        "string",
				"description": "CSS selector (for click/type/text/wait)",
			},
			"value": map[string]interface{}{
				"type":        "string",
				"description": "Value to type (for type/fill)",
			},
			"target_id": map[string]interface{}{
				"type":        "string",
				"description": "Tab target ID (for focus/close)",
			},
		},
		"required": []string{"command"},
	}
}

// IsRunning returns true if browser is running
func (b *BrowserTool) IsRunning() bool {
	return b.manager != nil && b.manager.IsRunning()
}
