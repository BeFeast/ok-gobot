package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// BackendFailureKind is the normalized reason a backend/model is not usable.
type BackendFailureKind string

const (
	BackendFailureNone        BackendFailureKind = ""
	BackendFailureUnavailable BackendFailureKind = "unavailable"
	BackendFailureAuth        BackendFailureKind = "auth"
	BackendFailureQuota       BackendFailureKind = "quota"
	BackendFailureRateLimit   BackendFailureKind = "rate_limit"
	BackendFailureToolMissing BackendFailureKind = "tool_missing"
	BackendFailureModel       BackendFailureKind = "model_missing"
	BackendFailureUnknown     BackendFailureKind = "unknown"
)

// BackendHealthStatus is the health state shown in status and Mission Control.
type BackendHealthStatus string

const (
	BackendHealthHealthy          BackendHealthStatus = "healthy"
	BackendHealthUnavailable      BackendHealthStatus = "unavailable"
	BackendHealthAuthFailed       BackendHealthStatus = "auth_failed"
	BackendHealthQuotaFailed      BackendHealthStatus = "quota_failed"
	BackendHealthRateLimited      BackendHealthStatus = "rate_limited"
	BackendHealthToolMissing      BackendHealthStatus = "tool_missing"
	BackendHealthModelMissing     BackendHealthStatus = "model_missing"
	BackendHealthApprovalRequired BackendHealthStatus = "approval_required"
	BackendHealthSkipped          BackendHealthStatus = "skipped"
)

// BackendIdentity describes the concrete backend/model selected for a run.
type BackendIdentity struct {
	Provider      string   `json:"provider"`
	Backend       string   `json:"backend,omitempty"`
	Model         string   `json:"model"`
	Tier          string   `json:"tier,omitempty"`
	Effort        string   `json:"effort,omitempty"`
	BaseURL       string   `json:"base_url,omitempty"`
	FallbackOrder []string `json:"fallback_order,omitempty"`
}

// String returns a compact identity suitable for logs.
func (id BackendIdentity) String() string {
	parts := []string{}
	if id.Provider != "" {
		parts = append(parts, "provider="+id.Provider)
	}
	if id.Backend != "" {
		parts = append(parts, "backend="+id.Backend)
	}
	if id.Model != "" {
		parts = append(parts, "model="+id.Model)
	}
	if id.Tier != "" {
		parts = append(parts, "tier="+id.Tier)
	}
	if id.Effort != "" {
		parts = append(parts, "effort="+id.Effort)
	}
	return strings.Join(parts, " ")
}

// FallbackAction is the policy action selected for a backend failure.
type FallbackAction string

const (
	FallbackActionPrimary  FallbackAction = "primary"
	FallbackActionFallback FallbackAction = "fallback"
	FallbackActionStop     FallbackAction = "stop"
	FallbackActionApproval FallbackAction = "approval_required"
)

// FallbackDecision records why a backend was used, skipped, or blocked.
type FallbackDecision struct {
	Action      FallbackAction     `json:"action"`
	Enabled     bool               `json:"enabled"`
	FailureKind BackendFailureKind `json:"failure_kind,omitempty"`
	FromModel   string             `json:"from_model,omitempty"`
	ToModel     string             `json:"to_model,omitempty"`
	Order       []string           `json:"order,omitempty"`
	Reason      string             `json:"reason,omitempty"`
}

// BackendHealth is the health snapshot exposed to runtime/status surfaces.
type BackendHealth struct {
	Identity    BackendIdentity     `json:"identity"`
	Status      BackendHealthStatus `json:"status"`
	FailureKind BackendFailureKind  `json:"failure_kind,omitempty"`
	Detail      string              `json:"detail,omitempty"`
	LatencyMS   int64               `json:"latency_ms,omitempty"`
	CheckedAt   string              `json:"checked_at,omitempty"`
	Fallback    FallbackDecision    `json:"fallback"`
}

// OK reports whether this health snapshot allows a run to start.
func (h BackendHealth) OK() bool {
	return h.Status == BackendHealthHealthy
}

