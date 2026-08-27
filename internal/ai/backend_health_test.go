package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackendPreflightHealthy(t *testing.T) {
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider: ProviderConfig{Name: "anthropic", Model: "claude-sonnet"},
		Probe: func(context.Context, ProviderConfig, DroidConfig) ProbeResult {
			return ProbeResult{Provider: "anthropic", Backend: "anthropic", Model: "claude-sonnet", Status: ProbeOK}
		},
	})

	health, err := checker.Check(context.Background(), "claude-sonnet", "premium", "high")
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if health.Status != BackendHealthHealthy {
		t.Fatalf("status=%s, want healthy", health.Status)
	}
	if health.Identity.Model != "claude-sonnet" || health.Identity.Tier != "premium" || health.Identity.Effort != "high" {
		t.Fatalf("identity not recorded: %+v", health.Identity)
	}
	if health.Fallback.Action != FallbackActionPrimary {
		t.Fatalf("fallback action=%s, want primary", health.Fallback.Action)
	}
}

func TestBackendPreflightUnavailableFallsBack(t *testing.T) {
	probes := map[string]ProbeStatus{
		"primary":  ProbeEndpointUnreachable,
		"fallback": ProbeOK,
	}
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider:        ProviderConfig{Name: "openai", Model: "primary"},
		FallbackModels:  []string{"fallback"},
		FallbackEnabled: true,
		Probe: func(_ context.Context, cfg ProviderConfig, _ DroidConfig) ProbeResult {
			status := probes[cfg.Model]
			return ProbeResult{Provider: "openai", Backend: "openai", Model: cfg.Model, Status: status, FailureKind: failureKindForProbeStatus(status), Detail: "probe result"}
		},
	})

	health, err := checker.Check(context.Background(), "primary", "standard", "medium")
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if health.Identity.Model != "fallback" {
		t.Fatalf("model=%q, want fallback", health.Identity.Model)
	}
	if health.Fallback.Action != FallbackActionFallback || health.Fallback.FromModel != "primary" || health.Fallback.ToModel != "fallback" {
		t.Fatalf("unexpected fallback decision: %+v", health.Fallback)
	}
}

func TestBackendPreflightAuthFailureStops(t *testing.T) {
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider:        ProviderConfig{Name: "openai", Model: "primary"},
		FallbackModels:  []string{"fallback"},
		FallbackEnabled: true,
		Probe: func(_ context.Context, cfg ProviderConfig, _ DroidConfig) ProbeResult {
			return ProbeResult{Provider: "openai", Backend: "openai", Model: cfg.Model, Status: ProbeAuthFailed, FailureKind: BackendFailureAuth, Detail: "bad key"}
		},
	})

	health, err := checker.Check(context.Background(), "primary", "standard", "off")
	if err == nil {
		t.Fatal("expected auth preflight error")
	}
	if health.Status != BackendHealthAuthFailed || health.Fallback.Action != FallbackActionStop {
		t.Fatalf("unexpected health=%+v", health)
	}
}

func TestBackendPreflightQuotaFailureStops(t *testing.T) {
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider:        ProviderConfig{Name: "openai", Model: "primary"},
		FallbackModels:  []string{"fallback"},
		FallbackEnabled: true,
		Probe: func(_ context.Context, cfg ProviderConfig, _ DroidConfig) ProbeResult {
			return ProbeResult{Provider: "openai", Backend: "openai", Model: cfg.Model, Status: ProbeQuotaFailed, FailureKind: BackendFailureQuota, Detail: "quota exceeded"}
		},
	})

	health, err := checker.Check(context.Background(), "primary", "standard", "off")
	if err == nil {
		t.Fatal("expected quota preflight error")
	}
	if health.FailureKind != BackendFailureQuota || health.Fallback.Action != FallbackActionStop {
		t.Fatalf("unexpected health=%+v", health)
	}
}

