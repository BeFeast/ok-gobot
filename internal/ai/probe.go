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

	"ok-gobot/internal/worker"
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
	// ProbeQuotaFailed means the provider reported quota/billing exhaustion.
	ProbeQuotaFailed
	// ProbeRateLimited means the provider is currently rate limiting requests.
	ProbeRateLimited
	// ProbeToolMissing means the selected backend cannot provide required tools.
	ProbeToolMissing
	// ProbeSkipped means the provider cannot be probed (e.g. droid subprocess).
	ProbeSkipped
)

// ProbeResult holds the outcome of a provider health check.
type ProbeResult struct {
	Provider        string
	Backend         string
	Model           string
	BaseURL         string
	Status          ProbeStatus
	FailureKind     BackendFailureKind
	Latency         time.Duration
	AvailableModels []string // populated on ModelNotFound when discoverable
	Detail          string   // human-readable detail / error context
}

// ProbeProvider performs a lightweight health check against the configured
// provider, distinguishing authentication, endpoint, and model failures.
// The context should carry a reasonable timeout (e.g. 10 s).
// For the "droid" provider, pass DroidConfig to resolve the binary path.
func ProbeProvider(ctx context.Context, cfg ProviderConfig, droidCfg DroidConfig) ProbeResult {
	base := ProbeResult{Provider: cfg.Name, Backend: cfg.Name, Model: cfg.Model, BaseURL: cfg.BaseURL}

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
			res.FailureKind = BackendFailureUnavailable
			res.Detail = "no base_url configured for custom provider"
			return res
		}
	}
	res.BaseURL = baseURL

	modelsURL := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		res.Status = ProbeEndpointUnreachable
		res.FailureKind = BackendFailureUnavailable
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
		res.FailureKind = BackendFailureUnavailable
		res.Detail = fmt.Sprintf("endpoint unreachable: %v", err)
		return res
	}
	defer resp.Body.Close()
	res.Latency = latency

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		res.Status = ProbeAuthFailed
		res.FailureKind = BackendFailureAuth
		res.Detail = "authentication failed (check API key)"
		return res
	}
	if status := probeStatusForHTTP(resp.StatusCode, string(body)); status != ProbeOK {
		res.Status = status
		res.FailureKind = failureKindForProbeStatus(status)
		res.Detail = fmt.Sprintf("unexpected status %d: %s", resp.StatusCode, truncate(string(body), 200))
		return res
	}

	// Parse the model list and check if configured model exists.
	models := parseOpenAIModelList(body)
	if len(models) == 0 {
		res.Status = ProbeEndpointUnreachable
		res.FailureKind = BackendFailureUnavailable
		res.Detail = "endpoint returned 200 but model list could not be parsed"
		return res
	}
	if cfg.Model != "" {
		found := false
		for _, m := range models {
			if m == cfg.Model {
				found = true
				break
			}
		}
		if !found {
			res.Status = ProbeModelNotFound
			res.FailureKind = BackendFailureModel
			res.AvailableModels = models
			res.Detail = fmt.Sprintf("model %q not found", cfg.Model)
			return res
		}
	}

	res.Status = ProbeOK
	res.FailureKind = BackendFailureNone
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
	res.BaseURL = cfg.BaseURL

	// Resolve API key (supports OAuth).
	tmpClient := NewAnthropicClient(cfg)
	apiKey, err := tmpClient.resolveAPIKey(ctx)
	if err != nil {
		res.Status = ProbeAuthFailed
		res.FailureKind = BackendFailureAuth
		res.Detail = fmt.Sprintf("authentication failed: %v", err)
		return res
	}

	// Use the lightweight GET /v1/models endpoint to validate auth and
	// reachability without consuming any API credits.
	modelsURL := strings.TrimRight(cfg.BaseURL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		res.Status = ProbeEndpointUnreachable
		res.FailureKind = BackendFailureUnavailable
		res.Detail = fmt.Sprintf("invalid URL: %v", err)
		return res
	}
	req.Header.Set("anthropic-version", anthropicVersionHeader)
	switch {
	case isOAuthAccessToken(apiKey):
		req.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(apiKey, "oauth:"))
	case isOAuthSetupToken(apiKey):
		req.Header.Set("Authorization", "Bearer "+apiKey)
	default:
		req.Header.Set("x-api-key", apiKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		res.Status = ProbeEndpointUnreachable
		res.FailureKind = BackendFailureUnavailable
		res.Detail = fmt.Sprintf("endpoint unreachable: %v", err)
		return res
	}
	defer resp.Body.Close()
	res.Latency = latency

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		res.Status = ProbeAuthFailed
		res.FailureKind = BackendFailureAuth
		res.Detail = "authentication failed (check API key)"
		return res
	}
	if status := probeStatusForHTTP(resp.StatusCode, string(body)); status != ProbeOK {
		res.Status = status
		res.FailureKind = failureKindForProbeStatus(status)
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
			res.FailureKind = BackendFailureModel
			res.AvailableModels = modelsToCheck
			res.Detail = fmt.Sprintf("model %q not found", cfg.Model)
			return res
		}
	}

	res.Status = ProbeOK
	res.FailureKind = BackendFailureNone
	res.Detail = fmt.Sprintf("ok (model %s, latency %dms)", cfg.Model, latency.Milliseconds())
	return res
}

// ---------- ChatGPT (Codex Responses API) ----------