// BackendPreflightConfig configures backend/model checks.
type BackendPreflightConfig struct {
	Provider        ProviderConfig
	Droid           DroidConfig
	FallbackModels  []string
	FallbackEnabled bool
	CacheTTL        time.Duration
	Now             func() time.Time
	Probe           func(context.Context, ProviderConfig, DroidConfig) ProbeResult
}

// BackendPreflight probes and caches backend/model health before runs start.
type BackendPreflight struct {
	cfg BackendPreflightConfig

	mu    sync.Mutex
	cache map[string]cachedBackendHealth
}

type cachedBackendHealth struct {
	health    BackendHealth
	expiresAt time.Time
}

// NewBackendPreflight creates a reusable backend health checker.
func NewBackendPreflight(cfg BackendPreflightConfig) *BackendPreflight {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Probe == nil {
		cfg.Probe = ProbeProvider
	}
	return &BackendPreflight{cfg: cfg, cache: make(map[string]cachedBackendHealth)}
}

// Check verifies model health and returns the selected identity. If fallback is
// used, the returned identity points at the fallback model that should be used.
func (p *BackendPreflight) Check(ctx context.Context, model, tier, effort string) (BackendHealth, error) {
	if p == nil {
		return BackendHealth{}, fmt.Errorf("backend preflight is not configured")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(p.cfg.Provider.Model)
	}
	tier = strings.TrimSpace(tier)
	effort = strings.TrimSpace(effort)

	cacheKey := strings.Join([]string{model, tier, effort}, "\x00")
	if health, ok := p.cached(cacheKey); ok {
		if health.OK() {
			return health, nil
		}
		return health, backendPreflightError(health)
	}

	order := fallbackOrder(model, p.cfg.FallbackModels)
	primaryProbe := p.probe(ctx, model)
	primaryIdentity := p.identity(primaryProbe, model, tier, effort, order)
	if primaryProbe.Status == ProbeOK {
		health := backendHealthFromProbe(primaryProbe, primaryIdentity, FallbackDecision{
			Action:  FallbackActionPrimary,
			Enabled: p.cfg.FallbackEnabled && len(order) > 1,
			Order:   order,
			Reason:  "primary backend healthy",
		})
		p.store(cacheKey, health)
		return health, nil
	}

	kind := primaryProbe.FailureKind
	if kind == "" {
		kind = failureKindForProbeStatus(primaryProbe.Status)
	}
	decision := DecideFallback(kind, p.cfg.FallbackEnabled && len(order) > 1, model, order)
	if decision.Action == FallbackActionFallback {
		var last BackendHealth
		for _, fallbackModel := range order[1:] {
			probe := p.probe(ctx, fallbackModel)
			identity := p.identity(probe, fallbackModel, tier, effort, order)
			decision.ToModel = fallbackModel
			if probe.Status == ProbeOK {
				health := backendHealthFromProbe(probe, identity, decision)
				p.store(cacheKey, health)
				return health, nil
			}
			last = backendHealthFromProbe(probe, identity, decision)
			last.Detail = strings.TrimSpace(last.Detail)
			if !fallbackableFailure(last.FailureKind) {
				break
			}
		}
		if last.Status != "" {
			last.Fallback.Action = FallbackActionStop
			if last.Fallback.Reason == "" {
				last.Fallback.Reason = "fallback order exhausted"
			}
			p.store(cacheKey, last)
			return last, backendPreflightError(last)
		}
	}

	health := backendHealthFromProbe(primaryProbe, primaryIdentity, decision)
	if decision.Action == FallbackActionApproval {
		health.Status = BackendHealthApprovalRequired
	}
	p.store(cacheKey, health)
	return health, backendPreflightError(health)
}

func (p *BackendPreflight) probe(ctx context.Context, model string) ProbeResult {
	cfg := p.cfg.Provider
	cfg.Model = model
	return p.cfg.Probe(ctx, cfg, p.cfg.Droid)
}

