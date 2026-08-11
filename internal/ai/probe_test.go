package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeOpenAICompat_AuthFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "openai",
		APIKey:  "bad-key",
		BaseURL: srv.URL,
		Model:   "gpt-4o",
	}, DroidConfig{})

	if res.Status != ProbeAuthFailed {
		t.Fatalf("expected ProbeAuthFailed, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeOpenAICompat_QuotaFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":{"message":"insufficient_quota"}}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "openai",
		APIKey:  "key",
		BaseURL: srv.URL,
		Model:   "gpt-4o",
	}, DroidConfig{})

	if res.Status != ProbeQuotaFailed || res.FailureKind != BackendFailureQuota {
		t.Fatalf("expected quota failure, got status=%d kind=%s detail=%s", res.Status, res.FailureKind, res.Detail)
	}
}

func TestProbeOpenAICompat_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "openai",
		APIKey:  "key",
		BaseURL: srv.URL,
		Model:   "gpt-4o",
	}, DroidConfig{})

	if res.Status != ProbeRateLimited || res.FailureKind != BackendFailureRateLimit {
		t.Fatalf("expected rate limit, got status=%d kind=%s detail=%s", res.Status, res.FailureKind, res.Detail)
	}
}

func TestProbeOpenAICompat_EndpointUnreachable(t *testing.T) {
	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "openai",
		APIKey:  "key",
		BaseURL: "http://127.0.0.1:1", // nothing listens here
		Model:   "gpt-4o",
	}, DroidConfig{})

	if res.Status != ProbeEndpointUnreachable {
		t.Fatalf("expected ProbeEndpointUnreachable, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeOpenAICompat_ModelNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}{
			Data: []struct {
				ID string `json:"id"`
			}{
				{ID: "gpt-4o"},
				{ID: "gpt-4o-mini"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "openai",
		APIKey:  "good-key",
		BaseURL: srv.URL,
		Model:   "nonexistent-model",
	}, DroidConfig{})

	if res.Status != ProbeModelNotFound {
		t.Fatalf("expected ProbeModelNotFound, got %d (detail: %s)", res.Status, res.Detail)
	}
	if len(res.AvailableModels) != 2 {
		t.Fatalf("expected 2 available models, got %d", len(res.AvailableModels))
	}
}

func TestProbeOpenAICompat_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}{
			Data: []struct {
				ID string `json:"id"`
			}{
				{ID: "gpt-4o"},
				{ID: "gpt-4o-mini"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "openai",
		APIKey:  "good-key",
		BaseURL: srv.URL,
		Model:   "gpt-4o",
	}, DroidConfig{})

	if res.Status != ProbeOK {
		t.Fatalf("expected ProbeOK, got %d (detail: %s)", res.Status, res.Detail)
	}
	if res.Latency == 0 {
		t.Fatal("expected non-zero latency")
	}
}

func TestProbeAnthropic_AuthFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "anthropic",
		APIKey:  "bad-key",
		BaseURL: srv.URL,
		Model:   "claude-sonnet-4-5-20250929",
	}, DroidConfig{})

	if res.Status != ProbeAuthFailed {
		t.Fatalf("expected ProbeAuthFailed, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeAnthropic_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-5-20250929"},{"id":"claude-opus-4-5-20251101"}]}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "anthropic",
		APIKey:  "good-key",
		BaseURL: srv.URL,
		Model:   "claude-sonnet-4-5-20250929",
	}, DroidConfig{})

	if res.Status != ProbeOK {
		t.Fatalf("expected ProbeOK, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeAnthropic_ModelNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a valid /v1/models response that does not contain the requested model.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-5-20250929"},{"id":"claude-haiku-3-5-20241022"}]}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "anthropic",
		APIKey:  "good-key",
		BaseURL: srv.URL,
		Model:   "claude-nonexistent",
	}, DroidConfig{})

	if res.Status != ProbeModelNotFound {
		t.Fatalf("expected ProbeModelNotFound, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeAnthropic_EndpointUnreachable(t *testing.T) {
	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "anthropic",
		APIKey:  "key",
		BaseURL: "http://127.0.0.1:1",
		Model:   "claude-sonnet-4-5-20250929",
	}, DroidConfig{})

	if res.Status != ProbeEndpointUnreachable {
		t.Fatalf("expected ProbeEndpointUnreachable, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeDroid_BinaryNotFound(t *testing.T) {
	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:  "droid",
		Model: "glm-5",
	}, DroidConfig{BinaryPath: "/nonexistent/droid-binary"})

	if res.Status != ProbeEndpointUnreachable {
		t.Fatalf("expected ProbeEndpointUnreachable, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeDroid_ModelNotFound(t *testing.T) {
	binary := writeProbeBinary(t, "droid")
	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:  "droid",
		Model: "nonexistent-model",
	}, DroidConfig{BinaryPath: binary})

	if res.Status != ProbeModelNotFound {
		t.Fatalf("expected ProbeModelNotFound, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeDroid_DetectsOpenCodeBackendWithoutDroidCatalog(t *testing.T) {
	binary := writeProbeBinary(t, "opencode")
	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:  "droid",
		Model: "anthropic/claude-sonnet-4-5",
	}, DroidConfig{BinaryPath: binary})

	if res.Status != ProbeOK {
		t.Fatalf("expected ProbeOK, got %d (detail: %s)", res.Status, res.Detail)
	}
	if res.Backend != "opencode" {
		t.Fatalf("backend=%q, want opencode", res.Backend)
	}
}

