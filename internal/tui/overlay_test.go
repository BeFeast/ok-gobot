package tui

import (
	"strings"
	"testing"
)

func TestDialogContentWidth(t *testing.T) {
	tests := []struct {
		name        string
		longestItem int
		termWidth   int
		want        int
	}{
		{"clamps to minimum", 10, 120, 34},
		{"clamps to maximum", 100, 120, 74},
		{"uses content width in range", 50, 120, 50},
		{"respects terminal width", 74, 60, 52}, // 60 - 6 - 2 = 52
		{"narrow terminal floor", 74, 16, 10},
		{"exact minimum boundary", 34, 120, 34},
		{"exact maximum boundary", 74, 120, 74},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dialogContentWidth(tt.longestItem, tt.termWidth)
			if got != tt.want {
				t.Errorf("dialogContentWidth(%d, %d) = %d, want %d",
					tt.longestItem, tt.termWidth, got, tt.want)
			}
		})
	}
}

func TestFilteredModelList(t *testing.T) {
	m := &Model{
		modelList: []string{
			"anthropic/claude-opus-4-5",
			"anthropic/claude-sonnet-4-5",
			"openai/gpt-4o",
			"google/gemini-2.5-pro",
			"deepseek/deepseek-chat-v3",
		},
	}

	tests := []struct {
		name   string
		filter string
		want   int
		first  string
	}{
		{"empty filter returns all", "", 5, "anthropic/claude-opus-4-5"},
		{"filter by provider", "anthropic", 2, "anthropic/claude-opus-4-5"},
		{"filter by model name", "gemini", 1, "google/gemini-2.5-pro"},
		{"case insensitive", "GPT", 1, "openai/gpt-4o"},
		{"no matches", "llama", 0, ""},
		{"partial match", "deep", 1, "deepseek/deepseek-chat-v3"},
		{"filter by slash", "/claude", 2, "anthropic/claude-opus-4-5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.modelFilter = tt.filter
			got := m.filteredModelList()
			if len(got) != tt.want {
				t.Errorf("filteredModelList() with filter %q returned %d items, want %d",
					tt.filter, len(got), tt.want)
			}
			if tt.want > 0 && got[0] != tt.first {
				t.Errorf("filteredModelList() first item = %q, want %q", got[0], tt.first)
			}
		})
	}
}

func TestOverlaySessionListContainsTitle(t *testing.T) {
	m := &Model{
		width:  100,
		height: 40,
	}

	result := m.overlaySessionList("")
	if !strings.Contains(result, "Sessions") {
		t.Error("overlaySessionList should contain 'Sessions' title")
	}
}

func TestOverlayModelListContainsFilterPrompt(t *testing.T) {
	m := &Model{
		width:     100,
		height:    40,
		modelList: []string{"anthropic/claude-opus", "openai/gpt-4o"},
	}

	result := m.overlayModelList("")
	if !strings.Contains(result, "Filter") {
		t.Error("overlayModelList should contain 'Filter' prompt")
	}
	if !strings.Contains(result, "Select Model") {
		t.Error("overlayModelList should contain 'Select Model' title")
	}
}

func TestOverlayModelListShowsNoMatches(t *testing.T) {
	m := &Model{
		width:       100,
		height:      40,
		modelList:   []string{"anthropic/claude-opus", "openai/gpt-4o"},
		modelFilter: "zzzzz",
	}

	result := m.overlayModelList("")
	if !strings.Contains(result, "no matches") {
		t.Error("overlayModelList should show 'no matches' when filter has no results")
	}
}
