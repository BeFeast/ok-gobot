package ai

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultAnthropicOAuthClientID   = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	defaultAnthropicOAuthAuthURL    = "https://claude.ai/oauth/authorize"
	defaultAnthropicOAuthTokenURL   = "https://console.anthropic.com/v1/oauth/token"
	defaultAnthropicOAuthRedirectURI = "https://console.anthropic.com/oauth/code/callback"
	defaultAnthropicOAuthScopes     = "org:create_api_key user:profile user:inference"
)

const (
	anthropicOAuthExpirySkew = 5 * time.Minute
	anthropicOAuthMinTTL     = 60 * time.Minute
)

var (
	anthropicOAuthClientID    = defaultAnthropicOAuthClientID
	anthropicOAuthAuthURL     = defaultAnthropicOAuthAuthURL
	anthropicOAuthTokenURL    = defaultAnthropicOAuthTokenURL
	anthropicOAuthRedirectURI = defaultAnthropicOAuthRedirectURI
	anthropicOAuthScopes      = defaultAnthropicOAuthScopes
)

// AnthropicOAuthAuthRequest contains browser URL and verifier data for PKCE flow.
type AnthropicOAuthAuthRequest struct {
	URL      string
	Verifier string
	State    string
}

// AnthropicOAuthCredentials stores OAuth tokens and metadata for Anthropic.
type AnthropicOAuthCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"` // unix millis
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
}

// IsExpired returns true when access token is already expired.
func (c *AnthropicOAuthCredentials) IsExpired(now time.Time) bool {
	if c == nil || c.ExpiresAt <= 0 {
		return false
	}
	return now.UnixMilli() >= c.ExpiresAt
}

// IsExpiringSoon returns true when access token is near expiration.
func (c *AnthropicOAuthCredentials) IsExpiringSoon(now time.Time, within time.Duration) bool {
	if c == nil || c.ExpiresAt <= 0 {
		return false
	}
	return now.Add(within).UnixMilli() >= c.ExpiresAt
}

type anthropicOAuthTokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// NewAnthropicOAuthAuthRequest prepares a new PKCE auth request URL.
func NewAnthropicOAuthAuthRequest() (*AnthropicOAuthAuthRequest, error) {
	verifier, err := randomURLSafeString(64)
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE verifier: %w", err)
	}
	state, err := randomURLSafeString(32)
	if err != nil {
		return nil, fmt.Errorf("failed to generate OAuth state: %w", err)
	}

	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", anthropicOAuthClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", anthropicOAuthRedirectURI)
	q.Set("scope", anthropicOAuthScopes)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)

	return &AnthropicOAuthAuthRequest{
		URL:      anthropicOAuthAuthURL + "?" + q.Encode(),
		Verifier: verifier,
		State:    state,
	}, nil
}

// ExtractAnthropicOAuthCode accepts either a raw code or callback URL and returns only code value.
func ExtractAnthropicOAuthCode(input string) string {
	raw := strings.TrimSpace(input)
	raw = strings.TrimSuffix(raw, "#")
	if raw == "" {
		return ""
	}

	if parsed, err := url.Parse(raw); err == nil {
		if code := strings.TrimSpace(parsed.Query().Get("code")); code != "" {
			return code
		}
	}

	// Handle pasted "code=..." fragments.
	if idx := strings.Index(raw, "code="); idx >= 0 {
		fragment := raw[idx+len("code="):]
		for i, r := range fragment {
			if r == '&' || r == '#' || r == ' ' || r == '\n' || r == '\r' {
				fragment = fragment[:i]
				break
			}
		}
		decoded, err := url.QueryUnescape(fragment)
		if err == nil && strings.TrimSpace(decoded) != "" {
			return strings.TrimSpace(decoded)
		}
	}

	return raw
}

// ExchangeAnthropicOAuthCode exchanges authorization code for access/refresh tokens.
func ExchangeAnthropicOAuthCode(ctx context.Context, code, verifier, state string) (*AnthropicOAuthCredentials, error) {
	code = ExtractAnthropicOAuthCode(code)
	if code == "" {
		return nil, errors.New("authorization code is required")
	}
	if strings.TrimSpace(verifier) == "" {
		return nil, errors.New("PKCE verifier is required")
	}
	if strings.TrimSpace(state) == "" {
		return nil, errors.New("OAuth state is required")
	}

	payload := map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     anthropicOAuthClientID,
		"code":          code,
		"redirect_uri":  anthropicOAuthRedirectURI,
		"code_verifier": verifier,
		"state":         state,
	}

	resp, err := requestAnthropicOAuthToken(ctx, payload)
	if err != nil {
		return nil, err
	}

	return toAnthropicOAuthCredentials(resp), nil
}

