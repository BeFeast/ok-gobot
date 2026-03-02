package tools

import (
	"context"
	"fmt"

	"github.com/chromedp/chromedp"
	"ok-gobot/internal/browser"
	"ok-gobot/internal/logger"
)

// BrowserTool provides browser automation capabilities
type BrowserTool struct {
	manager   *browser.Manager
	snapshots *browser.SnapshotStore
	activeCtx context.Context    // persistent tab context
	cancelTab context.CancelFunc // cancel for active tab
}

// NewBrowserTool creates a new browser tool
func NewBrowserTool(profilePath string) *BrowserTool {
	return &BrowserTool{
		manager:   browser.NewManager(profilePath),
		snapshots: browser.NewSnapshotStore(0), // default TTL
	}
}

func (b *BrowserTool) Name() string {
	return "browser"
}

func (b *BrowserTool) Description() string {
	return `Control a real Chrome browser. Workflow: call "snapshot" to get a snapshot_id and element refs, then use "click", "fill", or "focus" with snapshot_id+ref to interact. Commands: start, stop, navigate <url>, snapshot, click (ref or selector), fill (ref or selector + value), focus (ref), screenshot, text <selector>, wait <selector>.`
}

// Execute runs browser commands
func (b *BrowserTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: browser <start|stop|navigate|snapshot|click|fill|focus|screenshot|wait|text>")
	}

	command := args[0]

	switch command {
	case "start":
		return b.start()
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
	case "fill":
		return b.fillDispatch(args[1:])
	case "focus":
		return b.focusDispatch(args[1:])
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
	default:
		return "", fmt.Errorf("unknown command: %s", command)
	}
}

// ExecuteJSON runs a browser command with structured JSON parameters.
// This is the preferred entry point from the tool-calling agent.
func (b *BrowserTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	command := params["command"]
	if command == "" {
		return "", fmt.Errorf("command is required")
	}

	switch command {
	case "start":
		return b.start()
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
		sid := params["snapshot_id"]
		ref := params["ref"]
		sel := params["selector"]
		if sid != "" && ref != "" {
			return b.clickByRef(sid, ref)
		}
		if sel != "" {
			return b.clickCSS(sel)
		}
		return "", fmt.Errorf("click requires snapshot_id+ref or selector")
	case "fill":
		val := params["value"]
		if val == "" {
			return "", fmt.Errorf("value is required for fill")
		}
		sid := params["snapshot_id"]
		ref := params["ref"]
		sel := params["selector"]
		if sid != "" && ref != "" {
			return b.fillByRef(sid, ref, val)
		}
		if sel != "" {
			return b.fillCSS(sel, val)
		}
		return "", fmt.Errorf("fill requires snapshot_id+ref or selector")
	case "focus":
		sid := params["snapshot_id"]
		ref := params["ref"]
		if sid == "" || ref == "" {
			return "", fmt.Errorf("focus requires snapshot_id and ref")
		}
		return b.focusByRef(sid, ref)
	case "screenshot":
		return b.screenshotCmd()
	case "wait":
		sel := params["selector"]
		if sel == "" {
			return "", fmt.Errorf("selector is required for wait")
		}
		return b.wait(sel)
	case "text":
		sel := params["selector"]
		if sel == "" {
			return "", fmt.Errorf("selector is required for text")
		}
		return b.getText(sel)
	default:
		return "", fmt.Errorf("unknown command: %s", command)
	}
}

// ensureRunning auto-starts browser and returns the active tab context
func (b *BrowserTool) ensureRunning() (context.Context, error) {
	// Start browser if needed
	if !b.manager.IsRunning() {
		if !b.manager.IsChromeInstalled() {
			return nil, fmt.Errorf("Chrome not found. Please install Google Chrome.")
		}
		logger.Debugf("Browser: auto-starting Chrome")
		if err := b.manager.Start(); err != nil {
			return nil, fmt.Errorf("failed to start browser: %w", err)
		}
	}

	// Create persistent tab if needed
	if b.activeCtx == nil {
		logger.Debugf("Browser: creating persistent tab")
		ctx, cancel, err := b.manager.NewTab()
		if err != nil {
			return nil, err
		}
		b.activeCtx = ctx
		b.cancelTab = cancel
	}

	return b.activeCtx, nil
}

func (b *BrowserTool) start() (string, error) {
	if !b.manager.IsChromeInstalled() {
		return "", fmt.Errorf("Chrome not found. Please install Google Chrome.")
	}

	if err := b.manager.Start(); err != nil {
		return "", err
	}

	return "Chrome started successfully", nil
}

