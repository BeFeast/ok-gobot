package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFakeCodexAuth(t *testing.T, exp time.Time) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]int64{"exp": exp.Unix()})
	token := "x." + base64.RawURLEncoding.EncodeToString(payload) + ".y"
	cache := map[string]any{"tokens": map[string]string{"access_token": token, "account_id": "acc"}}
	data, _ := json.Marshal(cache)
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeChatGPTUsesRuntimeEndpointWithoutStartingModelTurn(t *testing.T) {
	var gotMethod, gotPath, gotBody, gotAccountID, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		gotAccountID = r.Header.Get("ChatGPT-Account-ID")
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	cfg := ProviderConfig{
		Name:               "chatgpt",
		Model:              "gpt-5.6-sol",
		BaseURL:            server.URL,
		ChatGPTAuthFile:    writeFakeCodexAuth(t, time.Now().Add(2*time.Hour)),
		ChatGPTCodexBinary: "/bin/false", // refresh CLI fails fast
	}

	res := probeChatGPT(context.Background(), ProbeResult{}, cfg)
	if res.Status != ProbeOK {
		t.Fatalf("Status = %v (detail %q), want ProbeOK", res.Status, res.Detail)
	}
	if gotMethod != http.MethodPost || gotPath != "/codex/responses" {
		t.Fatalf("probe request = %s %s, want POST /codex/responses", gotMethod, gotPath)
	}
	if gotBody != "{" {
		t.Fatalf("probe body = %q, want deliberately invalid JSON", gotBody)
	}
	if gotAccountID != "acc" || gotAccept != "text/event-stream" {
		t.Fatalf("runtime headers account=%q accept=%q", gotAccountID, gotAccept)
	}
	if res.FailureKind != BackendFailureNone {
		t.Fatalf("FailureKind = %q, want none", res.FailureKind)
	}
}

func TestProbeChatGPTRuntimeEndpointFailureOverridesModelsHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/models":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-sol"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/codex/responses":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Fatalf("unexpected probe request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	res := probeChatGPT(context.Background(), ProbeResult{}, ProviderConfig{
		Name:            "chatgpt",
		Model:           "gpt-5.6-sol",
		BaseURL:         server.URL,
		ChatGPTAuthFile: writeFakeCodexAuth(t, time.Now().Add(2*time.Hour)),
	})
	if res.Status != ProbeEndpointUnreachable || res.FailureKind != BackendFailureUnavailable {
		t.Fatalf("status=%v kind=%q detail=%q, want runtime endpoint unavailable", res.Status, res.FailureKind, res.Detail)
	}
}

func TestProbeChatGPTRuntimeEndpointNotFoundIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"route not found"}`))
	}))
	t.Cleanup(server.Close)

	res := probeChatGPT(context.Background(), ProbeResult{}, ProviderConfig{
		Name:            "chatgpt",
		Model:           "gpt-5.6-sol",
		BaseURL:         server.URL,
		ChatGPTAuthFile: writeFakeCodexAuth(t, time.Now().Add(2*time.Hour)),
	})
	if res.Status != ProbeEndpointUnreachable || res.FailureKind != BackendFailureUnavailable {
		t.Fatalf("status=%v kind=%q detail=%q, want missing runtime route unavailable", res.Status, res.FailureKind, res.Detail)
	}
}

func TestProbeChatGPTUnauthorizedOnRuntimeEndpointIsConclusive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	cfg := ProviderConfig{
		Name:               "chatgpt",
		Model:              "gpt-5.6-sol",
		BaseURL:            server.URL,
		ChatGPTAuthFile:    writeFakeCodexAuth(t, time.Now().Add(2*time.Hour)),
		ChatGPTCodexBinary: "/bin/false",
	}

	res := probeChatGPT(context.Background(), ProbeResult{}, cfg)
	if res.Status != ProbeAuthFailed || res.FailureKind != BackendFailureAuth {
		t.Fatalf("status=%v kind=%q detail=%q, want conclusive auth failure", res.Status, res.FailureKind, res.Detail)
	}
}

func TestProbeChatGPT_UnauthorizedWithExpiredCredsFails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	cfg := ProviderConfig{
		Name:               "chatgpt",
		Model:              "gpt-5.6-sol",
		BaseURL:            server.URL,
		ChatGPTAuthFile:    writeFakeCodexAuth(t, time.Now().Add(-time.Hour)),
		ChatGPTCodexBinary: "/bin/false",
	}

	res := probeChatGPT(context.Background(), ProbeResult{}, cfg)
	if res.Status != ProbeAuthFailed {
		t.Fatalf("Status = %v (detail %q), want ProbeAuthFailed for expired creds", res.Status, res.Detail)
	}
}

func TestProbeChatGPTRuntimeEndpointStillValidatesCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	t.Cleanup(server.Close)

	cfg := ProviderConfig{
		Name:            "chatgpt",
		Model:           "gpt-typo-model",
		BaseURL:         server.URL,
		ChatGPTAuthFile: writeFakeCodexAuth(t, time.Now().Add(2*time.Hour)),
	}

	res := probeChatGPT(context.Background(), ProbeResult{}, cfg)
	if res.Status != ProbeModelNotFound {
		t.Fatalf("Status = %v (detail %q), want ProbeModelNotFound", res.Status, res.Detail)
	}
}
