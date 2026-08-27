package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const chatGPTRefreshWindow = 5 * time.Minute

type chatGPTCredentials struct {
	accessToken string
	accountID   string
	expiresAt   time.Time
	static      bool
}

type codexAuthCache struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

type codexRefreshFunc func(context.Context, string, string) error

type chatGPTAuthManager struct {
	config  ProviderConfig
	now     func() time.Time
	refresh codexRefreshFunc
}

// Codex owns a single auth cache per CODEX_HOME. Clients are frequently
// recreated for model overrides, so refresh coordination must be process-wide,
// not tied to a ChatGPTClient instance.
var chatGPTRefreshLocks sync.Map // map[cleaned auth cache path]*sync.Mutex

func newChatGPTAuthManager(config ProviderConfig) *chatGPTAuthManager {
	return &chatGPTAuthManager{
		config:  config,
		now:     time.Now,
		refresh: runCodexAuthRefresh,
	}
}

func (a *chatGPTAuthManager) credentials(ctx context.Context) (chatGPTCredentials, error) {
	if a.config.APIKey != "" {
		return chatGPTCredentials{accessToken: a.config.APIKey, static: true}, nil
	}

	creds, err := a.readCodexCredentials()
	if err != nil {
		return chatGPTCredentials{}, err
	}
	if creds.expiresAt.After(a.now().Add(chatGPTRefreshWindow)) {
		return creds, nil
	}

	return a.refreshCredentials(ctx, creds.accessToken, false)
}

func (a *chatGPTAuthManager) refreshRejected(ctx context.Context, rejectedToken string) (chatGPTCredentials, error) {
	if a.config.APIKey != "" {
		return chatGPTCredentials{accessToken: a.config.APIKey, static: true}, nil
	}
	return a.refreshCredentials(ctx, rejectedToken, true)
}

func (a *chatGPTAuthManager) refreshCredentials(ctx context.Context, observedToken string, force bool) (chatGPTCredentials, error) {
	authPath, codexHome, err := a.authLocations()
	if err != nil {
		return chatGPTCredentials{}, err
	}
	refreshLock := chatGPTRefreshLock(authPath)
	refreshLock.Lock()
	defer refreshLock.Unlock()

	// Always reread after acquiring the lock. Another request may already have
	// refreshed Codex's cache while this request was waiting.
	current, err := a.readCodexCredentials()
	if err != nil {
		return chatGPTCredentials{}, err
	}
	if force {
		if current.accessToken != observedToken && current.expiresAt.After(a.now().Add(chatGPTRefreshWindow)) {
			return current, nil
		}
	} else if current.expiresAt.After(a.now().Add(chatGPTRefreshWindow)) {
		return current, nil
	}

	binary := strings.TrimSpace(a.config.ChatGPTCodexBinary)
	if binary == "" {
		binary = "codex"
	}
	if err := a.refresh(ctx, binary, codexHome); err != nil {
		// The callback or CLI may return stderr containing credentials. Keep the
		// public error deliberately opaque.
		return chatGPTCredentials{}, errors.New("Codex CLI auth refresh failed")
	}

	refreshed, err := a.readCodexCredentials()
	if err != nil {
		return chatGPTCredentials{}, err
	}
	if !refreshed.expiresAt.After(a.now().Add(chatGPTRefreshWindow)) {
		return chatGPTCredentials{}, errors.New("Codex auth cache did not provide fresh credentials")
	}
	return refreshed, nil
}