func (p *BackendPreflight) identity(probe ProbeResult, model, tier, effort string, order []string) BackendIdentity {
	provider := strings.TrimSpace(p.cfg.Provider.Name)
	if probe.Provider != "" {
		provider = probe.Provider
	}
	backend := strings.TrimSpace(probe.Backend)
	if backend == "" {
		backend = provider
	}
	baseURL := strings.TrimSpace(p.cfg.Provider.BaseURL)
	if probe.BaseURL != "" {
		baseURL = probe.BaseURL
	}
	return BackendIdentity{
		Provider:      provider,
		Backend:       backend,
		Model:         model,
		Tier:          tier,
		Effort:        effort,
		BaseURL:       baseURL,
		FallbackOrder: append([]string(nil), order...),
	}
}

func (p *BackendPreflight) cached(key string) (BackendHealth, bool) {
	now := p.cfg.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	cached, ok := p.cache[key]
	if !ok || now.After(cached.expiresAt) {
		return BackendHealth{}, false
	}
	return cached.health, true
}

func (p *BackendPreflight) store(key string, health BackendHealth) {
	p.mu.Lock()
	p.cache[key] = cachedBackendHealth{health: health, expiresAt: p.cfg.Now().Add(p.cfg.CacheTTL)}
	p.mu.Unlock()
}

func backendHealthFromProbe(probe ProbeResult, identity BackendIdentity, decision FallbackDecision) BackendHealth {
	kind := probe.FailureKind
	if kind == "" {
		kind = failureKindForProbeStatus(probe.Status)
	}
	return BackendHealth{
		Identity:    identity,
		Status:      backendStatusForProbe(probe.Status),
		FailureKind: kind,
		Detail:      strings.TrimSpace(probe.Detail),
		LatencyMS:   probe.Latency.Milliseconds(),
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
		Fallback:    decision,
	}
}

func backendPreflightError(health BackendHealth) error {
	if health.OK() {
		return nil
	}
	reason := health.Fallback.Reason
	if reason == "" {
		reason = health.Detail
	}
	if reason == "" {
		reason = string(health.Status)
	}
	return fmt.Errorf("%s backend preflight failed for model %q: %s", health.Identity.Provider, health.Identity.Model, reason)
}

// DecideFallback encodes the runtime fallback policy.
func DecideFallback(kind BackendFailureKind, enabled bool, fromModel string, order []string) FallbackDecision {
	decision := FallbackDecision{
		Enabled:     enabled,
		FailureKind: kind,
		FromModel:   fromModel,
		Order:       append([]string(nil), order...),
	}
	if kind == BackendFailureNone {
		decision.Action = FallbackActionPrimary
		decision.Reason = "primary backend healthy"
		return decision
	}
	if !enabled || len(order) <= 1 {
		decision.Action = FallbackActionStop
		decision.Reason = "fallback disabled"
		return decision
	}
	switch kind {
	case BackendFailureUnavailable, BackendFailureRateLimit:
		decision.Action = FallbackActionFallback
		decision.Reason = fmt.Sprintf("%s is fallbackable", kind)
		return decision
	case BackendFailureAuth, BackendFailureQuota, BackendFailureToolMissing:
		decision.Action = FallbackActionStop
		decision.Reason = fmt.Sprintf("%s requires operator action", kind)
		return decision
	case BackendFailureModel, BackendFailureUnknown:
		decision.Action = FallbackActionApproval
		decision.Reason = fmt.Sprintf("%s requires approval before changing model", kind)
		return decision
	default:
		decision.Action = FallbackActionApproval
		decision.Reason = "unclassified backend failure requires approval"
		return decision
	}
}

