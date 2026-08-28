package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactview "ok-gobot/internal/artifacts"
)

func TestFrontendVerifyCaptureCancelsRemoteDiscovery(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	releaseRequest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		select {
		case <-req.Context().Done():
		case <-releaseRequest:
		}
	}))
	defer server.Close()
	defer close(releaseRequest)

	chromePath, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	tool := NewFrontendVerifyTool(t.TempDir(), chromePath, server.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := tool.captureScreenshot(ctx, "https://example.com")
		done <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("remote discovery request did not start")
	}
	cancel()

	select {
	case captureErr := <-done:
		if !errors.Is(captureErr, context.Canceled) {
			t.Fatalf("captureScreenshot error = %v, want context.Canceled", captureErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("captureScreenshot did not return promptly after caller cancellation")
	}
}

// TestFrontendVerifyTool_Name verifies the tool name and schema.
func TestFrontendVerifyTool_Name(t *testing.T) {
	tool := NewFrontendVerifyTool("", "", "", nil)
	if tool.Name() != "frontend_verify" {
		t.Errorf("expected name 'frontend_verify', got %q", tool.Name())
	}

	schema := tool.GetSchema()
	if schema["type"] != "object" {
		t.Error("schema type should be object")
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing properties")
	}
	required := []string{"url"}
	for _, r := range required {
		if _, ok := props[r]; !ok {
			t.Errorf("schema missing required property %q", r)
		}
	}

	req, _ := schema["required"].([]string)
	found := false
	for _, r := range req {
		if r == "url" {
			found = true
		}
	}
	if !found {
		t.Error("'url' should be in required list")
	}
}

// TestFrontendVerifyTool_Description verifies the description is non-empty.
func TestFrontendVerifyTool_Description(t *testing.T) {
	tool := NewFrontendVerifyTool("", "", "", nil)
	if tool.Description() == "" {
		t.Error("description should not be empty")
	}
}

// TestFrontendVerifyTool_MissingURL returns an error when url is absent.
func TestFrontendVerifyTool_MissingURL(t *testing.T) {
	tool := NewFrontendVerifyTool("", "", "", nil)
	_, err := tool.ExecuteJSON(context.Background(), map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Errorf("expected url-required error, got %v", err)
	}
}

// TestFrontendVerifyTool_StopServer returns {"stopped":true} when stop_server=true.
func TestFrontendVerifyTool_StopServer(t *testing.T) {
	tool := NewFrontendVerifyTool("", "", "", nil)
	result, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"url":         "http://localhost:9999",
		"stop_server": "true",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if out["stopped"] != true {
		t.Errorf("expected stopped=true, got %v", out["stopped"])
	}
}

// TestFrontendVerifyTool_URLTimeout verifies fallback when URL never becomes accessible.
func TestFrontendVerifyTool_URLTimeout(t *testing.T) {
	tool := NewFrontendVerifyTool("", "", "", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := tool.ExecuteJSON(ctx, map[string]string{
		"url":          "http://127.0.0.1:19876", // unlikely to be in use
		"wait_timeout": "1",                      // 1 second timeout
		"max_retries":  "1",
	})
	if err != nil {
		t.Fatalf("unexpected error (should return fallback result): %v", err)
	}

	var out FrontendVerifyResult
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if out.Match {
		t.Error("match should be false when URL is unreachable")
	}
	if out.Feedback == "" {
		t.Error("feedback should describe the timeout")
	}
	if out.URL != "http://127.0.0.1:19876" {
		t.Fatalf("url = %q, want target URL", out.URL)
	}
	if out.Status != "failed" {
		t.Fatalf("status = %q, want failed", out.Status)
	}
	if !strings.Contains(out.TextReport, "frontend_verify failed") {
		t.Fatalf("text_report should summarize failure, got %q", out.TextReport)
	}
}

func TestFrontendVerifyTool_NextScreenshotPathUsesArtifactRoot(t *testing.T) {
	root := t.TempDir()
	tool := NewFrontendVerifyTool("", "", "", nil)
	tool.SetArtifactRoots([]string{root})

	path, err := tool.nextScreenshotPath(time.Date(2026, 5, 2, 12, 34, 56, 123, time.UTC))
	if err != nil {
		t.Fatalf("nextScreenshotPath: %v", err)
	}

	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		t.Fatalf("path %q is not below root %q (rel=%q err=%v)", path, root, rel, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create screenshot dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}
	safePath, ok := artifactview.SafeLocalPath(path, []string{root})
	if !ok {
		t.Fatalf("generated path is not accepted under safe root %q: %q", root, path)
	}
	if safePath != path {
		t.Fatalf("path = %q, safe path = %q", path, safePath)
	}
	if filepath.Base(filepath.Dir(path)) != "frontend_verify" {
		t.Fatalf("screenshot dir = %q, want frontend_verify under root", filepath.Dir(path))
	}
	if filepath.Ext(path) != ".png" {
		t.Fatalf("screenshot extension = %q, want .png", filepath.Ext(path))
	}
}

func TestFrontendVerifyTool_ResolveDevCommandFallsBackFromMissingBun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	tool := NewFrontendVerifyTool("", "", "", nil)
	tool.lookPath = func(name string) (string, error) {
		if name == "npm" {
			return "/usr/bin/npm", nil
		}
		return "", os.ErrNotExist
	}

	cmd, err := tool.resolveDevCommand("bun run dev", dir)
	if err != nil {
		t.Fatalf("resolveDevCommand: %v", err)
	}
	if cmd.Command != "npm run dev" || !cmd.Auto {
		t.Fatalf("resolved command = %+v, want npm fallback", cmd)
	}
}

func TestFrontendVerifyTool_ExecuteJSONMissingDevCommandClearFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	tool := NewFrontendVerifyTool("", "", "", nil)
	tool.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	_, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"url":        "http://127.0.0.1:19876",
		"work_dir":   dir,
		"auto_start": "true",
	})
	if err == nil {
		t.Fatal("expected clear dev command failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "failed to start dev server") || !strings.Contains(msg, "no supported frontend dev command available") || !strings.Contains(msg, "install npm, pnpm, yarn, or bun") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFrontendVerifyTool_ScreenshotNoAI verifies screenshot-only mode (no description/AI).
// This is an integration test: requires Chrome installed and a working headless environment.
// Set RUN_BROWSER_INTEGRATION_TESTS=1 to enable.
func TestFrontendVerifyTool_ScreenshotNoAI(t *testing.T) {
	if os.Getenv("RUN_BROWSER_INTEGRATION_TESTS") != "1" {
		t.Skip("set RUN_BROWSER_INTEGRATION_TESTS=1 to run browser integration tests")
	}

	// Check that Chrome is available before attempting the browser test.
	probe := NewFrontendVerifyTool("", "", "", nil)
	if !probe.manager.IsChromeInstalled() {
		t.Skip("Chrome not installed; skipping browser screenshot test")
	}

	// Serve a simple HTML page locally.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><h1>Hello</h1></body></html>`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	tool := NewFrontendVerifyTool("", "", "", nil)
	tool.screenshotDir = dir

	result, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"url":          srv.URL,
		"wait_timeout": "5",
		"max_retries":  "1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out FrontendVerifyResult
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if !out.Match {
		t.Errorf("expected match=true in screenshot-only mode, got feedback: %s", out.Feedback)
	}
	if out.ScreenshotPath == "" {
		t.Error("screenshot_path should be set")
	}
}

// TestParseLLMCompareResult tests the JSON extraction from LLM responses.
func TestParseLLMCompareResult(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantMatch bool
		wantErr   bool
	}{
		{
			name:      "direct JSON match",
			input:     `{"match":true,"score":"8/10","feedback":"looks good","suggestions":""}`,
			wantMatch: true,
		},
		{
			name:      "JSON with preamble",
			input:     "Sure! Here's my analysis:\n\n{\"match\":false,\"score\":\"4/10\",\"feedback\":\"button missing\",\"suggestions\":\"add a button\"}",
			wantMatch: false,
		},
		{
			name:    "no JSON",
			input:   "I cannot analyze this image",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseLLMCompareResult(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Match != tt.wantMatch {
				t.Errorf("match: got %v, want %v", result.Match, tt.wantMatch)
			}
		})
	}
}

// TestFrontendVerifyTool_WaitForURL_Success verifies waitForURL succeeds for an accessible URL.
func TestFrontendVerifyTool_WaitForURL_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tool := NewFrontendVerifyTool("", "", "", nil)
	ctx := context.Background()
	err := tool.waitForURL(ctx, srv.URL, 5*time.Second)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// TestFrontendVerifyTool_WaitForURL_Timeout verifies waitForURL times out properly.
func TestFrontendVerifyTool_WaitForURL_Timeout(t *testing.T) {
	tool := NewFrontendVerifyTool("", "", "", nil)
	ctx := context.Background()
	err := tool.waitForURL(ctx, "http://127.0.0.1:19877", 500*time.Millisecond)
	if err == nil {
		t.Error("expected timeout error")
	}
}

// TestFrontendVerifyTool_RegistrationInRegistry verifies the tool is registered.
func TestFrontendVerifyTool_RegistrationInRegistry(t *testing.T) {
	dir := t.TempDir()
	registry, err := LoadFromConfigWithOptions(dir, &ToolsConfig{})
	if err != nil {
		t.Fatalf("LoadFromConfigWithOptions: %v", err)
	}

	tool, ok := registry.Get("frontend_verify")
	if !ok {
		t.Fatal("frontend_verify tool not registered in registry")
	}
	if tool.Name() != "frontend_verify" {
		t.Errorf("unexpected tool name: %q", tool.Name())
	}
}

// TestFrontendVerifyTool_InDangerousFamily verifies the tool is in the browser dangerous family.
func TestFrontendVerifyTool_InDangerousFamily(t *testing.T) {
	family, ok := DangerousToolFamily("frontend_verify")
	if !ok {
		t.Error("frontend_verify should be in a dangerous tool family")
	}
	if family != "browser" {
		t.Errorf("expected browser family, got %q", family)
	}
}
