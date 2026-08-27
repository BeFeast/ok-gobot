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

// BackendHTTPError carries an upstream HTTP status independently of its
// human-readable error text. Classifiers must not depend on provider-specific
// prose to recover the status code.
type BackendHTTPError struct {
	Provider   string
	StatusCode int
	Detail     string
}

func (e *BackendHTTPError) Error() string {
	if e == nil {
		return "backend HTTP error"
	}
	prefix := strings.TrimSpace(e.Provider)
	if prefix == "" {
		prefix = "API"
	} else {
		prefix += " API"
	}
	if detail := strings.TrimSpace(e.Detail); detail != "" {
		return fmt.Sprintf("%s error (status %d): %s", prefix, e.StatusCode, detail)
	}
	return fmt.Sprintf("%s error (status %d)", prefix, e.StatusCode)
}

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

// BackendRuntimeOutcome describes one real provider attempt. It is emitted at
// the client boundary, before agent/tool fallbacks can hide the upstream
// result. Canceled is true only when the caller's context ended the attempt.
type BackendRuntimeOutcome struct {
	Identity     BackendIdentity
	Err          error
	FinishReason string
	Canceled     bool
	Latency      time.Duration
}

// BackendOutcomeReporter records real provider attempts for health surfaces.
type BackendOutcomeReporter func(BackendRuntimeOutcome)

// ErrBackendStreamIncomplete marks a stream that delivered partial content but
// did not reach a normal terminal event.
var ErrBackendStreamIncomplete = errors.New("backend stream ended incomplete")

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

	mu     sync.Mutex
	cache  map[string]cachedBackendHealth
	latest BackendHealth
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
	if err := callerCancellation(ctx); err != nil {
		return BackendHealth{}, err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(p.cfg.Provider.Model)
	}
	tier = strings.TrimSpace(tier)
	effort = strings.TrimSpace(effort)

	cacheKey := backendHealthCacheKey(model, tier, effort)
	if health, ok := p.cached(cacheKey); ok {
		if err := callerCancellation(ctx); err != nil {
			return BackendHealth{}, err
		}
		p.setLatest(health)
		if health.OK() {
			return health, nil
		}
		return health, backendPreflightError(health)
	}

	order := fallbackOrder(model, p.cfg.FallbackModels)
	primaryProbe := p.probe(ctx, model)
	if err := callerCancellation(ctx); err != nil {
		return BackendHealth{}, err
	}
	primaryIdentity := p.identity(primaryProbe, model, tier, effort, order)
	if primaryProbe.Status == ProbeOK {
		health := backendHealthFromProbe(primaryProbe, primaryIdentity, FallbackDecision{
			Action:  FallbackActionPrimary,
			Enabled: p.cfg.FallbackEnabled && len(order) > 1,
			Order:   order,
			Reason:  "primary backend healthy",
		}, p.cfg.Now())
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
			if err := callerCancellation(ctx); err != nil {
				return BackendHealth{}, err
			}
			identity := p.identity(probe, fallbackModel, tier, effort, order)
			decision.ToModel = fallbackModel
			if probe.Status == ProbeOK {
				health := backendHealthFromProbe(probe, identity, decision, p.cfg.Now())
				p.store(cacheKey, health)
				return health, nil
			}
			last = backendHealthFromProbe(probe, identity, decision, p.cfg.Now())
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

	health := backendHealthFromProbe(primaryProbe, primaryIdentity, decision, p.cfg.Now())
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
		if ok {
			delete(p.cache, key)
		}
		return BackendHealth{}, false
	}
	return cloneBackendHealth(cached.health), true
}

func (p *BackendPreflight) store(key string, health BackendHealth) {
	p.mu.Lock()
	health = cloneBackendHealth(health)
	p.cache[key] = cachedBackendHealth{health: health, expiresAt: p.cfg.Now().Add(p.cfg.CacheTTL)}
	p.latest = health
	p.mu.Unlock()
}

