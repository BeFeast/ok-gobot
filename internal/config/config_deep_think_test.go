package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func deepThinkBaseConfig() *Config {
	return &Config{
		Telegram:    TelegramConfig{Token: "test-token"},
		AI:          AIConfig{APIKey: "test-key", Model: "test-model"},
		Auth:        AuthConfig{Mode: "open"},
		StoragePath: "/tmp/test.db",
		Maestro:     MaestroConfig{ReadyLabel: "ready"},
		Runtime: RuntimeConfig{CostTiers: map[string]CostTierEntry{
			"premium": {Model: "gpt-6-sol", Thinking: "high"},
		}},
	}
}

// The zero value is the documented default: no triggers, no tool.
func TestDeepThinkDefaultIsOff(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	dt := loaded.AI.DeepThink
	if len(dt.Triggers) != 0 || dt.Tier != "" || dt.Thinking != "" {
		t.Fatalf("trigger defaults = %+v, want empty", dt)
	}
	if dt.Escalation.Enabled || len(dt.Escalation.Tiers) != 0 || len(dt.Escalation.Models) != 0 || len(dt.Escalation.OnRequestModels) != 0 {
		t.Fatalf("escalation defaults = %+v, want disabled", dt.Escalation)
	}
	if err := deepThinkBaseConfig().Validate(); err != nil {
		t.Fatalf("Validate() with feature off = %v", err)
	}
}

func TestSavePersistsDeepThink(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		ConfigPath: configPath,
		AI: AIConfig{DeepThink: DeepThinkConfig{
			Triggers: []string{"подумай хорошо", "think hard"},
			Tier:     "premium",
			Thinking: "xhigh",
			Escalation: DeepThinkEscalationConfig{
				Enabled:         true,
				Tiers:           []string{"premium"},
				Models:          []string{"gpt-6-sol"},
				OnRequestModels: []string{"claude-opus-5-5", "gpt-6-astra"},
			},
		}},
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	got := loaded.AI.DeepThink
	if strings.Join(got.Triggers, "|") != "подумай хорошо|think hard" || got.Tier != "premium" || got.Thinking != "xhigh" {
		t.Fatalf("trigger config = %+v", got)
	}
	if !got.Escalation.Enabled || strings.Join(got.Escalation.Tiers, "|") != "premium" ||
		strings.Join(got.Escalation.Models, "|") != "gpt-6-sol" ||
		strings.Join(got.Escalation.OnRequestModels, "|") != "claude-opus-5-5|gpt-6-astra" {
		t.Fatalf("escalation config = %+v", got.Escalation)
	}
}

func TestValidateDeepThink(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:   "tier target",
			mutate: func(c *Config) { c.AI.DeepThink = DeepThinkConfig{Triggers: []string{"think hard"}, Tier: "premium"} },
		},
		{
			name:   "thinking only target",
			mutate: func(c *Config) { c.AI.DeepThink = DeepThinkConfig{Triggers: []string{"think hard"}, Thinking: "high"} },
		},
		{
			name:    "triggers without target",
			mutate:  func(c *Config) { c.AI.DeepThink = DeepThinkConfig{Triggers: []string{"think hard"}} },
			wantErr: "neither ai.deep_think.tier nor ai.deep_think.thinking",
		},
		{
			name:    "unknown tier name",
			mutate:  func(c *Config) { c.AI.DeepThink = DeepThinkConfig{Tier: "turbo"} },
			wantErr: "invalid ai.deep_think.tier",
		},
		{
			name:    "tier missing from cost_tiers",
			mutate:  func(c *Config) { c.AI.DeepThink = DeepThinkConfig{Tier: "cheap"} },
			wantErr: "not defined in runtime.cost_tiers",
		},
		{
			name:    "bad thinking level",
			mutate:  func(c *Config) { c.AI.DeepThink = DeepThinkConfig{Thinking: "adaptive"} },
			wantErr: "invalid ai.deep_think.thinking",
		},
		{
			name: "escalation enabled with tier",
			mutate: func(c *Config) {
				c.AI.DeepThink.Escalation = DeepThinkEscalationConfig{Enabled: true, Tiers: []string{"premium"}}
			},
		},
		{
			name: "escalation enabled with only on-request models",
			mutate: func(c *Config) {
				c.AI.DeepThink.Escalation = DeepThinkEscalationConfig{Enabled: true, OnRequestModels: []string{"claude-opus-5-5"}}
			},
		},
		{
			name: "escalation enabled without allowlist",
			mutate: func(c *Config) {
				c.AI.DeepThink.Escalation = DeepThinkEscalationConfig{Enabled: true}
			},
			wantErr: "no tiers or models are allowed",
		},
		{
			name: "escalation tier missing from cost_tiers",
			mutate: func(c *Config) {
				c.AI.DeepThink.Escalation = DeepThinkEscalationConfig{Enabled: true, Tiers: []string{"standard"}}
			},
			wantErr: "not defined in runtime.cost_tiers",
		},
		{
			name: "escalation disabled ignores allowlist",
			mutate: func(c *Config) {
				c.AI.DeepThink.Escalation = DeepThinkEscalationConfig{Enabled: false, Tiers: []string{"bogus"}}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := deepThinkBaseConfig()
			tc.mutate(cfg)
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}
