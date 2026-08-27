package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type chatGPTTransportTestError struct{}

func (*chatGPTTransportTestError) Error() string   { return "test transport failure" }
func (*chatGPTTransportTestError) Timeout() bool   { return true }
func (*chatGPTTransportTestError) Temporary() bool { return true }

type chatGPTRoundTripFunc func(*http.Request) (*http.Response, error)

func (f chatGPTRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestChatGPTStaticAPIKeyCompatibility(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewChatGPTClient(ProviderConfig{
		Name:            "chatgpt",
		APIKey:          "static-test-key",
		BaseURL:         server.URL,
		ChatGPTAuthFile: filepath.Join(t.TempDir(), "missing.json"),
	})
	client.auth.refresh = func(context.Context, string, string) error {
		t.Fatal("static API key must not invoke Codex refresh")
		return nil
	}

	got, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if got != "ok" {
		t.Fatalf("Complete() = %q, want ok", got)
	}
	if authorization != "Bearer static-test-key" {
		t.Fatalf("Authorization = %q", authorization)
	}
}

func TestChatGPTSendRequestPreservesTransportCause(t *testing.T) {
	sentinel := &chatGPTTransportTestError{}
	httpClient := &http.Client{Transport: chatGPTRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, sentinel
	})}
	client := NewChatGPTClient(ProviderConfig{Name: "chatgpt", BaseURL: "https://example.invalid"})

	_, err := client.sendRequest(context.Background(), []byte(`{}`), chatGPTCredentials{accessToken: "test-token"}, httpClient)
	if err == nil {
		t.Fatal("sendRequest unexpectedly succeeded")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is(%v, sentinel) = false", err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) {
		t.Fatalf("errors.As(%v, net.Error) = false", err)
	}
	if got := ClassifyBackendError(err); got != BackendFailureUnavailable {
		t.Fatalf("ClassifyBackendError(%v) = %s, want %s", err, got, BackendFailureUnavailable)
	}
}

