package ai

import "testing"

func TestThinkingForLevel(t *testing.T) {
	tests := []struct {
		level      string
		wantNil    bool
		wantType   string
		wantBudget int
	}{
		{"off", true, "", 0},
		{"", true, "", 0},
		{"low", false, "enabled", 1024},
		{"medium", false, "enabled", 8000},
		{"high", false, "enabled", 32000},
		{"adaptive", false, "adaptive", 8000},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			got := thinkingForLevel(tt.level)
			if tt.wantNil {
				if got != nil {
					t.Errorf("thinkingForLevel(%q) = %+v, want nil", tt.level, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("thinkingForLevel(%q) = nil, want non-nil", tt.level)
			}
			if got.Type != tt.wantType {
				t.Errorf("thinkingForLevel(%q).Type = %q, want %q", tt.level, got.Type, tt.wantType)
			}
			if got.BudgetTokens != tt.wantBudget {
				t.Errorf("thinkingForLevel(%q).BudgetTokens = %d, want %d", tt.level, got.BudgetTokens, tt.wantBudget)
			}
		})
	}
}

func TestMaxTokensForThinking(t *testing.T) {
	tests := []struct {
		name       string
		thinking   *ThinkingConfig
		defaultMax int
		want       int
	}{
		{"nil thinking returns default", nil, 4096, 4096},
		{"low: default is already sufficient", &ThinkingConfig{Type: "enabled", BudgetTokens: 1024}, 4096, 4096},
		{"medium: default too small, bumped up", &ThinkingConfig{Type: "enabled", BudgetTokens: 8000}, 4096, 9024},
		{"high: default too small, bumped up", &ThinkingConfig{Type: "enabled", BudgetTokens: 32000}, 4096, 33024},
		{"adaptive: default too small, bumped up", &ThinkingConfig{Type: "adaptive", BudgetTokens: 8000}, 4096, 9024},
		{"large default preserved when sufficient", &ThinkingConfig{Type: "enabled", BudgetTokens: 8000}, 32768, 32768},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maxTokensForThinking(tt.thinking, tt.defaultMax)
			if got != tt.want {
				t.Errorf("maxTokensForThinking(..., %d) = %d, want %d", tt.defaultMax, got, tt.want)
			}
		})
	}
}