func (p *BackendPreflight) setLatest(health BackendHealth) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.latest = cloneBackendHealth(health)
	p.mu.Unlock()
}

// Snapshot returns the freshest preflight or real-runtime health observation.
func (p *BackendPreflight) Snapshot() BackendHealth {
	if p == nil {
		return BackendHealth{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneBackendHealth(p.latest)
}

// RecordRuntimeOutcome feeds one actual provider attempt back into health.
// Failures invalidate every cached preflight involving that model so the next
// run cannot reuse a stale healthy result. A successful attempt refreshes the
// matching cache entries and becomes the latest status snapshot.
func (p *BackendPreflight) RecordRuntimeOutcome(outcome BackendRuntimeOutcome) {
	if p == nil || outcome.Canceled {
		return
	}

	identity := p.runtimeIdentity(outcome.Identity)
	order := identity.FallbackOrder
	if len(order) == 0 {
		order = fallbackOrder(identity.Model, p.cfg.FallbackModels)
		identity.FallbackOrder = append([]string(nil), order...)
	}

	now := p.cfg.Now()
	health := BackendHealth{
		Identity:  identity,
		LatencyMS: outcome.Latency.Milliseconds(),
		CheckedAt: now.UTC().Format(time.RFC3339),
	}

	err := outcome.Err
	if err == nil && outcome.FinishReason == "incomplete" {
		err = ErrBackendStreamIncomplete
	}
	if err == nil {
		health.Status = BackendHealthHealthy
		health.FailureKind = BackendFailureNone
		health.Detail = "runtime request succeeded"
		health.Fallback = runtimeSuccessDecision(identity.Model, order, p.cfg.FallbackEnabled)
		p.recordRuntimeSuccess(health)
		return
	}

	kind := ClassifyBackendError(err)
	if errors.Is(err, context.Canceled) {
		// The caller-canceled case returned above. A cancellation emitted by the
		// provider while the caller is still active is a real backend failure.
		kind = BackendFailureUnavailable
	}
	nextModel := nextModelInOrder(identity.Model, order)
	decision := DecideFallback(kind, p.cfg.FallbackEnabled && nextModel != "", identity.Model, order)
	if decision.Action == FallbackActionFallback {
		decision.ToModel = nextModel
	} else if p.cfg.FallbackEnabled && len(order) > 1 && nextModel == "" && fallbackableFailure(kind) {
		decision.Reason = "fallback order exhausted"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		decision.Action = FallbackActionStop
		decision.ToModel = ""
		decision.Reason = "context ended; fallback suppressed"
	}
	if outcome.FinishReason == "incomplete" {
		kind = BackendFailureUnavailable
		decision.FailureKind = kind
		decision.Action = FallbackActionStop
		decision.ToModel = ""
		decision.Reason = "partial response already emitted; fallback suppressed"
	}
	health.Status = backendStatusForRuntimeFailure(kind, decision)
	health.FailureKind = kind
	health.Detail = truncate(strings.TrimSpace(err.Error()), 200)
	health.Fallback = decision
	p.recordRuntimeFailure(health)
}

func callerCancellation(ctx context.Context) error {
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	return nil
}

func (p *BackendPreflight) runtimeIdentity(identity BackendIdentity) BackendIdentity {
	identity.Provider = strings.TrimSpace(identity.Provider)
	if identity.Provider == "" {
		identity.Provider = strings.TrimSpace(p.cfg.Provider.Name)
	}
	identity.Backend = strings.TrimSpace(identity.Backend)
	if identity.Backend == "" {
		identity.Backend = identity.Provider
	}
	identity.Model = strings.TrimSpace(identity.Model)
	if identity.Model == "" {
		identity.Model = strings.TrimSpace(p.cfg.Provider.Model)
	}
	identity.BaseURL = strings.TrimSpace(identity.BaseURL)
	if identity.BaseURL == "" {
		identity.BaseURL = strings.TrimSpace(p.cfg.Provider.BaseURL)
	}
	identity.FallbackOrder = append([]string(nil), identity.FallbackOrder...)
	return identity
}

func (p *BackendPreflight) recordRuntimeFailure(health BackendHealth) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, cached := range p.cache {
		if backendHealthCacheModel(key) == health.Identity.Model || cached.health.Identity.Model == health.Identity.Model {
			delete(p.cache, key)
		}
	}
	p.latest = cloneBackendHealth(health)
}

func (p *BackendPreflight) recordRuntimeSuccess(health BackendHealth) {
	p.mu.Lock()
	defer p.mu.Unlock()
	expiresAt := p.cfg.Now().Add(p.cfg.CacheTTL)
	refreshed := false
	for key, cached := range p.cache {
		if backendHealthCacheModel(key) != health.Identity.Model && cached.health.Identity.Model != health.Identity.Model {
			continue
		}
		updated := cloneBackendHealth(health)
		if cached.health.Identity.Tier != "" {
			updated.Identity.Tier = cached.health.Identity.Tier
		}
		if cached.health.Identity.Effort != "" {
			updated.Identity.Effort = cached.health.Identity.Effort
		}
		p.cache[key] = cachedBackendHealth{health: updated, expiresAt: expiresAt}
		refreshed = true
	}
	if !refreshed {
		key := backendHealthCacheKey(health.Identity.Model, health.Identity.Tier, health.Identity.Effort)
		p.cache[key] = cachedBackendHealth{health: cloneBackendHealth(health), expiresAt: expiresAt}
	}
	p.latest = cloneBackendHealth(health)
}

func backendHealthCacheKey(model, tier, effort string) string {
	return strings.Join([]string{strings.TrimSpace(model), strings.TrimSpace(tier), strings.TrimSpace(effort)}, "\x00")
}

func backendHealthCacheModel(key string) string {
	model, _, _ := strings.Cut(key, "\x00")
	return model
}

func cloneBackendHealth(health BackendHealth) BackendHealth {
	health.Identity.FallbackOrder = append([]string(nil), health.Identity.FallbackOrder...)
	health.Fallback.Order = append([]string(nil), health.Fallback.Order...)
	return health
}

func runtimeSuccessDecision(model string, order []string, enabled bool) FallbackDecision {
	decision := FallbackDecision{
		Action:    FallbackActionPrimary,
		Enabled:   enabled && len(order) > 1,
		FromModel: model,
		Order:     append([]string(nil), order...),
		Reason:    "runtime request succeeded",
	}
	if len(order) > 0 && model != order[0] {
		decision.Action = FallbackActionFallback
		decision.FromModel = order[0]
		decision.ToModel = model
		decision.Reason = "runtime fallback succeeded"
	}
	return decision
}

func nextModelInOrder(current string, order []string) string {
	for i, model := range order {
		if model == current && i+1 < len(order) {
			return order[i+1]
		}
	}
	return ""
}

func backendStatusForRuntimeFailure(kind BackendFailureKind, decision FallbackDecision) BackendHealthStatus {
	if decision.Action == FallbackActionApproval {
		return BackendHealthApprovalRequired
	}
	switch kind {
	case BackendFailureAuth:
		return BackendHealthAuthFailed
	case BackendFailureQuota:
		return BackendHealthQuotaFailed
	case BackendFailureRateLimit:
		return BackendHealthRateLimited
	case BackendFailureToolMissing:
		return BackendHealthToolMissing
	case BackendFailureModel:
		return BackendHealthModelMissing
	default:
		return BackendHealthUnavailable
	}
}

func backendHealthFromProbe(probe ProbeResult, identity BackendIdentity, decision FallbackDecision, checkedAt time.Time) BackendHealth {
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
		CheckedAt:   checkedAt.UTC().Format(time.RFC3339),
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
	var httpErr *BackendHTTPError
	if errors.As(err, &httpErr) && httpErr != nil {
		return failureKindForHTTP(httpErr.StatusCode, strings.ToLower(httpErr.Detail))
	}
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