func chatGPTRefreshLock(authPath string) *sync.Mutex {
	key := filepath.Clean(authPath)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	lock, _ := chatGPTRefreshLocks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func (a *chatGPTAuthManager) readCodexCredentials() (chatGPTCredentials, error) {
	authPath, _, err := a.authLocations()
	if err != nil {
		return chatGPTCredentials{}, err
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		return chatGPTCredentials{}, errors.New("cannot read Codex auth cache")
	}

	var cache codexAuthCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return chatGPTCredentials{}, errors.New("Codex auth cache is malformed")
	}
	accessToken := strings.TrimSpace(cache.Tokens.AccessToken)
	if accessToken == "" {
		return chatGPTCredentials{}, errors.New("Codex auth cache has no access token")
	}
	expiresAt, err := jwtExpiry(accessToken)
	if err != nil {
		return chatGPTCredentials{}, errors.New("Codex auth cache access token is malformed")
	}

	return chatGPTCredentials{
		accessToken: accessToken,
		accountID:   strings.TrimSpace(cache.Tokens.AccountID),
		expiresAt:   expiresAt,
	}, nil
}

func (a *chatGPTAuthManager) authLocations() (string, string, error) {
	authPath := strings.TrimSpace(a.config.ChatGPTAuthFile)
	codexHome := strings.TrimSpace(a.config.ChatGPTCodexHome)
	if codexHome == "" {
		codexHome = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}

	var err error
	if codexHome != "" {
		codexHome, err = expandUserPath(codexHome)
		if err != nil {
			return "", "", errors.New("cannot resolve CODEX_HOME")
		}
	}
	if authPath != "" {
		authPath, err = expandUserPath(authPath)
		if err != nil {
			return "", "", errors.New("cannot resolve Codex auth cache path")
		}
		if codexHome == "" {
			codexHome = filepath.Dir(authPath)
		}
	}
	if codexHome == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", "", errors.New("cannot resolve Codex auth cache path")
		}
		codexHome = filepath.Join(home, ".codex")
	}
	if authPath == "" {
		authPath = filepath.Join(codexHome, "auth.json")
	}

	return filepath.Clean(authPath), filepath.Clean(codexHome), nil
}

func expandUserPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func jwtExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return time.Time{}, errors.New("invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, errors.New("invalid JWT")
	}
	var claims struct {
		ExpiresAt json.Number `json:"exp"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil || claims.ExpiresAt == "" {
		return time.Time{}, errors.New("invalid JWT")
	}
	exp, err := claims.ExpiresAt.Int64()
	if err != nil || exp <= 0 {
		return time.Time{}, errors.New("invalid JWT")
	}
	return time.Unix(exp, 0), nil
}

func runCodexAuthRefresh(ctx context.Context, binary, codexHome string) error {
	refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(refreshCtx, binary, codexAuthRefreshArgs()...)
	cmd.Env = withCodexHome(os.Environ(), codexHome)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func codexAuthRefreshArgs() []string {
	return []string{
		"exec",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--skip-git-repo-check",
		"--sandbox", "read-only",
		"--disable", "shell_tool",
		"-c", `web_search="disabled"`,
		"-c", `approval_policy="never"`,
		"-c", `model_reasoning_effort="low"`,
		"Reply with OK only. Do not use tools.",
	}
}

func withCodexHome(env []string, codexHome string) []string {
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if ok && (strings.EqualFold(key, "CODEX_HOME") || strings.EqualFold(key, "OPENAI_API_KEY")) {
			continue
		}
		result = append(result, item)
	}
	return append(result, "CODEX_HOME="+codexHome)
}

func (c *ChatGPTClient) doRequest(ctx context.Context, body []byte, client *http.Client) (*http.Response, error) {
	creds, err := c.auth.credentials(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendRequest(ctx, body, creds, client)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || creds.static {
		return resp, nil
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	refreshed, err := c.auth.refreshRejected(ctx, creds.accessToken)
	if err != nil {
		return nil, err
	}
	return c.sendRequest(ctx, body, refreshed, client)
}

func (c *ChatGPTClient) sendRequest(ctx context.Context, body []byte, creds chatGPTCredentials, client *http.Client) (*http.Response, error) {
	req, err := c.buildRequest(ctx, body, creds)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ChatGPT API request failed: %w", err)
	}
	return resp, nil
}
