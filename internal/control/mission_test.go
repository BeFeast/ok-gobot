package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/evidence"
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
