package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	if out.TargetURL != "http://127.0.0.1:19876" || out.VerificationStatus != "failed" || out.TextReport == "" {
		t.Fatalf("missing structured failure metadata: %+v", out)
	}
}

func TestFrontendVerifyTool_ScreenshotPathUsesArtifactRoot(t *testing.T) {
	root := t.TempDir()
	tool := NewFrontendVerifyTool("", "", "", nil)
	tool.SetArtifactRoots([]string{root})

	path, err := tool.newScreenshotPath(time.Date(2026, 5, 2, 12, 0, 0, 123, time.UTC))
	if err != nil {
		t.Fatalf("newScreenshotPath failed: %v", err)
	}
	if !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		t.Fatalf("screenshot path %q is outside configured root %q", path, root)
	}
	if filepath.Base(path) != "frontend_verify_20260502_120000_000000123.png" {
		t.Fatalf("unexpected screenshot filename: %q", path)
	}
}

func TestResolveFrontendDevCommandFallsBackFromMissingBunToNPM(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "package.json"), []byte(`{"scripts":{"dev":"vite --host 127.0.0.1"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "npm"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake npm: %v", err)
	}
	t.Setenv("PATH", binDir)

	command, err := resolveFrontendDevCommand("bun run dev", workDir)
	if err != nil {
		t.Fatalf("resolveFrontendDevCommand failed: %v", err)
	}
	if command != "npm run dev" {
		t.Fatalf("command = %q, want npm run dev", command)
	}
}

func TestFrontendVerifyTool_AutoCommandMissingPackageManager(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "package.json"), []byte(`{"scripts":{"dev":"vite --host 127.0.0.1"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	t.Setenv("PATH", t.TempDir())

	tool := NewFrontendVerifyTool("", "", "", nil)
	_, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"url":      "http://127.0.0.1:5173",
		"work_dir": workDir,
		"command":  "auto",
	})
	if err == nil {
		t.Fatal("expected missing package manager error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no supported frontend dev command available") || !strings.Contains(msg, "npm, pnpm, yarn, or bun") {
		t.Fatalf("error is not actionable: %v", err)
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
