package recommend

import (
	"strings"
	"testing"

	"ok-gobot/internal/storage"
)

// mockStore implements PatternStore for testing.
type mockStore struct {
	activity []storage.ChatActivityRow
	messages []storage.UserMessageRow
	cronJobs []storage.CronJob
	jobKinds map[string]int
}

func (m *mockStore) GetChatActivityStats(limit int) ([]storage.ChatActivityRow, error) {
	return m.activity, nil
}

func (m *mockStore) GetRecentUserMessages(limit int) ([]storage.UserMessageRow, error) {
	return m.messages, nil
}

func (m *mockStore) CronJobSummary() ([]storage.CronJob, error) {
	return m.cronJobs, nil
}

func (m *mockStore) JobKindCounts(limit int) (map[string]int, error) {
	return m.jobKinds, nil
}

func TestAnalyze_NoData(t *testing.T) {
	store := &mockStore{jobKinds: map[string]int{}}
	r := New(store)

	recs, err := r.Analyze()
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations, got %d", len(recs))
	}
}

func TestAnalyze_DetectsMonitorPattern(t *testing.T) {
	store := &mockStore{
		messages: []storage.UserMessageRow{
			{Content: "check if the API is running"},
			{Content: "is the service alive?"},
			{Content: "can you check the status of the deploy?"},
			{Content: "monitor the health endpoint"},
		},
		jobKinds: map[string]int{},
	}

	r := New(store)
	recs, err := r.Analyze()
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	found := false
	for _, rec := range recs {
		if rec.Name == "monitor" {
			found = true
			if rec.Schedule == "" {
				t.Error("monitor recommendation has empty schedule")
			}
			if rec.OutputExample == "" {
				t.Error("monitor recommendation has empty output example")
			}
			if !strings.Contains(rec.Rationale, "monitor") {
				t.Errorf("rationale should mention monitor, got: %s", rec.Rationale)
			}
		}
	}
	if !found {
		t.Error("expected monitor role recommendation")
	}
}

func TestAnalyze_DetectsReporterPattern(t *testing.T) {
	store := &mockStore{
		messages: []storage.UserMessageRow{
			{Content: "give me a summary of yesterday"},
			{Content: "what are the stats for this week?"},
			{Content: "daily report please"},
		},
		jobKinds: map[string]int{},
	}

	r := New(store)
	recs, err := r.Analyze()
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	found := false
	for _, rec := range recs {
		if rec.Name == "reporter" {
			found = true
		}
	}
	if !found {
		t.Error("expected reporter role recommendation")
	}
}

func TestAnalyze_SkipsWhenCronAlreadyCovers(t *testing.T) {
	store := &mockStore{
		messages: []storage.UserMessageRow{
			{Content: "check the status"},
			{Content: "is the service running"},
			{Content: "monitor health please"},
		},
		cronJobs: []storage.CronJob{
			{Task: "check health status of all services", Enabled: true},
		},
		jobKinds: map[string]int{},
	}

	r := New(store)
	recs, err := r.Analyze()
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	for _, rec := range recs {
		if rec.Name == "monitor" {
			t.Error("should not recommend monitor when cron already covers it")
		}
	}
}

func TestAnalyze_RecommendsFromJobKinds(t *testing.T) {
	store := &mockStore{
		jobKinds: map[string]int{
			"backup": 5,
		},
	}

	r := New(store)
	recs, err := r.Analyze()
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	found := false
	for _, rec := range recs {
		if rec.Name == "backup-scheduler" {
			found = true
		}
	}
	if !found {
		t.Error("expected backup-scheduler recommendation from job kinds")
	}
}

func TestAnalyze_RecommendsTriageFromMultiAgentActivity(t *testing.T) {
	var activity []storage.ChatActivityRow
	for i := 0; i < 6; i++ {
		agentID := "bot"
		if i%2 == 0 {
			agentID = "assistant"
		}
		activity = append(activity, storage.ChatActivityRow{
			ChatID:       int64(100 + i),
			AgentID:      agentID,
			MessageCount: 20,
		})
	}

	store := &mockStore{
		activity: activity,
		jobKinds: map[string]int{},
	}

	r := New(store)
	recs, err := r.Analyze()
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	found := false
	for _, rec := range recs {
		if rec.Name == "triage" {
			found = true
		}
	}
	if !found {
		t.Error("expected triage recommendation from multi-agent activity")
	}
}

func TestFormat_Empty(t *testing.T) {
	out := Format(nil)
	if !strings.Contains(out, "No role recommendations") {
		t.Errorf("unexpected output for empty recommendations: %s", out)
	}
}

func TestFormat_WithRecommendations(t *testing.T) {
	recs := []RoleRecommendation{
		{
			Name:          "monitor",
			Description:   "Check service health.",
			Schedule:      "*/15 * * * *",
			OutputExample: "All OK",
			Rationale:     "Detected 5 monitoring messages.",
		},
	}
	out := Format(recs)
	if !strings.Contains(out, "monitor") {
		t.Error("output should contain role name")
	}
	if !strings.Contains(out, "*/15 * * * *") {
		t.Error("output should contain schedule")
	}
	if !strings.Contains(out, "All OK") {
		t.Error("output should contain output example")
	}
}

func TestHitThreshold(t *testing.T) {
	r := &Recommender{}
	if r.hitThreshold(10) != 2 {
		t.Error("threshold should be 2 for < 20 messages")
	}
	if r.hitThreshold(100) != 3 {
		t.Error("threshold should be 3 for >= 20 messages")
	}
}
