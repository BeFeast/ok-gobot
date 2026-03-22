package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ok-gobot/internal/logger"
)

// ModelCatalog holds cached model lists fetched from provider APIs.
type ModelCatalog struct {
	FetchedAt time.Time           `json:"fetched_at"`
	Providers map[string][]string `json:"providers"`
}

// CatalogCacheTTL is how long a cached catalog is considered fresh.
const CatalogCacheTTL = 24 * time.Hour

// DefaultCachePath returns the default path for the model catalog cache file.
func DefaultCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".ok-gobot", "model-cache.json"), nil
}

// LoadCatalog reads a cached catalog from disk. Returns nil (no error) if the
// file does not exist yet.
func LoadCatalog(path string) (*ModelCatalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading catalog cache: %w", err)
	}
	var cat ModelCatalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return nil, fmt.Errorf("parsing catalog cache: %w", err)
	}
	return &cat, nil
}

// SaveCatalog writes a catalog to disk, creating parent directories as needed.
func SaveCatalog(path string, cat *ModelCatalog) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}
	data, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling catalog: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// IsFresh reports whether the catalog was fetched within the TTL window.
func (c *ModelCatalog) IsFresh() bool {
	return time.Since(c.FetchedAt) < CatalogCacheTTL
}

// FetchRemoteModels fetches model lists from provider APIs that support
// enumeration. It returns the merged results. Only providers whose API keys
// are supplied will be queried.
func FetchRemoteModels(ctx context.Context, apiKey, provider, baseURL string) (map[string][]string, error) {
	result := make(map[string][]string)
	client := &http.Client{Timeout: 15 * time.Second}

	type fetchJob struct {
		name    string
		baseURL string
		headers map[string]string
	}

	var jobs []fetchJob

	switch provider {
	case "openrouter":
		jobs = append(jobs, fetchJob{
			name:    "openrouter",
			baseURL: defaultBaseURL("openrouter", baseURL),
			headers: map[string]string{
				"HTTP-Referer": "https://github.com/BeFeast/ok-gobot",
				"X-Title":      "ok-gobot",
			},
		})
	case "openai":
		jobs = append(jobs, fetchJob{
			name:    "openai",
			baseURL: defaultBaseURL("openai", baseURL),
		})
	case "anthropic":
		// Anthropic does not expose a public /models endpoint; static list only.
	case "chatgpt":
		// ChatGPT Codex API does not expose a models endpoint.
	case "droid":
		// Droid is a local subprocess; no remote catalog.
	case "custom":
		// Try the OpenAI-compatible /models endpoint on custom base URLs.
		if baseURL != "" {
			jobs = append(jobs, fetchJob{
				name:    "custom",
				baseURL: strings.TrimSuffix(baseURL, "/"),
			})
		}
	default:
		// Unknown provider; try OpenAI-compatible if base URL present.
		if baseURL != "" {
			jobs = append(jobs, fetchJob{
				name:    provider,
				baseURL: strings.TrimSuffix(baseURL, "/"),
			})
		}
	}

	for _, job := range jobs {
		models, err := fetchOpenAICompatibleModels(ctx, client, job.baseURL, apiKey, job.headers)
		if err != nil {
			logger.Debugf("catalog: failed to fetch models from %s: %v", job.name, err)
			return result, fmt.Errorf("fetching %s models: %w", job.name, err)
		}
		result[job.name] = models
	}

	return result, nil
}

// defaultBaseURL returns the provider's base URL, falling back to well-known
// defaults for recognized providers.
func defaultBaseURL(provider, override string) string {
	if override != "" {
		return strings.TrimSuffix(override, "/")
	}
	switch provider {
	case "openai":
		return "https://api.openai.com/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	default:
		return ""
	}
}

// openAIModelsResponse is the envelope returned by GET /models on
// OpenAI-compatible APIs.
type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// fetchOpenAICompatibleModels queries GET <baseURL>/models and returns a
// sorted slice of model IDs.
func fetchOpenAICompatibleModels(ctx context.Context, client *http.Client, baseURL, apiKey string, extraHeaders map[string]string) ([]string, error) {
	url := baseURL + "/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, truncateBody(string(body), 200))
	}

	var modelsResp openAIModelsResponse
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	ids := make([]string, 0, len(modelsResp.Data))
	for _, m := range modelsResp.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// MergedModels returns the static model list for each provider, overlaid with
// any cached remote models. Remote models appear after static ones, deduplicated.
func MergedModels(cached *ModelCatalog) map[string][]string {
	static := AvailableModels()
	if cached == nil {
		return static
	}

	merged := make(map[string][]string, len(static)+len(cached.Providers))
	for p, models := range static {
		merged[p] = append([]string{}, models...)
	}
	for p, remoteModels := range cached.Providers {
		existing := make(map[string]bool, len(merged[p]))
		for _, m := range merged[p] {
			existing[m] = true
		}
		for _, m := range remoteModels {
			if !existing[m] {
				merged[p] = append(merged[p], m)
			}
		}
	}
	return merged
}

// truncateBody shortens s to at most n bytes, appending "..." if truncated.
func truncateBody(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
