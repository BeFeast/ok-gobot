package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/chromedp"
	"ok-gobot/internal/browser"
	"ok-gobot/internal/logger"
)

// BrowserTool provides browser automation capabilities
type BrowserTool struct {
	manager   *browser.Manager
	activeCtx context.Context    // persistent tab context
	cancelTab context.CancelFunc // cancel for active tab
}

// NewBrowserTool creates a new browser tool
func NewBrowserTool(profilePath string) *BrowserTool {
	return &BrowserTool{
		manager: browser.NewManager(profilePath),
	}
}

func (b *BrowserTool) Name() string {
	return "browser"
}

func (b *BrowserTool) Description() string {
	return "Control a real Chrome browser. Use snapshot to get accessibility refs, then click/type with snapshot_id+ref. Commands: start, stop, navigate, snapshot, click, type, fill, screenshot, text, wait."
}

// Execute runs browser commands
func (b *BrowserTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: browser <start|stop|navigate|snapshot|click|type|fill|screenshot|wait|text>")
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

		// Manager may have restarted a dead browser instance; drop stale tab context.
		if b.cancelTab != nil {
			b.cancelTab()
			b.cancelTab = nil
		}
		b.activeCtx = nil
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
				"enum":        []string{"navigate", "snapshot", "click", "type", "fill", "screenshot", "text", "wait", "start", "stop"},
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to navigate to (for 'navigate' command)",
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
		},
		"required": []string{"command"},
	}
}

// IsRunning returns true if browser is running
func (b *BrowserTool) IsRunning() bool {
	return b.manager != nil && b.manager.IsRunning()
}
