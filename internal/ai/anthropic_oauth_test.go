package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewAnthropicOAuthAuthRequest(t *testing.T) {
	req, err := NewAnthropicOAuthAuthRequest()
	if err != nil {
		t.Fatalf("NewAnthropicOAuthAuthRequest failed: %v", err)
	}
	if req.Verifier == "" {
		t.Fatal("expected non-empty verifier")
	}
	if req.State == "" {
		t.Fatal("expected non-empty state")
	}
	if !strings.HasPrefix(req.URL, anthropicOAuthAuthURL+"?") {
		t.Fatalf("unexpected auth URL: %s", req.URL)
	}
	if !strings.Contains(req.URL, "code_challenge=") {
		t.Fatalf("auth URL missing code_challenge: %s", req.URL)
	}
}

func TestExtractAnthropicOAuthCode(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "raw code", input: "abc123", want: "abc123"},
		{name: "query URL", input: "https://console.anthropic.com/oauth/code/callback?code=xyz789&state=s", want: "xyz789"},
		{name: "fragment suffix", input: "xyz789#", want: "xyz789"},
		{name: "code fragment", input: "code=hello%2Bworld&foo=bar", want: "hello+world"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractAnthropicOAuthCode(tt.input)
			if got != tt.want {
				t.Fatalf("ExtractAnthropicOAuthCode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExchangeAnthropicOAuthCode(t *testing.T) {
	// Not parallel: mutates package-level anthropicOAuthTokenURL.
	oldURL := anthropicOAuthTokenURL
	t.Cleanup(func() { anthropicOAuthTokenURL = oldURL })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if payload["grant_type"] != "authorization_code" {
			t.Fatalf("unexpected grant_type: %v", payload["grant_type"])
		}
		if payload["code"] != "sample-code" {
			t.Fatalf("unexpected code: %v", payload["code"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at_123","refresh_token":"rt_123","expires_in":3600,"token_type":"Bearer","scope":"scope1"}`))
	}))
	defer srv.Close()

	anthropicOAuthTokenURL = srv.URL

	creds, err := ExchangeAnthropicOAuthCode(context.Background(), "sample-code", "verifier", "state")
	if err != nil {
		t.Fatalf("ExchangeAnthropicOAuthCode failed: %v", err)
	}
	if creds.AccessToken != "at_123" {
		t.Fatalf("unexpected access token: %s", creds.AccessToken)
	}
	if creds.RefreshToken != "rt_123" {
		t.Fatalf("unexpected refresh token: %s", creds.RefreshToken)
	}
	if creds.ExpiresAt <= time.Now().UnixMilli() {
		t.Fatalf("expected future expiry, got %d", creds.ExpiresAt)
	}
}

func TestRefreshAnthropicOAuthCredentials(t *testing.T) {
	// Not parallel: mutates package-level anthropicOAuthTokenURL.
	oldURL := anthropicOAuthTokenURL
	t.Cleanup(func() { anthropicOAuthTokenURL = oldURL })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode payload: %v", err)
		}
		if payload["grant_type"] != "refresh_token" {
			t.Fatalf("unexpected grant_type: %v", payload["grant_type"])
		}
		if payload["refresh_token"] != "rt_old" {
			t.Fatalf("unexpected refresh_token: %v", payload["refresh_token"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at_new","refresh_token":"","expires_in":1800,"token_type":"Bearer","scope":"scope2"}`))
	}))
	defer srv.Close()

	anthropicOAuthTokenURL = srv.URL

	now := time.Now().Add(-1 * time.Hour).UnixMilli()
	creds, err := RefreshAnthropicOAuthCredentials(context.Background(), &AnthropicOAuthCredentials{
		AccessToken:  "at_old",
		RefreshToken: "rt_old",
		ExpiresAt:    now,
		Scope:        "scope_old",
		ClientID:     anthropicOAuthClientID,
	})
	if err != nil {
		t.Fatalf("RefreshAnthropicOAuthCredentials failed: %v", err)
	}
	if creds.AccessToken != "at_new" {
		t.Fatalf("unexpected access token: %s", creds.AccessToken)
	}
	// Provider may return empty refresh token on refresh; keep previous token.
	if creds.RefreshToken != "rt_old" {
		t.Fatalf("unexpected refresh token: %s", creds.RefreshToken)
	}
	if creds.Scope != "scope2" {
		t.Fatalf("unexpected scope: %s", creds.Scope)
	}
}

func TestSaveLoadAnthropicOAuthCredentials(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "anthropic-oauth.json")

	in := &AnthropicOAuthCredentials{
		AccessToken:  "at_save",
		RefreshToken: "rt_save",
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		TokenType:    "Bearer",
		Scope:        "scope",
	}
	if err := SaveAnthropicOAuthCredentials(path, in); err != nil {
		t.Fatalf("SaveAnthropicOAuthCredentials failed: %v", err)
	}

	out, err := LoadAnthropicOAuthCredentials(path)
	if err != nil {
		t.Fatalf("LoadAnthropicOAuthCredentials failed: %v", err)
	}
	if out.AccessToken != in.AccessToken {
		t.Fatalf("access token mismatch: got %s want %s", out.AccessToken, in.AccessToken)
	}
	if out.RefreshToken != in.RefreshToken {
		t.Fatalf("refresh token mismatch: got %s want %s", out.RefreshToken, in.RefreshToken)
	}
}
