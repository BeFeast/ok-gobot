package cli

import (
	"testing"

	"ok-gobot/internal/config"
)

func TestCheckSecuritySettings_OpenAuthNoRemote(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "open"
	cfg.API.Enabled = false
	cfg.Control.Enabled = false

	results := checkSecuritySettings(cfg)

	found := findResult(results, "Auth mode")
	if found == nil {
		t.Fatal("expected Auth mode result")
	}
	if !found.warning {
		t.Error("expected warning for open auth mode")
	}
}

func TestCheckSecuritySettings_OpenAuthWithAPI(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "open"
	cfg.API.Enabled = true

	results := checkSecuritySettings(cfg)

	found := findResult(results, "Auth mode with remote surfaces")
	if found == nil {
		t.Fatal("expected 'Auth mode with remote surfaces' result")
	}
	if !found.warning {
		t.Error("expected warning")
	}
}

func TestCheckSecuritySettings_AllowlistAuthPasses(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "allowlist"

	results := checkSecuritySettings(cfg)

	found := findResult(results, "Auth mode")
	if found == nil {
		t.Fatal("expected Auth mode result")
	}
	if !found.passed {
		t.Error("expected passed for allowlist auth")
	}
}

func TestCheckSecuritySettings_APINoKey(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "allowlist"
	cfg.API.Enabled = true
	cfg.API.APIKey = ""

	results := checkSecuritySettings(cfg)

	found := findResult(results, "API server authentication")
	if found == nil {
		t.Fatal("expected API server authentication result")
	}
	if !found.warning {
		t.Error("expected warning for missing API key")
	}
}

func TestCheckSecuritySettings_APIWithKey(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "allowlist"
	cfg.API.Enabled = true
	cfg.API.APIKey = "secret-key"
	cfg.API.BindAddr = "127.0.0.1"

	results := checkSecuritySettings(cfg)

	found := findResult(results, "API server authentication")
	if found == nil {
		t.Fatal("expected API server authentication result")
	}
	if !found.passed {
		t.Error("expected passed for API with key set")
	}
}

func TestCheckSecuritySettings_APIEmptyBindAddrNoWarning(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "allowlist"
	cfg.API.Enabled = true
	cfg.API.APIKey = "secret"
	cfg.API.BindAddr = "" // empty defaults to loopback, no warning

	results := checkSecuritySettings(cfg)

	found := findResult(results, "API server bind address")
	if found != nil {
		t.Error("expected no bind address warning for empty (default loopback) bind_addr")
	}
}

func TestCheckSecuritySettings_APINonLoopbackBind(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "allowlist"
	cfg.API.Enabled = true
	cfg.API.APIKey = "secret"
	cfg.API.BindAddr = "0.0.0.0"

	results := checkSecuritySettings(cfg)

	found := findResult(results, "API server bind address")
	if found == nil {
		t.Fatal("expected API server bind address result")
	}
	if !found.warning {
		t.Error("expected warning for non-loopback bind")
	}
}

func TestCheckSecuritySettings_ControlNoToken(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "allowlist"
	cfg.Control.Enabled = true
	cfg.Control.Token = ""

	results := checkSecuritySettings(cfg)

	found := findResult(results, "Control server token")
	if found == nil {
		t.Fatal("expected Control server token result")
	}
	if !found.warning {
		t.Error("expected warning for missing control token")
	}
}

func TestCheckSecuritySettings_ControlWithToken(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "allowlist"
	cfg.Control.Enabled = true
	cfg.Control.Token = "strong-token"

	results := checkSecuritySettings(cfg)

	found := findResult(results, "Control server token")
	if found == nil {
		t.Fatal("expected Control server token result")
	}
	if !found.passed {
		t.Error("expected passed for control with token set")
	}
}

func TestCheckSecuritySettings_ControlLoopbackWithoutToken(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "allowlist"
	cfg.Control.Enabled = true
	cfg.Control.Token = "token"
	cfg.Control.AllowLoopbackWithoutToken = true

	results := checkSecuritySettings(cfg)

	found := findResult(results, "Control server loopback auth")
	if found == nil {
		t.Fatal("expected Control server loopback auth result")
	}
	if !found.warning {
		t.Error("expected warning for allow_loopback_without_token")
	}
}

func TestCheckSecuritySettings_DisabledRemoteNoExtraChecks(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Auth.Mode = "pairing"
	cfg.API.Enabled = false
	cfg.Control.Enabled = false

	results := checkSecuritySettings(cfg)

	// Should only have the auth mode check (passed).
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if !results[0].passed {
		t.Error("expected pairing mode to pass")
	}
}

func findResult(results []checkResult, name string) *checkResult {
	for i := range results {
		if results[i].name == name {
			return &results[i]
		}
	}
	return nil
}
