package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// TestProbeChatGPT_UnauthorizedWithUnexpiredCredsIsInconclusiveOK guards the
// 2026-08-15 outage class: the /models probe endpoint rejected a token that
// /codex/responses accepted, and the fatal auth_failed preflight killed every
// reply. With unexpired cached credentials the probe must stay healthy.
func TestProbeChatGPT_UnauthorizedWithUnexpiredCredsIsInconclusiveOK(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":"nope"}`)
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
		t.Fatalf("Status = %v (detail %q), want inconclusive-OK", res.Status, res.Detail)
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

// TestProbeChatGPT_InconclusiveStillValidatesCatalog: the local model check
// must run even when the auth result is inconclusive, so a misconfigured
// model is caught without the /models endpoint.
func TestProbeChatGPT_InconclusiveStillValidatesCatalog(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	cfg := ProviderConfig{
		Name:               "chatgpt",
		Model:              "gpt-typo-model",
		BaseURL:            server.URL,
		ChatGPTAuthFile:    writeFakeCodexAuth(t, time.Now().Add(2*time.Hour)),
		ChatGPTCodexBinary: "/bin/false",
	}

	res := probeChatGPT(context.Background(), ProbeResult{}, cfg)
	if res.Status != ProbeModelNotFound {
		t.Fatalf("Status = %v (detail %q), want ProbeModelNotFound", res.Status, res.Detail)
	}
}
