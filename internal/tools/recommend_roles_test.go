package tools

import (
	"context"
	"strings"
	"testing"

	"ok-gobot/internal/storage"
)

// mockPatternStore implements recommend.PatternStore for tool tests.
type mockPatternStore struct {
	messages []storage.UserMessageRow
}

func (m *mockPatternStore) GetChatActivityStats(limit int) ([]storage.ChatActivityRow, error) {
	return nil, nil
}

func (m *mockPatternStore) GetRecentUserMessages(limit int) ([]storage.UserMessageRow, error) {
	return m.messages, nil
}

func (m *mockPatternStore) CronJobSummary() ([]storage.CronJob, error) {
	return nil, nil
}

func (m *mockPatternStore) JobKindCounts(limit int) (map[string]int, error) {
	return map[string]int{}, nil
}

func TestRecommendRolesTool_Name(t *testing.T) {
	tool := NewRecommendRolesTool(&mockPatternStore{})
	if tool.Name() != "recommend_roles" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "recommend_roles")
	}
}

func TestRecommendRolesTool_EmptyData(t *testing.T) {
	tool := NewRecommendRolesTool(&mockPatternStore{})
	result, err := tool.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "No role recommendations") {
		t.Errorf("expected 'No role recommendations' in output, got: %s", result)
	}
}

func TestRecommendRolesTool_WithPatterns(t *testing.T) {
	store := &mockPatternStore{
		messages: []storage.UserMessageRow{
			{Content: "remind me tomorrow"},
			{Content: "don't forget to follow up"},
			{Content: "schedule a reminder"},
		},
	}
	tool := NewRecommendRolesTool(store)
	result, err := tool.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "reminder") {
		t.Errorf("expected 'reminder' in output, got: %s", result)
	}
	if !strings.Contains(result, "schedule") {
		t.Errorf("expected 'schedule' in output, got: %s", result)
	}
}

func TestRecommendRolesTool_HasSchema(t *testing.T) {
	tool := NewRecommendRolesTool(&mockPatternStore{})
	schema := tool.GetSchema()
	if schema == nil {
		t.Fatal("GetSchema() returned nil")
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want 'object'", schema["type"])
	}
}

func TestRecommendRolesTool_Registration(t *testing.T) {
	tmpDir := t.TempDir()
	store := &mockPatternStore{}
	registry, err := LoadFromConfigWithOptions(tmpDir, &ToolsConfig{
		PatternStore: store,
	})
	if err != nil {
		t.Fatalf("LoadFromConfigWithOptions: %v", err)
	}
	if _, ok := registry.Get("recommend_roles"); !ok {
		t.Fatal("recommend_roles tool is not registered")
	}
}