func TestBackendPreflightFallbackDisabled(t *testing.T) {
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider:        ProviderConfig{Name: "openai", Model: "primary"},
		FallbackModels:  []string{"fallback"},
		FallbackEnabled: false,
		Probe: func(_ context.Context, cfg ProviderConfig, _ DroidConfig) ProbeResult {
			return ProbeResult{Provider: "openai", Backend: "openai", Model: cfg.Model, Status: ProbeEndpointUnreachable, FailureKind: BackendFailureUnavailable, Detail: "down"}
		},
	})

	health, err := checker.Check(context.Background(), "primary", "standard", "off")
	if err == nil {
		t.Fatal("expected fallback-disabled preflight error")
	}
	if health.Fallback.Action != FallbackActionStop || !strings.Contains(health.Fallback.Reason, "disabled") {
		t.Fatalf("unexpected decision=%+v", health.Fallback)
	}
}

func TestBackendPreflightRuntimeFailureInvalidatesCachedHealthyCheck(t *testing.T) {
	var probeCalls atomic.Int32
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider: ProviderConfig{Name: "chatgpt", Model: "primary"},
		Probe: func(_ context.Context, cfg ProviderConfig, _ DroidConfig) ProbeResult {
			probeCalls.Add(1)
			return ProbeResult{Provider: "chatgpt", Backend: "chatgpt", Model: cfg.Model, Status: ProbeOK}
		},
	})

	identity := BackendIdentity{Provider: "chatgpt", Backend: "chatgpt", Model: "primary", Tier: "agent", Effort: "high"}
	if _, err := checker.Check(context.Background(), identity.Model, identity.Tier, identity.Effort); err != nil {
		t.Fatal(err)
	}
	if _, err := checker.Check(context.Background(), identity.Model, identity.Tier, identity.Effort); err != nil {
		t.Fatal(err)
	}
	if probeCalls.Load() != 1 {
		t.Fatalf("probe calls before runtime failure = %d, want cached result", probeCalls.Load())
	}

	checker.RecordRuntimeOutcome(BackendRuntimeOutcome{
		Identity: identity,
		Err:      &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503},
	})
	failed := checker.Snapshot()
	if failed.Status != BackendHealthUnavailable || failed.FailureKind != BackendFailureUnavailable {
		t.Fatalf("runtime failure snapshot = %+v", failed)
	}

	if _, err := checker.Check(context.Background(), identity.Model, identity.Tier, identity.Effort); err != nil {
		t.Fatal(err)
	}
	if probeCalls.Load() != 2 {
		t.Fatalf("probe calls after runtime failure = %d, want stale cache invalidated", probeCalls.Load())
	}
}

func TestBackendPreflightRuntimeSuccessRestoresAndRefreshesHealth(t *testing.T) {
	var probeCalls atomic.Int32
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider: ProviderConfig{Name: "chatgpt", Model: "primary"},
		Probe: func(_ context.Context, cfg ProviderConfig, _ DroidConfig) ProbeResult {
			probeCalls.Add(1)
			return ProbeResult{Provider: "chatgpt", Backend: "chatgpt", Model: cfg.Model, Status: ProbeOK}
		},
	})
	identity := BackendIdentity{Provider: "chatgpt", Backend: "chatgpt", Model: "primary", Tier: "agent", Effort: "high"}
	if _, err := checker.Check(context.Background(), identity.Model, identity.Tier, identity.Effort); err != nil {
		t.Fatal(err)
	}
	checker.RecordRuntimeOutcome(BackendRuntimeOutcome{Identity: identity, Err: &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}})
	checker.RecordRuntimeOutcome(BackendRuntimeOutcome{Identity: identity, Latency: 25 * time.Millisecond})

	health := checker.Snapshot()
	if health.Status != BackendHealthHealthy || health.LatencyMS != 25 || health.Fallback.Action != FallbackActionPrimary {
		t.Fatalf("restored health = %+v", health)
	}
	if _, err := checker.Check(context.Background(), identity.Model, identity.Tier, identity.Effort); err != nil {
		t.Fatal(err)
	}
	if probeCalls.Load() != 1 {
		t.Fatalf("probe calls = %d, want runtime success to refresh cache", probeCalls.Load())
	}
}