// RefreshAnthropicOAuthCredentials refreshes an existing Anthropic OAuth credential.
func RefreshAnthropicOAuthCredentials(ctx context.Context, creds *AnthropicOAuthCredentials) (*AnthropicOAuthCredentials, error) {
	if creds == nil {
		return nil, errors.New("credentials are required")
	}
	if strings.TrimSpace(creds.RefreshToken) == "" {
		return nil, errors.New("refresh token is missing")
	}

	clientID := strings.TrimSpace(creds.ClientID)
	if clientID == "" {
		clientID = anthropicOAuthClientID
	}

	payload := map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"refresh_token": creds.RefreshToken,
	}

	resp, err := requestAnthropicOAuthToken(ctx, payload)
	if err != nil {
		return nil, err
	}

	next := toAnthropicOAuthCredentials(resp)
	if next.RefreshToken == "" {
		next.RefreshToken = creds.RefreshToken
	}
	if next.Scope == "" {
		next.Scope = creds.Scope
	}
	if next.ClientID == "" {
		next.ClientID = clientID
	}
	return next, nil
}

// DefaultAnthropicOAuthStorePath returns default path for Anthropic OAuth credentials.
func DefaultAnthropicOAuthStorePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(homeDir, ".ok-gobot", "oauth", "anthropic.json"), nil
}

func resolveAnthropicOAuthStorePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path != "" {
		return path, nil
	}
	return DefaultAnthropicOAuthStorePath()
}

// LoadAnthropicOAuthCredentials reads credentials from disk.
func LoadAnthropicOAuthCredentials(path string) (*AnthropicOAuthCredentials, error) {
	path, err := resolveAnthropicOAuthStorePath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var creds AnthropicOAuthCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse OAuth credentials: %w", err)
	}
	if strings.TrimSpace(creds.AccessToken) == "" {
		return nil, errors.New("OAuth credentials are invalid: missing access_token")
	}
	if creds.ClientID == "" {
		creds.ClientID = anthropicOAuthClientID
	}
	return &creds, nil
}

// SaveAnthropicOAuthCredentials persists credentials to disk with strict permissions.
func SaveAnthropicOAuthCredentials(path string, creds *AnthropicOAuthCredentials) error {
	if creds == nil {
		return errors.New("credentials are required")
	}
	if strings.TrimSpace(creds.AccessToken) == "" {
		return errors.New("access token is required")
	}

	path, err := resolveAnthropicOAuthStorePath(path)
	if err != nil {
		return err
	}

	if creds.ClientID == "" {
		creds.ClientID = anthropicOAuthClientID
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode OAuth credentials: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create OAuth directory: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("failed to write OAuth credentials: %w", err)
	}
	return nil
}

// DeleteAnthropicOAuthCredentials removes stored credentials, ignoring missing file.
func DeleteAnthropicOAuthCredentials(path string) error {
	path, err := resolveAnthropicOAuthStorePath(path)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to delete OAuth credentials: %w", err)
	}
	return nil
}

func requestAnthropicOAuthToken(ctx context.Context, payload map[string]any) (*anthropicOAuthTokenResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode OAuth payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicOAuthTokenURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read OAuth response: %w", err)
	}

	var parsed anthropicOAuthTokenResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse OAuth response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(parsed.ErrorDescription)
		if msg == "" {
			msg = strings.TrimSpace(parsed.Error)
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("OAuth token request failed: %s", msg)
	}

	if strings.TrimSpace(parsed.AccessToken) == "" {
		return nil, errors.New("OAuth token response missing access_token")
	}

	return &parsed, nil
}

func toAnthropicOAuthCredentials(resp *anthropicOAuthTokenResponse) *AnthropicOAuthCredentials {
	return &AnthropicOAuthCredentials{
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		ExpiresAt:    expiresAtFromSeconds(resp.ExpiresIn),
		TokenType:    resp.TokenType,
		Scope:        resp.Scope,
		ClientID:     anthropicOAuthClientID,
	}
}

func expiresAtFromSeconds(expiresIn int64) int64 {
	now := time.Now()
	if expiresIn <= 0 {
		return now.Add(anthropicOAuthMinTTL).UnixMilli()
	}
	expires := now.Add(time.Duration(expiresIn) * time.Second).Add(-anthropicOAuthExpirySkew)
	if expires.Before(now) {
		return now.Add(anthropicOAuthMinTTL).UnixMilli()
	}
	return expires.UnixMilli()
}

func randomURLSafeString(length int) (string, error) {
	if length <= 0 {
		length = 32
	}
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
