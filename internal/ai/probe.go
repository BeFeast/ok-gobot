package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// ProbeStatus classifies the outcome of a provider health check.
type ProbeStatus int

const (
	// ProbeOK means the provider authenticated and the model was found.
	ProbeOK ProbeStatus = iota
	// ProbeAuthFailed means the API key / OAuth token was rejected (HTTP 401/403).
	ProbeAuthFailed
	// ProbeEndpointUnreachable means the base URL could not be contacted.
	ProbeEndpointUnreachable
	// ProbeModelNotFound means auth succeeded but the configured model is unknown.
	ProbeModelNotFound
	// ProbeSkipped means the provider cannot be probed (e.g. droid subprocess).
	ProbeSkipped
)

// ProbeResult holds the outcome of a provider health check.
type ProbeResult struct {
	Provider        string
	Model           string
	Status          ProbeStatus
	Latency         time.Duration
	AvailableModels []string // populated on ModelNotFound when discoverable
	Detail          string   // human-readable detail / error context
}

// ProbeProvider performs a lightweight health check against the configured
// provider, distinguishing authentication, endpoint, and model failures.
// The context should carry a reasonable timeout (e.g. 10 s).
// For the "droid" provider, pass DroidConfig to resolve the binary path.
func ProbeProvider(ctx context.Context, cfg ProviderConfig, droidCfg DroidConfig) ProbeResult {
	base := ProbeResult{Provider: cfg.Name, Model: cfg.Model}

	switch cfg.Name {
	case "droid":
		return probeDroid(base, cfg, droidCfg)
	case "anthropic":
		return probeAnthropic(ctx, base, cfg)
	case "chatgpt", "openai-codex":
		return probeChatGPT(ctx, base, cfg)
	default:
		// OpenAI-compatible: openai, openrouter, custom, etc.
		return probeOpenAICompat(ctx, base, cfg)
	}
}

// ---------- OpenAI-compatible (openai, openrouter, custom) ----------

func probeOpenAICompat(ctx context.Context, res ProbeResult, cfg ProviderConfig) ProbeResult {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		switch cfg.Name {
		case "openai":
			baseURL = "https://api.openai.com/v1"
		case "openrouter":
			baseURL = "https://openrouter.ai/api/v1"
		default:
			res.Status = ProbeSkipped
			res.Detail = "no base_url configured for custom provider"
			return res
		}
	}

	modelsURL := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		res.Status = ProbeEndpointUnreachable
		res.Detail = fmt.Sprintf("invalid URL: %v", err)
		return res
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	if cfg.Name == "openrouter" {
		req.Header.Set("HTTP-Referer", "https://github.com/BeFeast/ok-gobot")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		res.Status = ProbeEndpointUnreachable
		res.Detail = fmt.Sprintf("endpoint unreachable: %v", err)
		return res
	}
	defer resp.Body.Close()
	res.Latency = latency

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		res.Status = ProbeAuthFailed
		res.Detail = "authentication failed (check API key)"
		return res
	}

	if resp.StatusCode != http.StatusOK {
		res.Status = ProbeEndpointUnreachable
		res.Detail = fmt.Sprintf("unexpected status %d: %s", resp.StatusCode, truncate(string(body), 200))
		return res
	}

	// Parse the model list and check if configured model exists.
	models := parseOpenAIModelList(body)
	if cfg.Model != "" && len(models) > 0 {
		found := false
		for _, m := range models {
			if m == cfg.Model {
				found = true
				break
			}
		}
		if !found {
			res.Status = ProbeModelNotFound
			res.AvailableModels = models
			res.Detail = fmt.Sprintf("model %q not found", cfg.Model)
			return res
		}
	}

	res.Status = ProbeOK
	res.Detail = fmt.Sprintf("ok (model %s, latency %dms)", cfg.Model, latency.Milliseconds())
	return res
}