func TestProbeCustom_NoBaseURL(t *testing.T) {
	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:   "custom",
		APIKey: "key",
		Model:  "some-model",
	}, DroidConfig{})

	if res.Status != ProbeSkipped {
		t.Fatalf("expected ProbeSkipped, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeChatGPT_AuthFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "chatgpt",
		APIKey:  "bad-token",
		BaseURL: srv.URL,
		Model:   "gpt-4o",
	}, DroidConfig{})

	if res.Status != ProbeAuthFailed {
		t.Fatalf("expected ProbeAuthFailed, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeChatGPT_EndpointUnreachable(t *testing.T) {
	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "chatgpt",
		APIKey:  "token",
		BaseURL: "http://127.0.0.1:1",
		Model:   "gpt-4o",
	}, DroidConfig{})

	if res.Status != ProbeEndpointUnreachable {
		t.Fatalf("expected ProbeEndpointUnreachable, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeChatGPT_EndpointUnreachable_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`internal server error`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "chatgpt",
		APIKey:  "token",
		BaseURL: srv.URL,
		Model:   "gpt-4o",
	}, DroidConfig{})

	if res.Status != ProbeEndpointUnreachable {
		t.Fatalf("expected ProbeEndpointUnreachable, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeChatGPT_ModelNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "chatgpt",
		APIKey:  "token",
		BaseURL: srv.URL,
		Model:   "nonexistent-model",
	}, DroidConfig{})

	if res.Status != ProbeModelNotFound {
		t.Fatalf("expected ProbeModelNotFound, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestProbeChatGPT_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// Use a model from the known catalog so it passes.
	knownModels := AvailableModels()["chatgpt"]
	if len(knownModels) == 0 {
		t.Skip("no known chatgpt models in catalog")
	}

	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:    "chatgpt",
		APIKey:  "token",
		BaseURL: srv.URL,
		Model:   knownModels[0],
	}, DroidConfig{})

	if res.Status != ProbeOK {
		t.Fatalf("expected ProbeOK, got %d (detail: %s)", res.Status, res.Detail)
	}
	if res.Latency == 0 {
		t.Fatal("expected non-zero latency")
	}
}

func TestProbeChatGPT_OKWithCodexAuthCache(t *testing.T) {
	const accountID = "account-from-cache"
	token := testJWT(t, time.Now().Add(time.Hour))
	authFile := filepath.Join(t.TempDir(), "auth.json")
	writeCodexAuth(t, authFile, token, accountID)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != accountID {
			t.Fatalf("ChatGPT-Account-ID = %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	knownModels := AvailableModels()["chatgpt"]
	if len(knownModels) == 0 {
		t.Skip("no known chatgpt models in catalog")
	}
	res := ProbeProvider(context.Background(), ProviderConfig{
		Name:            "chatgpt",
		BaseURL:         srv.URL,
		Model:           knownModels[0],
		ChatGPTAuthFile: authFile,
	}, DroidConfig{})
	if res.Status != ProbeOK {
		t.Fatalf("expected ProbeOK, got %d (detail: %s)", res.Status, res.Detail)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Fatalf("expected 'hello', got %q", got)
	}
	if got := truncate("hello world", 5); got != "hello…" {
		t.Fatalf("expected 'hello…', got %q", got)
	}
	// Verify rune-safe truncation: "Привет" is 6 runes but 12 bytes.
	if got := truncate("Привет мир", 6); got != "Привет…" {
		t.Fatalf("expected 'Привет…', got %q", got)
	}
}

func writeProbeBinary(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write probe binary: %v", err)
	}
	return path
}