func probeChatGPT(ctx context.Context, res ProbeResult, cfg ProviderConfig) ProbeResult {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://chatgpt.com/backend-api"
	}
	res.BaseURL = cfg.BaseURL

	// Resolve the same Codex-owned credentials used by ChatGPTClient. This keeps
	// doctor/preflight aligned with runtime auth instead of treating an empty
	// legacy api_key as an invalid bearer token.
	auth := newChatGPTAuthManager(cfg)
	creds, err := auth.credentials(ctx)
	if err != nil {
		res.Status = ProbeAuthFailed
		res.FailureKind = BackendFailureAuth
		res.Detail = fmt.Sprintf("authentication failed: %v", err)
		return res
	}

	pingURL := strings.TrimRight(cfg.BaseURL, "/") + "/models"
	client := &http.Client{Timeout: 10 * time.Second}
	send := func(creds chatGPTCredentials) (*http.Response, error) {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, pingURL, nil)
		if requestErr != nil {
			return nil, requestErr
		}
		req.Header.Set("Authorization", "Bearer "+creds.accessToken)
		if creds.accountID != "" {
			req.Header.Set("ChatGPT-Account-ID", creds.accountID)
		}
		req.Header.Set("User-Agent", "ok-gobot")
		return client.Do(req)
	}

	start := time.Now()
	resp, err := send(creds)
	latency := time.Since(start)
	if err != nil {
		res.Status = ProbeEndpointUnreachable
		res.FailureKind = BackendFailureUnavailable
		if strings.Contains(err.Error(), "unsupported protocol scheme") || strings.Contains(err.Error(), "missing protocol scheme") {
			res.Detail = fmt.Sprintf("invalid URL: %v", err)
		} else {
			res.Detail = fmt.Sprintf("endpoint unreachable: %v", err)
		}
		return res
	}
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && !creds.static {
		// The probe pings /models, which the runtime never uses; the Codex
		// backend has been seen rejecting there while accepting the same
		// token on /codex/responses (2026-08-15 outage). credentials() only
		// returns unexpired tokens, so a rejection here is inconclusive:
		// report healthy without burning a billed CLI refresh — the runtime
		// path has its own 401-refresh and surfaces real auth failures per
		// request, and genuinely broken auth (unreadable cache, expired
		// token that will not refresh) already failed inside credentials().
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		res.Latency = latency
		return chatGPTCatalogCheck(res, cfg, "auth probe inconclusive: models endpoint rejected an unexpired token (runtime uses /codex/responses)")
	}
	defer resp.Body.Close()
	res.Latency = latency

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		res.Status = ProbeAuthFailed
		res.FailureKind = BackendFailureAuth
		res.Detail = "authentication failed (check API key)"
		return res
	}

	body, _ := io.ReadAll(resp.Body)

	if status := probeStatusForHTTP(resp.StatusCode, string(body)); status != ProbeOK {
		res.Status = status
		res.FailureKind = failureKindForProbeStatus(status)
		res.Detail = fmt.Sprintf("unexpected status %d: %s", resp.StatusCode, truncate(string(body), 200))
		return res
	}

	return chatGPTCatalogCheck(res, cfg, fmt.Sprintf("ok (model %s, latency %dms)", cfg.Model, latency.Milliseconds()))
}

// chatGPTCatalogCheck validates cfg.Model against the static catalog and
// finalizes the probe result. It runs on inconclusive-auth results too, so a
// misconfigured model is still caught locally even when /models is down.
func chatGPTCatalogCheck(res ProbeResult, cfg ProviderConfig, okDetail string) ProbeResult {
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
			res.FailureKind = BackendFailureModel
			res.AvailableModels = knownModels
			res.Detail = fmt.Sprintf("model %q not in known catalog", cfg.Model)
			return res
		}
	}

	res.Status = ProbeOK
	res.FailureKind = BackendFailureNone
	res.Detail = okDetail
	return res
}

// ---------- Droid (subprocess) ----------

func probeDroid(res ProbeResult, cfg ProviderConfig, droidCfg DroidConfig) ProbeResult {
	binary := droidCfg.BinaryPath
	if binary == "" {
		binary = "droid"
	}
	backend := worker.DetectBackend(binary)
	res.Backend = backend

	if _, err := exec.LookPath(binary); err != nil {
		res.Status = ProbeEndpointUnreachable
		res.FailureKind = BackendFailureUnavailable
		res.Detail = fmt.Sprintf("%s binary not found: %s", backend, binary)
		return res
	}

	// Check model against a known catalog only for backends with stable catalogs.
	knownModels := knownCLIBackendModels(backend)
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
			res.FailureKind = BackendFailureModel
			res.AvailableModels = knownModels
			res.Detail = fmt.Sprintf("model %q not in known %s catalog", cfg.Model, backend)
			return res
		}
	}

	res.Status = ProbeOK
	res.FailureKind = BackendFailureNone
	res.Detail = fmt.Sprintf("ok (backend %s, binary %s, model %s)", backend, binary, cfg.Model)
	return res
}

// ---------- helpers ----------

func knownCLIBackendModels(backend string) []string {
	switch backend {
	case "droid":
		return AvailableModels()["droid"]
	case "claude":
		return AvailableModels()["anthropic"]
	default:
		return nil
	}
}

func probeStatusForHTTP(statusCode int, body string) ProbeStatus {
	if statusCode == http.StatusOK {
		return ProbeOK
	}
	kind := failureKindForHTTP(statusCode, strings.ToLower(body))
	switch kind {
	case BackendFailureAuth:
		return ProbeAuthFailed
	case BackendFailureQuota:
		return ProbeQuotaFailed
	case BackendFailureRateLimit:
		return ProbeRateLimited
	case BackendFailureToolMissing:
		return ProbeToolMissing
	case BackendFailureModel:
		return ProbeModelNotFound
	default:
		return ProbeEndpointUnreachable
	}
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