func TestChatGPTRequestIncludesConfiguredReasoningEffort(t *testing.T) {
	var requestBody struct {
		Model     string `json:"model"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewChatGPTClient(ProviderConfig{
		Name:       "chatgpt",
		APIKey:     "static-test-key",
		BaseURL:    server.URL,
		Model:      "gpt-5.6-sol",
		ThinkLevel: "high",
	})
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if requestBody.Model != "gpt-5.6-sol" || requestBody.Reasoning.Effort != "high" {
		t.Fatalf("request model=%q reasoning=%q", requestBody.Model, requestBody.Reasoning.Effort)
	}
}

func TestChatGPTAuthMissingAndMalformedAreSecretSafe(t *testing.T) {
	secret := "super-secret-access-token"
	tests := []struct {
		name    string
		content *string
	}{
		{name: "missing"},
		{name: "malformed json", content: stringPtr(`{"tokens":{"access_token":"` + secret)},
		{name: "malformed jwt", content: stringPtr(`{"tokens":{"access_token":"` + secret + `","account_id":"acct"}}`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			if tc.content != nil {
				if err := os.WriteFile(authPath, []byte(*tc.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			manager := newChatGPTAuthManager(ProviderConfig{ChatGPTAuthFile: authPath})
			_, err := manager.credentials(context.Background())
			if err == nil {
				t.Fatal("credentials() unexpectedly succeeded")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked secret: %v", err)
			}
		})
	}
}

func TestChatGPTConcurrentRefreshIsSharedAcrossManagers(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	oldToken := testJWT(t, now.Add(time.Minute))
	newToken := testJWT(t, now.Add(time.Hour))
	writeCodexAuth(t, authPath, oldToken, "acct-old")

	var refreshCalls atomic.Int32
	refresh := func(context.Context, string, string) error {
		refreshCalls.Add(1)
		time.Sleep(30 * time.Millisecond)
		return writeCodexAuthFile(authPath, newToken, "acct-new")
	}
	managerA := newChatGPTAuthManager(ProviderConfig{ChatGPTAuthFile: authPath})
	managerB := newChatGPTAuthManager(ProviderConfig{ChatGPTAuthFile: authPath})
	managerA.now, managerB.now = func() time.Time { return now }, func() time.Time { return now }
	managerA.refresh, managerB.refresh = refresh, refresh

	const goroutines = 24
	start := make(chan struct{})
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		manager := managerA
		if i%2 == 1 {
			manager = managerB
		}
		go func() {
			defer wg.Done()
			<-start
			creds, err := manager.credentials(context.Background())
			if err == nil && creds.accessToken != newToken {
				err = fmt.Errorf("got stale token")
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestChatGPTRejectsUnchangedNearExpiryCacheAfterRefresh(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	token := testJWT(t, now.Add(time.Minute))
	writeCodexAuth(t, authPath, token, "acct")

	manager := newChatGPTAuthManager(ProviderConfig{ChatGPTAuthFile: authPath})
	manager.now = func() time.Time { return now }
	manager.refresh = func(context.Context, string, string) error { return nil }
	_, err := manager.credentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fresh credentials") {
		t.Fatalf("credentials() error = %v, want unchanged-cache failure", err)
	}
}

func TestChatGPTRejectsChangedButNearExpiryCacheAfterRefresh(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	oldToken := testJWT(t, now.Add(time.Minute))
	changedButNearExpiry := testJWT(t, now.Add(2*time.Minute))
	writeCodexAuth(t, authPath, oldToken, "acct")

	manager := newChatGPTAuthManager(ProviderConfig{ChatGPTAuthFile: authPath})
	manager.now = func() time.Time { return now }
	manager.refresh = func(context.Context, string, string) error {
		writeCodexAuth(t, authPath, changedButNearExpiry, "acct")
		return nil
	}
	_, err := manager.credentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fresh credentials") {
		t.Fatalf("credentials() error = %v, want near-expiry failure", err)
	}
}

func TestChatGPTUnauthorizedPathDoesNotAcceptChangedNearExpiryCache(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	rejectedToken := testJWT(t, now.Add(time.Hour))
	changedButNearExpiry := testJWT(t, now.Add(2*time.Minute))
	writeCodexAuth(t, authPath, changedButNearExpiry, "acct")

	manager := newChatGPTAuthManager(ProviderConfig{ChatGPTAuthFile: authPath})
	manager.now = func() time.Time { return now }
	var refreshCalls atomic.Int32
	manager.refresh = func(context.Context, string, string) error {
		refreshCalls.Add(1)
		return nil
	}
	_, err := manager.refreshRejected(context.Background(), rejectedToken)
	if err == nil || !strings.Contains(err.Error(), "fresh credentials") {
		t.Fatalf("refreshRejected() error = %v, want near-expiry failure", err)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls.Load())
	}
}

func TestChatGPTRetriesOnceAfterUnauthorized(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	oldToken := testJWT(t, now.Add(time.Hour))
	newToken := testJWT(t, now.Add(2*time.Hour))
	writeCodexAuth(t, authPath, oldToken, "acct-old")

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.Header.Get("Authorization") {
		case "Bearer " + oldToken:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, oldToken)
		case "Bearer " + newToken:
			if got := r.Header.Get("ChatGPT-Account-ID"); got != "acct-new" {
				t.Errorf("ChatGPT-Account-ID = %q", got)
			}
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"retried\"}\n\ndata: [DONE]\n\n")
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()

	client := NewChatGPTClient(ProviderConfig{Name: "chatgpt", BaseURL: server.URL, ChatGPTAuthFile: authPath})
	client.auth.now = func() time.Time { return now }
	var refreshCalls atomic.Int32
	client.auth.refresh = func(context.Context, string, string) error {
		refreshCalls.Add(1)
		writeCodexAuth(t, authPath, newToken, "acct-new")
		return nil
	}

	got, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if got != "retried" {
		t.Fatalf("Complete() = %q", got)
	}
	if requests.Load() != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("requests=%d refreshes=%d", requests.Load(), refreshCalls.Load())
	}
}

func TestChatGPTRefreshErrorsDoNotLeakSecrets(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	token := testJWT(t, now.Add(time.Minute))
	writeCodexAuth(t, authPath, token, "acct")
	secret := "refresh-stderr-secret"

	manager := newChatGPTAuthManager(ProviderConfig{ChatGPTAuthFile: authPath})
	manager.now = func() time.Time { return now }
	manager.refresh = func(context.Context, string, string) error {
		return fmt.Errorf("CLI failed: %s", secret)
	}
	_, err := manager.credentials(context.Background())
	if err == nil {
		t.Fatal("credentials() unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked refresh output: %v", err)
	}
}

func TestCodexRefreshCommandIsEphemeralReadOnlyAndToolRestricted(t *testing.T) {
	args := strings.Join(codexAuthRefreshArgs(), " ")
	for _, required := range []string{
		"exec",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--skip-git-repo-check",
		"--sandbox read-only",
		"--disable shell_tool",
		`web_search="disabled"`,
		`approval_policy="never"`,
	} {
		if !strings.Contains(args, required) {
			t.Fatalf("Codex refresh args missing %q: %s", required, args)
		}
	}
}

func TestCodexRefreshEnvironmentUsesOnlySelectedAuthHome(t *testing.T) {
	env := withCodexHome([]string{
		"PATH=/bin",
		"CODEX_HOME=/old-home",
		"OPENAI_API_KEY=must-not-override-chatgpt-auth",
	}, "/selected-home")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "/old-home") || strings.Contains(joined, "must-not-override") {
		t.Fatalf("refresh environment retained conflicting credentials: %s", joined)
	}
	if !strings.Contains(joined, "CODEX_HOME=/selected-home") {
		t.Fatalf("refresh environment missing selected CODEX_HOME: %s", joined)
	}
}

func writeCodexAuth(t *testing.T, path, token, accountID string) {
	t.Helper()
	if err := writeCodexAuthFile(path, token, accountID); err != nil {
		t.Fatal(err)
	}
}

func writeCodexAuthFile(path, token, accountID string) error {
	data, err := json.Marshal(map[string]any{
		"tokens": map[string]string{
			"access_token": token,
			"account_id":   accountID,
		},
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func testJWT(t *testing.T, expiresAt time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payloadJSON, err := json.Marshal(map[string]int64{"exp": expiresAt.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	return header + "." + payload + ".signature"
}

func stringPtr(value string) *string { return &value }
