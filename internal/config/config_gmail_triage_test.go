package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/gmailtriage"
)

func TestLoadAndSaveGmailTriage(t *testing.T) {
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `gmail_triage:
  enabled: true
  gog_binary: ~/.local/bin/gog
  profiles:
    - name: shared
      mode: read_only
      taxonomy: sales
      language: en
      account: sales@example.com
      query: "label:info"
      chat_id: 12345
      schedule: ["08:30", "19:00"]
      days: sun-thu
      initial_lookback_days: 90
`
	if err := os.WriteFile(configPath, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	g := cfg.GmailTriage
	if !g.Enabled || g.Skill != gmailtriage.DefaultSkill || g.GogBinary != filepath.Join(home, ".local/bin/gog") {
		t.Fatalf("gmail_triage = %+v", g)
	}
	profiles, err := g.Resolve()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("Resolve = %+v, %v", profiles, err)
	}
	p := profiles[0]
	if !p.ReadOnly() || p.ChatID != 12345 || p.InitialLookbackDays != 90 || p.Schedule.Describe("en") != "08:30, 19:00 · sun,mon,tue,wed,thu · Asia/Jerusalem" {
		t.Fatalf("profile = %+v", p)
	}

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	again, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(again.GmailTriage.Profiles) != 1 || again.GmailTriage.Profiles[0].Query != "label:info" || again.GmailTriage.Profiles[0].Days != "sun-thu" {
		t.Fatalf("round trip lost gmail_triage: %+v", again.GmailTriage)
	}
}

func TestValidateRejectsBrokenGmailTriage(t *testing.T) {
	cfg := &Config{
		Telegram:    TelegramConfig{Token: "test-token"},
		AI:          AIConfig{APIKey: "test-key", Model: "test-model"},
		Auth:        AuthConfig{Mode: "open"},
		StoragePath: "/tmp/test.db",
		Maestro:     MaestroConfig{ReadyLabel: "ready"},
		GmailTriage: gmailtriage.Config{Enabled: true, Profiles: []gmailtriage.ProfileConfig{{Name: "x", Account: "a@example.com"}}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "chat_id") {
		t.Fatalf("Validate() = %v, want chat_id error", err)
	}
}
