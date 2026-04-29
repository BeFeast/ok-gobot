package tools

import (
	"context"
	"testing"
)

func TestValidateURLWithOpts_BlocksPrivateIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		url           string
		allowInternal bool
		wantErr       bool
	}{
		{"localhost blocked", "http://localhost/test", false, true},
		{"0.0.0.0 blocked", "http://0.0.0.0/test", false, true},
		{"localhost allowed when internal", "http://localhost/test", true, false},
		{"0.0.0.0 allowed when internal", "http://0.0.0.0/test", true, false},
		{"file scheme blocked", "file:///etc/passwd", false, true},
		{"ftp scheme blocked", "ftp://example.com", false, true},
		{"https allowed", "https://example.com", false, false},
		{"http allowed", "http://example.com", false, false},
		{"missing hostname blocked", "https://", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateURLWithOpts(tt.url, tt.allowInternal)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for %q", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tt.url, err)
			}
		})
	}
}

func TestValidateURLForContext_AllowlistEnforcement(t *testing.T) {
	t.Parallel()

	// Use real resolvable hostnames to avoid DNS failures.
	policy := &CapabilityPolicy{
		Network:          true,
		NetworkAllowlist: []string{"github.com", "*.google.com"},
	}
	ctx := ContextWithNetworkPolicy(context.Background(), policy)

	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"allowed exact host", "https://github.com/foo", false},
		{"allowed wildcard", "https://www.google.com/search", false},
		{"blocked host", "https://example.com/nope", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateURLForContext(ctx, tt.url)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for %q", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tt.url, err)
			}
		})
	}
}

func TestValidateURLForContext_NoPolicy(t *testing.T) {
	t.Parallel()

	// Without policy in context, validateURLForContext should behave like validateURL.
	err := validateURLForContext(context.Background(), "https://anything.com")
	if err != nil {
		t.Fatalf("no policy should allow any valid URL: %v", err)
	}
}

func TestValidateURLForContext_AllowInternalWithAllowlist(t *testing.T) {
	t.Parallel()

	policy := &CapabilityPolicy{
		Network:               true,
		NetworkAllowlist:      []string{"localhost"},
		AllowInternalNetworks: true,
	}
	ctx := ContextWithNetworkPolicy(context.Background(), policy)

	// localhost is in the allowlist and AllowInternalNetworks=true, so this should pass.
	err := validateURLForContext(ctx, "http://localhost:8080/api")
	if err != nil {
		t.Fatalf("expected localhost to be allowed with AllowInternalNetworks: %v", err)
	}

	// Evil host is not in the allowlist.
	err = validateURLForContext(ctx, "https://evil.com")
	if err == nil {
		t.Fatal("expected non-allowlisted host to be blocked")
	}
}

func TestValidateBrowserURLWithOpts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		url           string
		allowInternal bool
		wantErr       bool
	}{
		{"file scheme always blocked", "file:///etc/passwd", false, true},
		{"file scheme blocked even when internal", "file:///etc/passwd", true, true},
		{"localhost blocked", "http://localhost:3000", false, true},
		{"localhost allowed when internal", "http://localhost:3000", true, false},
		{"loopback blocked", "http://127.0.0.1:8080", false, true},
		{"loopback allowed when internal", "http://127.0.0.1:8080", true, false},
		{"ipv6 loopback blocked", "http://[::1]:8080", false, true},
		{"ipv6 loopback allowed when internal", "http://[::1]:8080", true, false},
		{".internal blocked", "http://app.internal", false, true},
		{".internal allowed when internal", "http://app.internal", true, false},
		{".local blocked", "http://printer.local", false, true},
		{".local allowed when internal", "http://printer.local", true, false},
		{"https allowed", "https://example.com", false, false},
		{"ftp blocked", "ftp://example.com", false, true},
		{"javascript blocked", "javascript:alert(1)", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateBrowserURLWithOpts(tt.url, tt.allowInternal)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for %q (allowInternal=%v)", tt.url, tt.allowInternal)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for %q (allowInternal=%v): %v", tt.url, tt.allowInternal, err)
			}
		})
	}
}

func TestNetworkPolicyGuard_FileSchemeBlocked(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "web_fetch"})

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		NetworkAllowlist: []string{"example.com"},
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
	}

	result := ApplyPolicy(reg, policy)

	_, err := result.Execute(context.Background(), "web_fetch", "file:///etc/passwd")
	if err == nil {
		t.Fatal("expected file:// URL to be blocked")
	}
	denial, isDenial := IsToolDenial(err)
	if !isDenial {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
	if denial.Family != "network_allowlist" {
		t.Errorf("denial.Family = %q, want network_allowlist", denial.Family)
	}
}

func TestNetworkPolicyGuard_BrowserOpenBlocked(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "browser"})

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		NetworkAllowlist: []string{"github.com"},
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
	}

	result := ApplyPolicy(reg, policy)

	// open with blocked URL.
	_, err := result.Execute(context.Background(), "browser", "open", "https://evil.com")
	if err == nil {
		t.Fatal("expected browser open to be denied for non-allowlisted host")
	}
	if _, ok := IsToolDenial(err); !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}

	// open with allowed URL.
	_, err = result.Execute(context.Background(), "browser", "open", "https://github.com")
	if err != nil {
		t.Fatalf("expected github.com open to be allowed: %v", err)
	}
}