func (b *BrowserTool) stop() (string, error) {
	if b.cancelTab != nil {
		b.cancelTab()
		b.activeCtx = nil
		b.cancelTab = nil
	}
	b.manager.Stop()
	return "Chrome stopped", nil
}

// NOTE: No context.WithTimeout — chromedp treats context cancellation as "close tab".
// The persistent activeCtx must never be cancelled between tool calls.

func (b *BrowserTool) navigate(url string) (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}

	if err := chromedp.Run(ctx, chromedp.Navigate(url)); err != nil {
		return "", fmt.Errorf("failed to navigate: %w", err)
	}

	// Wait briefly for page to settle
	if err := chromedp.Run(ctx, chromedp.WaitReady("body")); err != nil {
		logger.Debugf("Browser: WaitReady after navigate: %v", err)
	}

	return fmt.Sprintf("Navigated to %s", url), nil
}

func (b *BrowserTool) snapshot() (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}

	snap, err := browser.TakeSnapshot(ctx, b.snapshots)
	if err != nil {
		return "", fmt.Errorf("snapshot failed: %w", err)
	}

	return browser.FormatSnapshot(snap), nil
}

// --- click dispatchers ---

func (b *BrowserTool) clickDispatch(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("click requires a selector or snapshot_id+ref")
	}
	// Two-arg form: snapshot_id ref
	if len(args) >= 2 {
		return b.clickByRef(args[0], args[1])
	}
	// Single arg: CSS selector (legacy)
	return b.clickCSS(args[0])
}

func (b *BrowserTool) clickByRef(snapshotID, ref string) (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}
	if err := browser.ClickByRef(ctx, b.snapshots, snapshotID, ref); err != nil {
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

// --- fill dispatchers ---

func (b *BrowserTool) fillDispatch(args []string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("fill requires (selector value) or (snapshot_id ref value)")
	}
	// Three-arg form: snapshot_id ref value
	if len(args) >= 3 {
		return b.fillByRef(args[0], args[1], args[2])
	}
	// Two-arg form: selector value (legacy)
	return b.fillCSS(args[0], args[1])
}

func (b *BrowserTool) fillByRef(snapshotID, ref, value string) (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}
	if err := browser.TypeByRef(ctx, b.snapshots, snapshotID, ref, value); err != nil {
		return "", fmt.Errorf("fill failed: %w", err)
	}
	return fmt.Sprintf("Filled ref %s (snapshot %s)", ref, snapshotID), nil
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

// --- focus dispatcher ---

func (b *BrowserTool) focusDispatch(args []string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("focus requires snapshot_id and ref")
	}
	return b.focusByRef(args[0], args[1])
}

func (b *BrowserTool) focusByRef(snapshotID, ref string) (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}
	if err := browser.FocusByRef(ctx, b.snapshots, snapshotID, ref); err != nil {
		return "", fmt.Errorf("focus failed: %w", err)
	}
	return fmt.Sprintf("Focused ref %s (snapshot %s)", ref, snapshotID), nil
}

// --- other commands ---

func (b *BrowserTool) screenshotCmd() (string, error) {
	ctx, err := b.ensureRunning()
	if err != nil {
		return "", err
	}

	var buf []byte
	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90)); err != nil {
		return "", fmt.Errorf("failed to take screenshot: %w", err)
	}

	// TODO: Save screenshot to file and return path
	return fmt.Sprintf("Screenshot taken (%d bytes)", len(buf)), nil
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

// GetSchema returns the JSON Schema for browser tool parameters
func (b *BrowserTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Browser command to execute",
				"enum":        []string{"navigate", "snapshot", "click", "fill", "focus", "screenshot", "text", "wait", "start", "stop"},
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to navigate to (for 'navigate' command)",
			},
			"snapshot_id": map[string]interface{}{
				"type":        "string",
				"description": "Snapshot ID returned by 'snapshot' command (for click, fill, focus by ref)",
			},
			"ref": map[string]interface{}{
				"type":        "string",
				"description": "Element ref from snapshot (e.g. 'e1', 'e2') — used with snapshot_id for click, fill, focus",
			},
			"selector": map[string]interface{}{
				"type":        "string",
				"description": "CSS selector (fallback for click, fill, text, wait when not using snapshot refs)",
			},
			"value": map[string]interface{}{
				"type":        "string",
				"description": "Value to type (for 'fill' command)",
			},
		},
		"required": []string{"command"},
	}
}

// IsRunning returns true if browser is running
func (b *BrowserTool) IsRunning() bool {
	return b.manager != nil && b.manager.IsRunning()
}