// ClassifyBackendError normalizes provider and CLI errors for fallback policy.
func ClassifyBackendError(err error) BackendFailureKind {
	if err == nil {
		return BackendFailureNone
	}
	msg := strings.ToLower(err.Error())
	var statusCode int
	if _, scanErr := fmt.Sscanf(err.Error(), "API error (status %d):", &statusCode); scanErr == nil {
		return failureKindForHTTP(statusCode, msg)
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return BackendFailureUnavailable
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return BackendFailureUnavailable
	}
	switch {
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden"), strings.Contains(msg, "invalid api key"), strings.Contains(msg, "authentication failed"), strings.Contains(msg, "invalid x-api-key"):
		return BackendFailureAuth
	case strings.Contains(msg, "insufficient_quota"), strings.Contains(msg, "quota exceeded"), strings.Contains(msg, "billing hard limit"), strings.Contains(msg, "credit balance"):
		return BackendFailureQuota
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many requests"), strings.Contains(msg, "429"):
		return BackendFailureRateLimit
	case strings.Contains(msg, "tool not found"), strings.Contains(msg, "missing tool"), strings.Contains(msg, "tools unsupported"), strings.Contains(msg, "tool use not supported"), strings.Contains(msg, "unknown tool"):
		return BackendFailureToolMissing
	case strings.Contains(msg, "context_length_exceeded"), strings.Contains(msg, "context length"):
		return BackendFailureUnavailable
	case strings.Contains(msg, "empty model output"):
		return BackendFailureUnavailable
	case strings.Contains(msg, "model not found"), strings.Contains(msg, "unknown model"), strings.Contains(msg, "does not exist"):
		return BackendFailureModel
	case strings.Contains(msg, "connection reset"), strings.Contains(msg, "tls handshake"), strings.Contains(msg, "no such host"), strings.Contains(msg, "binary not found"), strings.Contains(msg, "executable file not found"):
		return BackendFailureUnavailable
	default:
		return BackendFailureUnknown
	}
}

func fallbackOrder(primary string, fallbacks []string) []string {
	seen := map[string]struct{}{}
	order := []string{}
	for _, model := range append([]string{primary}, fallbacks...) {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		order = append(order, model)
	}
	return order
}

func fallbackableFailure(kind BackendFailureKind) bool {
	return kind == BackendFailureUnavailable || kind == BackendFailureRateLimit
}

func backendStatusForProbe(status ProbeStatus) BackendHealthStatus {
	switch status {
	case ProbeOK:
		return BackendHealthHealthy
	case ProbeAuthFailed:
		return BackendHealthAuthFailed
	case ProbeQuotaFailed:
		return BackendHealthQuotaFailed
	case ProbeRateLimited:
		return BackendHealthRateLimited
	case ProbeToolMissing:
		return BackendHealthToolMissing
	case ProbeModelNotFound:
		return BackendHealthModelMissing
	case ProbeSkipped:
		return BackendHealthSkipped
	default:
		return BackendHealthUnavailable
	}
}

func failureKindForProbeStatus(status ProbeStatus) BackendFailureKind {
	switch status {
	case ProbeOK:
		return BackendFailureNone
	case ProbeAuthFailed:
		return BackendFailureAuth
	case ProbeQuotaFailed:
		return BackendFailureQuota
	case ProbeRateLimited:
		return BackendFailureRateLimit
	case ProbeToolMissing:
		return BackendFailureToolMissing
	case ProbeModelNotFound:
		return BackendFailureModel
	case ProbeEndpointUnreachable:
		return BackendFailureUnavailable
	default:
		return BackendFailureUnknown
	}
}

func failureKindForHTTP(statusCode int, body string) BackendFailureKind {
	switch statusCode {
	case 401, 403:
		return BackendFailureAuth
	case 402:
		return BackendFailureQuota
	case 429:
		if strings.Contains(body, "insufficient_quota") || strings.Contains(body, "quota") {
			return BackendFailureQuota
		}
		return BackendFailureRateLimit
	case 404:
		return BackendFailureModel
	case 500, 502, 503, 504:
		return BackendFailureUnavailable
	}
	if strings.Contains(body, "tool") && (strings.Contains(body, "not found") || strings.Contains(body, "unsupported") || strings.Contains(body, "missing")) {
		return BackendFailureToolMissing
	}
	if strings.Contains(body, "context_length_exceeded") || strings.Contains(body, "context length") {
		return BackendFailureUnavailable
	}
	if strings.Contains(body, "insufficient_quota") || strings.Contains(body, "quota exceeded") {
		return BackendFailureQuota
	}
	if strings.Contains(body, "rate limit") || strings.Contains(body, "too many requests") {
		return BackendFailureRateLimit
	}
	return BackendFailureUnknown
}
