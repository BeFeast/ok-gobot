package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/evidence"
	"ok-gobot/internal/hygiene"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

type missionEvidenceProvider struct {
	store *storage.Store
}

func (m missionEvidenceProvider) GetStore() *storage.Store { return m.store }

func (m missionEvidenceProvider) GetAgentRegistry() *agent.AgentRegistry { return nil }

func (m missionEvidenceProvider) GetScheduler() tools.CronScheduler { return nil }

func (m missionEvidenceProvider) GetMemoryStatus(context.Context) (memory.IndexStatus, error) {
	return memory.IndexStatus{}, nil
}

type missionStateStub struct {
	status map[string]interface{}
}

func (s missionStateStub) GetStatus() map[string]interface{} { return s.status }

func (s missionStateStub) RespondToApproval(string, bool) error { return nil }

type missionHygieneProvider struct {
	missionEvidenceProvider
	report hygiene.Report
}

func (m missionHygieneProvider) GetHygieneReport(context.Context) (hygiene.Report, error) {
	return m.report, nil
}

func TestMissionEvidenceRendersSessionTimeline(t *testing.T) {
	t.Parallel()

	store, err := storage.New(filepath.Join(t.TempDir(), "mission-evidence.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	defer store.Close() //nolint:errcheck

	const sessionKey = "agent:maestro:main"
	if err := store.AddEvidenceEvent(evidence.Event{
		SessionKey: sessionKey,
		Type:       evidence.EventPreflight,
		Status:     "passed",
		Summary:    "preflight passed",
	}); err != nil {
		t.Fatalf("AddEvidenceEvent failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mission/evidence?session_key="+sessionKey, nil)
	w := httptest.NewRecorder()
	missionEvidence(missionEvidenceProvider{store: store})(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"\"session_key\"", "Preflight", "preflight passed", "markdown"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in body: %s", want, body)
		}
	}
}

func TestMissionEvidenceRequiresSessionKey(t *testing.T) {
	t.Parallel()

	store, err := storage.New(filepath.Join(t.TempDir(), "mission-evidence-missing.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	defer store.Close() //nolint:errcheck

	req := httptest.NewRequest(http.MethodGet, "/api/mission/evidence", nil)
	w := httptest.NewRecorder()
	missionEvidence(missionEvidenceProvider{store: store})(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestMissionEvidenceListFailureUsesGenericError(t *testing.T) {
	t.Parallel()

	store, err := storage.New(filepath.Join(t.TempDir(), "mission-evidence-closed.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mission/evidence?session_key=agent:maestro:main", nil)
	w := httptest.NewRecorder()
	missionEvidence(missionEvidenceProvider{store: store})(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "failed to list evidence") {
		t.Fatalf("expected generic evidence error in body: %s", body)
	}
	if strings.Contains(body, "database") || strings.Contains(body, "evidence_events") {
		t.Fatalf("raw storage detail leaked in body: %s", body)
	}
}

func TestMissionProvidersIncludesBackendHealth(t *testing.T) {
	srv := &Server{state: missionStateStub{status: map[string]interface{}{
		"ai": map[string]interface{}{
			"provider":   "droid",
			"backend":    "opencode",
			"model":      "anthropic/claude-sonnet",
			"model_tier": "premium",
			"effort":     "high",
			"health": ai.BackendHealth{
				Identity: ai.BackendIdentity{Provider: "droid", Backend: "opencode", Model: "anthropic/claude-sonnet"},
				Status:   ai.BackendHealthHealthy,
				Fallback: ai.FallbackDecision{Action: ai.FallbackActionPrimary},
			},
		},
	}}}

	req := httptest.NewRequest(http.MethodGet, "/api/mission/providers", nil)
	rec := httptest.NewRecorder()
	missionProviders(srv)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	for key, want := range map[string]string{
		"provider":   "droid",
		"backend":    "opencode",
		"model":      "anthropic/claude-sonnet",
		"model_tier": "premium",
		"effort":     "high",
	} {
		if got[key] != want {
			t.Fatalf("%s=%v, want %s", key, got[key], want)
		}
	}
	if got["health"] == nil {
		t.Fatal("expected health payload")
	}
}

func TestMissionHygieneReturnsReadOnlySummary(t *testing.T) {
	t.Parallel()

	store, err := storage.New(filepath.Join(t.TempDir(), "mission-hygiene.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	defer store.Close() //nolint:errcheck

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	provider := missionHygieneProvider{
		missionEvidenceProvider: missionEvidenceProvider{store: store},
		report: hygiene.Report{
			GeneratedAt: now,
			Summary: hygiene.Summary{
				Status:          "attention_needed",
				TotalFindings:   1,
				SafeActionCount: 1,
				GeneratedAt:     now,
			},
			SafeActions: []hygiene.Finding{{
				ID:          hygiene.FindingCheckoutBehind,
				Severity:    hygiene.SeverityWarning,
				Title:       "Local main is behind origin/main",
				ActionGroup: hygiene.ActionSafe,
			}},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mission/hygiene", nil)
	rec := httptest.NewRecorder()
	missionHygiene(provider)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"attention_needed", "checkout_behind_origin", "safe_actions"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}
