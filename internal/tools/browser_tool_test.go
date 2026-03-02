package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/browser"
)

func skipIfNoChrome(t *testing.T) {
	t.Helper()
	m := browser.NewManager("")
	if !m.IsChromeInstalled() {
		t.Skip("Chrome not installed, skipping integration test")
	}
}

func TestBrowserToolName(t *testing.T) {
	bt := NewBrowserTool("")
	if bt.Name() != "browser" {
		t.Errorf("Name() = %q, want %q", bt.Name(), "browser")
	}
}

func TestBrowserToolDescription(t *testing.T) {
	bt := NewBrowserTool("")
	desc := bt.Description()
	for _, keyword := range []string{"open", "navigate", "screenshot", "tabs", "focus", "close_tab"} {
		if !strings.Contains(desc, keyword) {
			t.Errorf("Description missing keyword %q", keyword)
		}
	}
}

func TestBrowserToolSchema(t *testing.T) {
	bt := NewBrowserTool("")
	schema := bt.GetSchema()

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing properties")
	}

	for _, field := range []string{"command", "url", "selector", "value", "tab_id", "headless"} {
		if _, ok := props[field]; !ok {
			t.Errorf("schema missing property %q", field)
		}
	}

	cmdProp := props["command"].(map[string]interface{})
	enumVals := cmdProp["enum"].([]string)
	expected := map[string]bool{
		"open": true, "navigate": true, "screenshot": true,
		"tabs": true, "focus": true, "close_tab": true,
		"click": true, "fill": true, "text": true,
		"wait": true, "stop": true,
	}
	for _, v := range enumVals {
		if !expected[v] {
			t.Errorf("unexpected enum value %q", v)
		}
		delete(expected, v)
	}
	for k := range expected {
		t.Errorf("missing enum value %q", k)
	}
}

func TestBrowserToolExecute_NoArgs(t *testing.T) {
	bt := NewBrowserTool("")
	_, err := bt.Execute(context.Background())
	if err == nil {
		t.Error("Execute with no args should return error")
	}
}

func TestBrowserToolExecute_UnknownCommand(t *testing.T) {
	bt := NewBrowserTool("")
	_, err := bt.Execute(context.Background(), "foobar")
	if err == nil {
		t.Error("Execute with unknown command should return error")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error = %q, want 'unknown command'", err.Error())
	}
}

func TestBrowserToolExecute_NavigateNoURL(t *testing.T) {
	bt := NewBrowserTool("")
	_, err := bt.Execute(context.Background(), "navigate")
	if err == nil {
		t.Error("navigate without URL should return error")
	}
}

func TestBrowserToolExecute_FocusNoID(t *testing.T) {
	bt := NewBrowserTool("")
	_, err := bt.Execute(context.Background(), "focus")
	if err == nil {
		t.Error("focus without tab_id should return error")
	}
}

func TestBrowserToolExecute_CloseTabNoID(t *testing.T) {
	bt := NewBrowserTool("")
	_, err := bt.Execute(context.Background(), "close_tab")
	if err == nil {
		t.Error("close_tab without tab_id should return error")
	}
}

func TestBrowserToolExecute_ClickNoSelector(t *testing.T) {
	bt := NewBrowserTool("")
	_, err := bt.Execute(context.Background(), "click")
	if err == nil {
		t.Error("click without selector should return error")
	}
}

func TestBrowserToolExecute_FillMissingArgs(t *testing.T) {
	bt := NewBrowserTool("")
	_, err := bt.Execute(context.Background(), "fill", "input")
	if err == nil {
		t.Error("fill with only selector should return error")
	}
}

func TestBrowserToolExecute_StopIdempotent(t *testing.T) {
	bt := NewBrowserTool("")
	result, err := bt.Execute(context.Background(), "stop")
	if err != nil {
		t.Errorf("stop should not error: %v", err)
	}
	if !strings.Contains(result, "stopped") {
		t.Errorf("result = %q, want 'stopped'", result)
	}
}

