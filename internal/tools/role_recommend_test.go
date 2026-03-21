package tools

import (
	"context"
	"strings"
	"testing"

	"ok-gobot/internal/storage"
)

// mockRoleRecommendStore implements RoleRecommendStore for testing.
type mockRoleRecommendStore struct {
	messages []storage.SessionMessageV2
	jobs     []storage.Job
	cronJobs []storage.CronJob
}

func (m *mockRoleRecommendStore) RecentUserMessages(limit int) ([]storage.SessionMessageV2, error) {
	return m.messages, nil
}

func (m *mockRoleRecommendStore) RecentCompletedJobs(limit int) ([]storage.Job, error) {
	return m.jobs, nil
}

func (m *mockRoleRecommendStore) AllCronJobs() ([]storage.CronJob, error) {
	return m.cronJobs, nil
}

func TestRoleRecommendTool_EmptyHistory(t *testing.T) {
	tool := NewRoleRecommendTool(&mockRoleRecommendStore{}, nil)

	result, err := tool.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No chat or job history") {
		t.Errorf("expected empty history message, got: %s", result)
	}
}

func TestRoleRecommendTool_DetectsCodeReviewPattern(t *testing.T) {
	store := &mockRoleRecommendStore{
		messages: []storage.SessionMessageV2{
			{Content: "please review this PR"},
			{Content: "can you review the pull request for auth?"},
			{Content: "check the diff on the merge request"},
			{Content: "what do you think about this code review?"},
		},
	}

	tool := NewRoleRecommendTool(store, nil)
	result, err := tool.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "code-reviewer") {
		t.Errorf("expected code-reviewer recommendation, got: %s", result)
	}
	if !strings.Contains(result, "Suggested schedule") {
		t.Errorf("expected schedule suggestion, got: %s", result)
	}
	if !strings.Contains(result, "Example output") {
		t.Errorf("expected example output, got: %s", result)
	}
}

func TestRoleRecommendTool_DetectsMultiplePatterns(t *testing.T) {
	store := &mockRoleRecommendStore{
		messages: []storage.SessionMessageV2{
			{Content: "review this PR"},
			{Content: "deploy to production"},
			{Content: "check the logs for errors"},
			{Content: "generate the weekly report"},
			{Content: "look at the deployment pipeline"},
		},
		jobs: []storage.Job{
			{Description: "deploy the latest build", Kind: "deploy"},
			{Description: "review code changes", Kind: "review"},
		},
	}

	tool := NewRoleRecommendTool(store, nil)
	result, err := tool.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Role Recommendations") {
		t.Errorf("expected recommendations header, got: %s", result)
	}
	// Should have multiple recommendations
	if strings.Count(result, "###") < 2 {
		t.Errorf("expected at least 2 recommendations, got: %s", result)
	}
}

func TestRoleRecommendTool_FiltersExistingRoles(t *testing.T) {
	store := &mockRoleRecommendStore{
		messages: []storage.SessionMessageV2{
			{Content: "review this PR"},
			{Content: "review code changes"},
			{Content: "check the diff"},
		},
	}

	tool := NewRoleRecommendTool(store, []string{"code-reviewer"})
	result, err := tool.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// code-reviewer should be filtered out since it already exists
	if strings.Contains(result, "### 1. code-reviewer") {
		t.Errorf("should not recommend existing role, got: %s", result)
	}
	if !strings.Contains(result, "already covered") || !strings.Contains(result, "code-reviewer") {
		t.Errorf("should mention existing pattern is covered, got: %s", result)
	}
}

func TestRoleRecommendTool_IncludesCronJobContext(t *testing.T) {
	store := &mockRoleRecommendStore{
		messages: []storage.SessionMessageV2{
			{Content: "deploy to production"},
		},
		cronJobs: []storage.CronJob{
			{Expression: "0 9 * * *", Task: "morning check", Enabled: true},
			{Expression: "0 18 * * 1-5", Task: "end of day report", Enabled: false},
		},
	}

	tool := NewRoleRecommendTool(store, nil)
	result, err := tool.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Existing scheduled tasks") {
		t.Errorf("expected existing scheduled tasks section, got: %s", result)
	}
	if !strings.Contains(result, "morning check") {
		t.Errorf("expected cron job reference, got: %s", result)
	}
}

func TestRoleRecommendTool_ExecuteJSON(t *testing.T) {
	store := &mockRoleRecommendStore{
		messages: []storage.SessionMessageV2{
			{Content: "check the server health"},
			{Content: "monitor CPU usage"},
		},
	}

	tool := NewRoleRecommendTool(store, nil)
	result, err := tool.ExecuteJSON(context.Background(), map[string]string{"command": "analyze"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "infra-watchdog") {
		t.Errorf("expected infra-watchdog recommendation, got: %s", result)
	}
}

func TestRoleRecommendTool_InvalidCommand(t *testing.T) {
	tool := NewRoleRecommendTool(&mockRoleRecommendStore{}, nil)
	_, err := tool.ExecuteJSON(context.Background(), map[string]string{"command": "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid command")
	}
}

func TestRoleRecommendTool_Schema(t *testing.T) {
	tool := NewRoleRecommendTool(&mockRoleRecommendStore{}, nil)
	schema := tool.GetSchema()

	if schema["type"] != "object" {
		t.Errorf("expected object schema, got: %v", schema["type"])
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("expected properties in schema")
	}

	if _, ok := props["command"]; !ok {
		t.Error("expected 'command' property in schema")
	}
}

func TestRoleRecommendTool_Name(t *testing.T) {
	tool := NewRoleRecommendTool(&mockRoleRecommendStore{}, nil)
	if tool.Name() != "role_recommend" {
		t.Errorf("expected name 'role_recommend', got: %s", tool.Name())
	}
}

func TestContainsWord(t *testing.T) {
	tests := []struct {
		text    string
		keyword string
		want    bool
	}{
		{"deploy to production", "deploy", true},
		{"redeployment failed", "deploy", false}, // substring, not word
		{"check the logs", "logs", true},
		{"check the log file", "log", true},
		{"dialog box", "log", false},
		{"pull request review", "pull request", true},
		{"", "test", false},
		{"test", "test", true},
		{"testing framework", "test", false}, // "test" followed by "ing"
	}

	for _, tc := range tests {
		got := containsWord(tc.text, tc.keyword)
		if got != tc.want {
			t.Errorf("containsWord(%q, %q) = %v, want %v", tc.text, tc.keyword, got, tc.want)
		}
	}
}

func TestRoleRecommendTool_JobPatterns(t *testing.T) {
	store := &mockRoleRecommendStore{
		jobs: []storage.Job{
			{Description: "scan for security vulnerabilities", Kind: "security"},
			{Description: "check CVE database for updates", Kind: "security"},
			{Description: "audit the dependency tree", Kind: "audit"},
		},
	}

	tool := NewRoleRecommendTool(store, nil)
	result, err := tool.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "security-auditor") {
		t.Errorf("expected security-auditor from job patterns, got: %s", result)
	}
}
