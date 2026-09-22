package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBrowserConfig(t *testing.T, browserBlock string) *Config {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `telegram:
  token: "test-token"
ai:
  api_key: "test-key"
  model: "test-model"
  provider: "openrouter"
` + browserBlock
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	return cfg
}

func TestBrowserProfilesConfigParsesNamedProfiles(t *testing.T) {
	cfg := writeBrowserConfig(t, `browser:
  default_profile: work
  profiles:
    personal:
      account: me@personal.example
      debug_url: http://cdp.example:9221
    work:
      account: me@example.com
      debug_url: http://cdp.example:9224
`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	set, err := cfg.Browser.AccountProfiles()
	if err != nil {
		t.Fatalf("AccountProfiles: %v", err)
	}
	if def := set.Default(); def.Name != "work" || def.DebugURL != "http://cdp.example:9224" {
		t.Fatalf("Default() = %+v", def)
	}
	p, err := set.Resolve("ME@PERSONAL.EXAMPLE")
	if err != nil || p.Name != "personal" || p.DebugURL != "http://cdp.example:9221" {
		t.Fatalf("Resolve(personal email) = %+v, %v", p, err)
	}
	if len(set.All()) != 2 {
		t.Fatalf("All() = %+v", set.All())
	}
}

func TestBrowserProfilesConfigLegacyDebugURLFormsDefaultProfile(t *testing.T) {
	cfg := writeBrowserConfig(t, `browser:
  debug_url: http://cdp.example:9222
`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	set, err := cfg.Browser.AccountProfiles()
	if err != nil {
		t.Fatalf("AccountProfiles: %v", err)
	}
	all := set.All()
	if len(all) != 1 || all[0].Name != "default" || all[0].DebugURL != "http://cdp.example:9222" || !set.IsDefault("default") {
		t.Fatalf("legacy profiles = %+v", all)
	}
}

func TestBrowserProfilesConfigWithoutBrowserBlockIsLocal(t *testing.T) {
	cfg := writeBrowserConfig(t, "")
	set, err := cfg.Browser.AccountProfiles()
	if err != nil {
		t.Fatalf("AccountProfiles: %v", err)
	}
	if set.UsesRemoteCDP() || set.Default().DebugURL != "" {
		t.Fatalf("expected local default profile, got %+v", set.Default())
	}
}

func TestBrowserProfilesConfigValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		block   string
		wantErr string
	}{
		{
			name: "duplicate account",
			block: `browser:
  default_profile: a
  profiles:
    a: {account: same@example.com, debug_url: http://cdp.example:1}
    b: {account: SAME@example.com, debug_url: http://cdp.example:2}
`,
			wantErr: "already used by profile",
		},
		{
			name: "missing default",
			block: `browser:
  profiles:
    a: {account: a@example.com, debug_url: http://cdp.example:1}
`,
			wantErr: "default_profile is required",
		},
		{
			name: "unknown default",
			block: `browser:
  default_profile: nope
  profiles:
    a: {account: a@example.com, debug_url: http://cdp.example:1}
`,
			wantErr: `default_profile "nope" does not name`,
		},
		{
			name: "empty debug_url",
			block: `browser:
  default_profile: a
  profiles:
    a: {account: a@example.com}
`,
			wantErr: "debug_url must not be empty",
		},
		{
			name: "legacy and profiles together",
			block: `browser:
  debug_url: http://cdp.example:9222
  default_profile: a
  profiles:
    a: {account: a@example.com, debug_url: http://cdp.example:1}
`,
			wantErr: "mutually exclusive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := writeBrowserConfig(t, tt.block)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.wantErr)
			}
			if _, err := cfg.Browser.AccountProfiles(); err == nil {
				t.Fatal("AccountProfiles() succeeded on invalid config")
			}
		})
	}
}