func TestBrowserToolIsRunning_Initial(t *testing.T) {
	bt := NewBrowserTool("")
	if bt.IsRunning() {
		t.Error("should not be running initially")
	}
}

// --- Integration tests (require Chrome) ---

func TestBrowserToolIntegration_OpenNavigateScreenshot(t *testing.T) {
	skipIfNoChrome(t)

	tmpDir := t.TempDir()
	bt := NewBrowserTool(filepath.Join(tmpDir, "profile"))
	// Override screenshot dir via manager
	bt.manager.ScreenshotDir = filepath.Join(tmpDir, "screenshots")
	ctx := context.Background()

	// Open in headless mode.
	result, err := bt.Execute(ctx, "open", "headless")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	if !strings.Contains(result, "headless") {
		t.Errorf("open result = %q, want headless indicator", result)
	}
	defer bt.Execute(ctx, "stop") //nolint:errcheck

	if !bt.IsRunning() {
		t.Error("should be running after open")
	}

	// Navigate.
	result, err = bt.Execute(ctx, "navigate", "https://example.com")
	if err != nil {
		t.Fatalf("navigate failed: %v", err)
	}
	if !strings.Contains(result, "example.com") {
		t.Errorf("navigate result = %q, want URL in output", result)
	}

	// Screenshot.
	result, err = bt.Execute(ctx, "screenshot")
	if err != nil {
		t.Fatalf("screenshot failed: %v", err)
	}
	if !strings.Contains(result, "screenshot") {
		t.Errorf("screenshot result = %q, want path", result)
	}
	// Extract path and verify file exists.
	path := strings.TrimPrefix(result, "Screenshot saved: ")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("screenshot file not found: %v", err)
	}
}

func TestBrowserToolIntegration_TabManagement(t *testing.T) {
	skipIfNoChrome(t)

	tmpDir := t.TempDir()
	bt := NewBrowserTool(filepath.Join(tmpDir, "profile"))
	bt.manager.ScreenshotDir = filepath.Join(tmpDir, "screenshots")
	ctx := context.Background()

	// Open browser.
	_, err := bt.Execute(ctx, "open", "headless")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	defer bt.Execute(ctx, "stop") //nolint:errcheck

	// List tabs — should be 1.
	result, err := bt.Execute(ctx, "tabs")
	if err != nil {
		t.Fatalf("tabs failed: %v", err)
	}
	if !strings.Contains(result, "(1)") {
		t.Errorf("tabs result = %q, want 1 tab", result)
	}

	// Navigate first tab.
	_, err = bt.Execute(ctx, "navigate", "https://example.com")
	if err != nil {
		t.Fatalf("navigate failed: %v", err)
	}

	// List tabs again to get active tab ID.
	result, err = bt.Execute(ctx, "tabs")
	if err != nil {
		t.Fatalf("tabs failed: %v", err)
	}
	// Extract the active tab ID (line starting with "> [tab_X]").
	var firstTabID string
	for _, line := range strings.Split(result, "\n") {
		if strings.HasPrefix(line, "> [") {
			start := strings.Index(line, "[") + 1
			end := strings.Index(line, "]")
			firstTabID = line[start:end]
			break
		}
	}
	if firstTabID == "" {
		t.Fatal("could not find active tab ID in tabs output")
	}

	// Open second tab by navigating with open.
	_, err = bt.Execute(ctx, "open", "https://example.org")
	if err != nil {
		// open on already-running browser navigates existing tab, not an error
		t.Logf("open with URL on running browser: %v", err)
	}

	// Focus back to first tab.
	result, err = bt.Execute(ctx, "focus", firstTabID)
	if err != nil {
		t.Fatalf("focus failed: %v", err)
	}
	if !strings.Contains(result, firstTabID) {
		t.Errorf("focus result = %q, want tab ID", result)
	}
}