func TestBackendPreflightCallerCancelDoesNotPoisonHealthButDeadlineDoes(t *testing.T) {
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider: ProviderConfig{Name: "chatgpt", Model: "primary"},
		Probe: func(_ context.Context, cfg ProviderConfig, _ DroidConfig) ProbeResult {
			return ProbeResult{Provider: "chatgpt", Backend: "chatgpt", Model: cfg.Model, Status: ProbeOK}
		},
	})
	identity := BackendIdentity{Provider: "chatgpt", Backend: "chatgpt", Model: "primary", Tier: "agent", Effort: "high"}
	if _, err := checker.Check(context.Background(), identity.Model, identity.Tier, identity.Effort); err != nil {
		t.Fatal(err)
	}

	checker.RecordRuntimeOutcome(BackendRuntimeOutcome{Identity: identity, Err: context.Canceled, Canceled: true})
	if health := checker.Snapshot(); health.Status != BackendHealthHealthy {
		t.Fatalf("caller cancellation poisoned health: %+v", health)
	}
	checker.RecordRuntimeOutcome(BackendRuntimeOutcome{Identity: identity, Err: context.Canceled})
	if health := checker.Snapshot(); health.Status != BackendHealthUnavailable || health.FailureKind != BackendFailureUnavailable {
		t.Fatalf("provider cancellation outcome = %+v, want unavailable", health)
	}
	checker.RecordRuntimeOutcome(BackendRuntimeOutcome{Identity: identity})

	checker.RecordRuntimeOutcome(BackendRuntimeOutcome{Identity: identity, Err: context.DeadlineExceeded})
	if health := checker.Snapshot(); health.Status != BackendHealthUnavailable || health.FailureKind != BackendFailureUnavailable || health.Fallback.Action != FallbackActionStop {
		t.Fatalf("deadline outcome = %+v, want unavailable", health)
	}
}

func TestBackendPreflightCallerCancellationDoesNotPoisonProbeCache(t *testing.T) {
	var probeCalls atomic.Int32
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider: ProviderConfig{Name: "chatgpt", Model: "primary"},
		Probe: func(ctx context.Context, cfg ProviderConfig, _ DroidConfig) ProbeResult {
			probeCalls.Add(1)
			if ctx.Err() != nil {
				return ProbeResult{Provider: "chatgpt", Model: cfg.Model, Status: ProbeEndpointUnreachable}
			}
			return ProbeResult{Provider: "chatgpt", Model: cfg.Model, Status: ProbeOK}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := checker.Check(ctx, "primary", "agent", "high"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight error = %v, want context.Canceled", err)
	}
	if health := checker.Snapshot(); health.Status != "" {
		t.Fatalf("canceled preflight poisoned latest health: %+v", health)
	}
	if _, err := checker.Check(context.Background(), "primary", "agent", "high"); err != nil {
		t.Fatal(err)
	}
	if probeCalls.Load() != 1 {
		t.Fatalf("probe calls = %d, want only the live follow-up probe", probeCalls.Load())
	}
}

func TestClassifyBackendErrorDistinguishesFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want BackendFailureKind
	}{
		{"auth", errors.New("API error (status 401): unauthorized"), BackendFailureAuth},
		{"quota", errors.New("API error (status 402): insufficient_quota"), BackendFailureQuota},
		{"rate limit", errors.New("API error (status 429): rate limit exceeded"), BackendFailureRateLimit},
		{"typed unavailable", &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}, BackendFailureUnavailable},
		{"wrapped typed rate limit", fmt.Errorf("request failed: %w", &BackendHTTPError{Provider: "ChatGPT", StatusCode: 429}), BackendFailureRateLimit},
		{"tool missing", errors.New("tool not found: browser"), BackendFailureToolMissing},
		{"unavailable", errors.New("request failed: no such host"), BackendFailureUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyBackendError(tt.err); got != tt.want {
				t.Fatalf("ClassifyBackendError()=%s, want %s", got, tt.want)
			}
		})
	}
}
