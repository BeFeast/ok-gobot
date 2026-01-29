package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
	"ok-gobot/internal/browser"
)

// BrowserTool provides browser automation capabilities
type BrowserTool struct {
	manager *browser.Manager
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
	return "Open and control a real Chrome browser on the user's computer. Use 'browser navigate <url>' to open websites, 'browser click <selector>' to click elements, 'browser fill <selector> <value>' to fill forms, 'browser screenshot' to take screenshots, 'browser text <selector>' to extract text. You CAN and SHOULD open websites for the user."
}

// Execute runs browser commands
func (b *BrowserTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: browser <start|stop|navigate|click|fill|screenshot|wait>")
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
	case "click":
		if len(args) < 2 {
			return "", fmt.Errorf("selector required")
		}
		return b.click(args[1])
	case "fill":
		if len(args) < 3 {
			return "", fmt.Errorf("selector and value required")
		}
		return b.fill(args[1], args[2])
	case "screenshot":
		return b.screenshot()
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

func (b *BrowserTool) start() (string, error) {
	if !b.manager.IsChromeInstalled() {
		return "", fmt.Errorf("Chrome not found. Please install Google Chrome.")
	}

	if err := b.manager.Start(); err != nil {
		return "", err
	}

	return "✅ Chrome started successfully", nil
}

func (b *BrowserTool) stop() (string, error) {
	b.manager.Stop()
	return "✅ Chrome stopped", nil
}

func (b *BrowserTool) navigate(url string) (string, error) {
	ctx, cancel, err := b.manager.NewTab()
	if err != nil {
		return "", err
	}
	defer cancel()

	ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
	defer cancelTimeout()

	if err := chromedp.Run(ctx, chromedp.Navigate(url)); err != nil {
		return "", fmt.Errorf("failed to navigate: %w", err)
	}

	return fmt.Sprintf("✅ Navigated to %s", url), nil
}

func (b *BrowserTool) click(selector string) (string, error) {
	ctx, cancel, err := b.manager.NewTab()
	if err != nil {
		return "", err
	}
	defer cancel()

	ctx, cancelTimeout := context.WithTimeout(ctx, 10*time.Second)
	defer cancelTimeout()

	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(selector),
		chromedp.Click(selector),
	); err != nil {
		return "", fmt.Errorf("failed to click: %w", err)
	}

	return fmt.Sprintf("✅ Clicked %s", selector), nil
}

func (b *BrowserTool) fill(selector, value string) (string, error) {
	ctx, cancel, err := b.manager.NewTab()
	if err != nil {
		return "", err
	}
	defer cancel()

	ctx, cancelTimeout := context.WithTimeout(ctx, 10*time.Second)
	defer cancelTimeout()

	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(selector),
		chromedp.SendKeys(selector, value),
	); err != nil {
		return "", fmt.Errorf("failed to fill: %w", err)
	}

	return fmt.Sprintf("✅ Filled %s", selector), nil
}

func (b *BrowserTool) screenshot() (string, error) {
	ctx, cancel, err := b.manager.NewTab()
	if err != nil {
		return "", err
	}
	defer cancel()

	var buf []byte
	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90)); err != nil {
		return "", fmt.Errorf("failed to take screenshot: %w", err)
	}

	// TODO: Save screenshot to file and return path
	return fmt.Sprintf("📸 Screenshot taken (%d bytes)", len(buf)), nil
}

func (b *BrowserTool) wait(selector string) (string, error) {
	ctx, cancel, err := b.manager.NewTab()
	if err != nil {
		return "", err
	}
	defer cancel()

	ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
	defer cancelTimeout()

	if err := chromedp.Run(ctx, chromedp.WaitVisible(selector)); err != nil {
		return "", fmt.Errorf("timeout waiting for element: %w", err)
	}

	return fmt.Sprintf("✅ Element %s is visible", selector), nil
}

func (b *BrowserTool) getText(selector string) (string, error) {
	ctx, cancel, err := b.manager.NewTab()
	if err != nil {
		return "", err
	}
	defer cancel()

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
				"description": "Browser command: navigate, click, fill, screenshot, text, wait, start, stop",
				"enum":        []string{"navigate", "click", "fill", "screenshot", "text", "wait", "start", "stop"},
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to navigate to (for 'navigate' command)",
			},
			"selector": map[string]interface{}{
				"type":        "string",
				"description": "CSS selector (for click, fill, text, wait commands)",
			},
			"value": map[string]interface{}{
				"type":        "string",
				"description": "Value to fill (for 'fill' command)",
			},
		},
		"required": []string{"command"},
	}
}

// IsRunning returns true if browser is running
func (b *BrowserTool) IsRunning() bool {
	// This is a simple check - in reality we'd track the context
	return b.manager != nil
}