// parseOpenAIModelList extracts model IDs from an OpenAI /models response.
func parseOpenAIModelList(body []byte) []string {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// ---------- Anthropic ----------

func probeAnthropic(ctx context.Context, res ProbeResult, cfg ProviderConfig) ProbeResult {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}

	// Resolve API key (supports OAuth).
	tmpClient := NewAnthropicClient(cfg)
	apiKey, err := tmpClient.resolveAPIKey(ctx)
	if err != nil {
		res.Status = ProbeAuthFailed
		res.Detail = fmt.Sprintf("authentication failed: %v", err)
		return res
	}

	// Use the lightweight GET /v1/models endpoint to validate auth and
	// reachability without consuming any API credits.
	modelsURL := strings.TrimRight(cfg.BaseURL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		res.Status = ProbeEndpointUnreachable
		res.Detail = fmt.Sprintf("invalid URL: %v", err)
		return res
	}
	req.Header.Set("anthropic-version", anthropicVersionHeader)
	if isOAuthAccessToken(apiKey) {
		req.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(apiKey, "oauth:"))
	} else {
		req.Header.Set("x-api-key", apiKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		res.Status = ProbeEndpointUnreachable
		res.Detail = fmt.Sprintf("endpoint unreachable: %v", err)
		return res
	}
	defer resp.Body.Close()
	res.Latency = latency

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		res.Status = ProbeAuthFailed
		res.Detail = "authentication failed (check API key)"
		return res
	}

	if resp.StatusCode != http.StatusOK {
		res.Status = ProbeEndpointUnreachable
		res.Detail = fmt.Sprintf("unexpected status %d: %s", resp.StatusCode, truncate(string(body), 200))
		return res
	}

	// Parse the model list and check if configured model exists.
	// The Anthropic /v1/models response uses the same {data:[{id:...}]} shape.
	knownModels := AvailableModels()["anthropic"]
	apiModels := parseOpenAIModelList(body)
	modelsToCheck := apiModels
	if len(modelsToCheck) == 0 {
		modelsToCheck = knownModels
	}

	if cfg.Model != "" && len(modelsToCheck) > 0 {
		found := false
		for _, m := range modelsToCheck {
			if m == cfg.Model {
				found = true
				break
			}
		}
		if !found {
			res.Status = ProbeModelNotFound
			res.AvailableModels = modelsToCheck
			res.Detail = fmt.Sprintf("model %q not found", cfg.Model)
			return res
		}
	}

	res.Status = ProbeOK
	res.Detail = fmt.Sprintf("ok (model %s, latency %dms)", cfg.Model, latency.Milliseconds())
	return res
}

// ---------- ChatGPT (Codex Responses API) ----------

func probeChatGPT(ctx context.Context, res ProbeResult, cfg ProviderConfig) ProbeResult {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://chatgpt.com/backend-api"
	}

	// ChatGPT uses session tokens — just verify the endpoint is reachable
	// and the token produces a non-401 response.
	pingURL := strings.TrimRight(cfg.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pingURL, nil)
	if err != nil {
		res.Status = ProbeEndpointUnreachable
		res.Detail = fmt.Sprintf("invalid URL: %v", err)
		return res
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		res.Status = ProbeEndpointUnreachable
		res.Detail = fmt.Sprintf("endpoint unreachable: %v", err)
		return res
	}
	defer resp.Body.Close()
	res.Latency = latency

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		res.Status = ProbeAuthFailed
		res.Detail = "authentication failed (check API key)"
		return res
	}

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		res.Status = ProbeEndpointUnreachable
		res.Detail = fmt.Sprintf("unexpected status %d: %s", resp.StatusCode, truncate(string(body), 200))
		return res
	}

	// Check model against known catalog.
	knownModels := AvailableModels()["chatgpt"]
	if cfg.Model != "" && len(knownModels) > 0 {
		found := false
		for _, m := range knownModels {
			if m == cfg.Model {
				found = true
				break
			}
		}
		if !found {
			res.Status = ProbeModelNotFound
			res.AvailableModels = knownModels
			res.Detail = fmt.Sprintf("model %q not in known catalog", cfg.Model)
			return res
		}
	}

	res.Status = ProbeOK
	res.Detail = fmt.Sprintf("ok (model %s, latency %dms)", cfg.Model, latency.Milliseconds())
	return res
}

// ---------- Droid (subprocess) ----------

func probeDroid(res ProbeResult, cfg ProviderConfig, droidCfg DroidConfig) ProbeResult {
	binary := droidCfg.BinaryPath
	if binary == "" {
		binary = "droid"
	}

	if _, err := exec.LookPath(binary); err != nil {
		res.Status = ProbeEndpointUnreachable
		res.Detail = fmt.Sprintf("droid binary not found: %s", binary)
		return res
	}

	// Check model against known catalog.
	knownModels := AvailableModels()["droid"]
	if cfg.Model != "" && len(knownModels) > 0 {
		found := false
		for _, m := range knownModels {
			if m == cfg.Model {
				found = true
				break
			}
		}
		if !found {
			res.Status = ProbeModelNotFound
			res.AvailableModels = knownModels
			res.Detail = fmt.Sprintf("model %q not in known catalog", cfg.Model)
			return res
		}
	}

	res.Status = ProbeOK
	res.Detail = fmt.Sprintf("ok (binary %s, model %s)", binary, cfg.Model)
	return res
}

// ---------- helpers ----------

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
