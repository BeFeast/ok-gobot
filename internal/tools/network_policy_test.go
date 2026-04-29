package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// HostMatchesAllowlist
// ---------------------------------------------------------------------------

func TestHostMatchesAllowlist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		host      string
		allowlist []string
		want      bool
	}{
		// Exact match.
		{"github.com", []string{"github.com"}, true},
		{"GITHUB.COM", []string{"github.com"}, true}, // case-insensitive
		{"example.com", []string{"github.com"}, false},

		// Wildcard suffix.
		{"api.github.com", []string{"*.github.com"}, true},
		{"github.com", []string{"*.github.com"}, true}, // wildcard also matches bare domain
		{"sub.api.github.com", []string{"*.github.com"}, true},
		{"evil.com", []string{"*.github.com"}, false},
		{"notgithub.com", []string{"*.github.com"}, false},

		// Multiple entries.
		{"example.com", []string{"github.com", "example.com"}, true},
		{"api.github.com", []string{"github.com", "*.github.com"}, true},
		{"evil.com", []string{"github.com", "*.example.com"}, false},

		// Trailing dot normalization.
		{"github.com.", []string{"github.com"}, true},

		// Empty allowlist.
		{"anything.com", []string{}, false},
		{"anything.com", nil, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_in_%v", tt.host, tt.allowlist), func(t *testing.T) {
			t.Parallel()
			got := HostMatchesAllowlist(tt.host, tt.allowlist)
			if got != tt.want {
				t.Errorf("HostMatchesAllowlist(%q, %v) = %v, want %v", tt.host, tt.allowlist, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CheckNetworkTarget
// ---------------------------------------------------------------------------

func TestCheckNetworkTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		policy  *CapabilityPolicy
		wantNil bool
	}{
		{
			"nil policy allows all",
			"https://evil.com",
			nil,
			true,
		},
		{
			"allowed host passes",
			"https://github.com/foo",
			&CapabilityPolicy{Network: true, NetworkAllowlist: []string{"github.com"}},
			true,
		},
		{
			"blocked host denied",
			"https://evil.com",
			&CapabilityPolicy{Network: true, NetworkAllowlist: []string{"github.com"}},
			false,
		},
		{
			"wildcard allows subdomain",
			"https://api.github.com/repos",
			&CapabilityPolicy{Network: true, NetworkAllowlist: []string{"*.github.com"}},
			true,
		},
		{
			"file scheme denied",
			"file:///etc/passwd",
			&CapabilityPolicy{Network: true, NetworkAllowlist: []string{"github.com"}},
			false,
		},
		{
			"file scheme denied even with empty allowlist",
			"file:///etc/passwd",
			&CapabilityPolicy{Network: true},
			false,
		},
		{
			"empty allowlist allows all hosts",
			"https://anything.com",
			&CapabilityPolicy{Network: true},
			true,
		},
		{
			"localhost blocked by default",
			"http://localhost:8080",
			&CapabilityPolicy{Network: true},
			false,
		},
		{
			"127.0.0.1 blocked by default",
			"http://127.0.0.1/admin",
			&CapabilityPolicy{Network: true},
			false,
		},
		{
			"::1 blocked by default",
			"http://[::1]/admin",
			&CapabilityPolicy{Network: true},
			false,
		},
		{
			".internal hostname blocked",
			"http://secret.internal/api",
			&CapabilityPolicy{Network: true},
			false,
		},
		{
			"localhost allowed with AllowInternalNetworks",
			"http://localhost:8080",
			&CapabilityPolicy{Network: true, AllowInternalNetworks: true},
			true,
		},
		{
			"127.0.0.1 allowed with AllowInternalNetworks",
			"http://127.0.0.1/admin",
			&CapabilityPolicy{Network: true, AllowInternalNetworks: true},
			true,
		},
		{
			"unsupported scheme denied",
			"ftp://example.com/file",
			&CapabilityPolicy{Network: true},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			denial := CheckNetworkTarget("web_fetch", tt.url, tt.policy)
			if tt.wantNil && denial != nil {
				t.Errorf("expected nil, got denial: %v", denial)
			}
			if !tt.wantNil && denial == nil {
				t.Error("expected denial, got nil")
			}
			if denial != nil {
				if denial.ToolName != "web_fetch" {
					t.Errorf("denial.ToolName = %q, want web_fetch", denial.ToolName)
				}
				if denial.Family != "network" {
					t.Errorf("denial.Family = %q, want network", denial.Family)
				}
				if denial.Remediation == "" {
					t.Error("denial.Remediation should not be empty")
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NetworkPolicyGuard wrapping / Execute
// ---------------------------------------------------------------------------

func TestNetworkPolicyGuard_BlocksDisallowedHost(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "web_fetch"})

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
		NetworkAllowlist: []string{"github.com"},
	}

	result := ApplyPolicy(reg, policy)

	// Allowed host.
	_, err := result.Execute(context.Background(), "web_fetch", "https://github.com/foo")
	if err != nil {
		t.Fatalf("expected github.com to be allowed: %v", err)
	}

	// Disallowed host.
	_, err = result.Execute(context.Background(), "web_fetch", "https://evil.com")
	if err == nil {
		t.Fatal("expected evil.com to be denied")
	}
	denial, ok := IsToolDenial(err)
	if !ok {
		t.Fatalf("expected ToolDenial, got %T: %v", err, err)
	}
	if denial.Family != "network" {
		t.Errorf("denial.Family = %q, want network", denial.Family)
	}
}

func TestNetworkPolicyGuard_NilContextSafe(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "web_fetch"})

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
		NetworkAllowlist: []string{"github.com"},
	}

	result := ApplyPolicy(reg, policy)

	// nil context — should NOT panic, should return denial for blocked host.
	_, err := result.Execute(nil, "web_fetch", "https://evil.com")
	if err == nil {
		t.Fatal("expected denial")
	}
	if _, ok := IsToolDenial(err); !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}

	// nil context with allowed host — should work (stubTool ignores ctx).
	_, err = result.Execute(nil, "web_fetch", "https://github.com")
	if err != nil {
		t.Fatalf("expected allowed host with nil ctx to work: %v", err)
	}
}

func TestNetworkPolicyGuard_SearchDeniedWithAllowlist(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "search"})

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
		NetworkAllowlist: []string{"github.com"},
	}

	result := ApplyPolicy(reg, policy)
	_, err := result.Execute(context.Background(), "search", "test query")
	if err == nil {
		t.Fatal("expected search to be denied when allowlist is active")
	}
	denial, ok := IsToolDenial(err)
	if !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
	if denial.ToolName != "search" {
		t.Errorf("denial.ToolName = %q, want search", denial.ToolName)
	}
}

func TestNetworkPolicyGuard_SearchAllowedWithoutAllowlist(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "search"})

	// AllowInternalNetworks without allowlist — context propagation only.
	policy := &CapabilityPolicy{
		Shell:                 true,
		Network:               true,
		Cron:                  true,
		MemoryWrite:           true,
		Spawn:                 true,
		AllowInternalNetworks: true,
	}

	result := ApplyPolicy(reg, policy)
	_, err := result.Execute(context.Background(), "search", "test query")
	if err != nil {
		t.Fatalf("expected search to be allowed without allowlist: %v", err)
	}
}

func TestNetworkPolicyGuard_BrowserNavigateDenied(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "browser"})

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
		NetworkAllowlist: []string{"github.com"},
	}

	result := ApplyPolicy(reg, policy)

	// Allowed navigation.
	_, err := result.Execute(context.Background(), "browser", "navigate", "https://github.com")
	if err != nil {
		t.Fatalf("expected github.com navigate to be allowed: %v", err)
	}

	// Denied navigation.
	_, err = result.Execute(context.Background(), "browser", "navigate", "https://evil.com")
	if err == nil {
		t.Fatal("expected evil.com navigate to be denied")
	}
	denial, ok := IsToolDenial(err)
	if !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
	if denial.ToolName != "browser" {
		t.Errorf("denial.ToolName = %q, want browser", denial.ToolName)
	}

	// Non-navigate commands should pass through.
	_, err = result.Execute(context.Background(), "browser", "snapshot")
	if err != nil {
		t.Fatalf("expected snapshot to pass through: %v", err)
	}
}

func TestNetworkPolicyGuard_BrowserOpenDenied(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "browser"})

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
		NetworkAllowlist: []string{"github.com"},
	}

	result := ApplyPolicy(reg, policy)

	_, err := result.Execute(context.Background(), "browser", "open", "https://evil.com")
	if err == nil {
		t.Fatal("expected browser open with denied host to fail")
	}
	if _, ok := IsToolDenial(err); !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
}

func TestNetworkPolicyGuard_PreservesSchemaAndJSON(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	schemaTool := &stubSchemaAndJSONTool{
		stubTool: &stubTool{name: "browser"},
		schema:   map[string]interface{}{"type": "object"},
	}
	reg.Register(schemaTool)

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
		NetworkAllowlist: []string{"github.com"},
	}

	result := ApplyPolicy(reg, policy)
	tool, ok := result.Get("browser")
	if !ok {
		t.Fatal("expected browser in registry")
	}

	// Schema preserved.
	ts, ok := tool.(ToolSchema)
	if !ok {
		t.Fatal("expected ToolSchema to be preserved")
	}
	if ts.GetSchema()["type"] != "object" {
		t.Error("schema not preserved")
	}

	// ExecuteJSON preserved and enforces policy.
	je, ok := tool.(interface {
		ExecuteJSON(context.Context, map[string]string) (string, error)
	})
	if !ok {
		t.Fatal("expected ExecuteJSON to be preserved")
	}

	// Denied navigate via JSON.
	_, err := je.ExecuteJSON(context.Background(), map[string]string{
		"command": "navigate",
		"url":     "https://evil.com",
	})
	if err == nil {
		t.Fatal("expected denial via ExecuteJSON")
	}
	if _, ok := IsToolDenial(err); !ok {
		t.Fatalf("expected ToolDenial from ExecuteJSON, got %v", err)
	}

	// Allowed navigate via JSON.
	_, err = je.ExecuteJSON(context.Background(), map[string]string{
		"command": "navigate",
		"url":     "https://github.com",
	})
	if err != nil {
		t.Fatalf("expected allowed navigate to succeed: %v", err)
	}
}

func TestNetworkPolicyGuard_BrowserTaskDeniedWithAllowlist(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	browserTaskTool := &stubSchemaAndJSONTool{
		stubTool: &stubTool{name: "browser_task"},
		schema:   map[string]interface{}{"type": "object"},
	}
	reg.Register(browserTaskTool)

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
		NetworkAllowlist: []string{"github.com"},
	}

	result := ApplyPolicy(reg, policy)
	tool, _ := result.Get("browser_task")

	je, ok := tool.(interface {
		ExecuteJSON(context.Context, map[string]string) (string, error)
	})
	if !ok {
		t.Fatal("expected ExecuteJSON to be preserved")
	}

	_, err := je.ExecuteJSON(context.Background(), map[string]string{"task": "visit evil.com"})
	if err == nil {
		t.Fatal("expected browser_task denied with allowlist")
	}
	denial, ok := IsToolDenial(err)
	if !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
	if denial.ToolName != "browser_task" {
		t.Errorf("denial.ToolName = %q, want browser_task", denial.ToolName)
	}
}

func TestNetworkPolicyGuard_BrowserTaskDeniedViaExecute(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	// Use a tool that implements both Execute and ExecuteJSON so the guard
	// wraps it as networkPolicyGuardWithJSON — the Execute path must still
	// check browser_task via checkExecArgs.
	browserTaskTool := &stubSchemaAndJSONTool{
		stubTool: &stubTool{name: "browser_task"},
		schema:   map[string]interface{}{"type": "object"},
	}
	reg.Register(browserTaskTool)

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
		NetworkAllowlist: []string{"github.com"},
	}

	result := ApplyPolicy(reg, policy)
	tool, _ := result.Get("browser_task")

	// Call via the positional-args Execute path (not ExecuteJSON).
	_, err := tool.Execute(context.Background(), "visit evil.com")
	if err == nil {
		t.Fatal("expected browser_task denied via Execute path with allowlist")
	}
	denial, ok := IsToolDenial(err)
	if !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
	if denial.ToolName != "browser_task" {
		t.Errorf("denial.ToolName = %q, want browser_task", denial.ToolName)
	}
}

// ---------------------------------------------------------------------------
// Context propagation
// ---------------------------------------------------------------------------

func TestContextWithNetworkPolicy_RoundTrip(t *testing.T) {
	t.Parallel()

	policy := &CapabilityPolicy{NetworkAllowlist: []string{"example.com"}}
	ctx := ContextWithNetworkPolicy(context.Background(), policy)

	got := NetworkPolicyFromContext(ctx)
	if got != policy {
		t.Fatal("expected policy round-trip through context")
	}

	// Nil context value.
	got = NetworkPolicyFromContext(context.Background())
	if got != nil {
		t.Fatal("expected nil from context without policy")
	}
}

// ---------------------------------------------------------------------------
// Redirect ToolDenial unwrapping
// ---------------------------------------------------------------------------

func TestIsToolDenial_UnwrapsURLError(t *testing.T) {
	t.Parallel()

	denial := &ToolDenial{
		ToolName: "web_fetch",
		Family:   "network",
		Reason:   "host blocked",
	}

	// Simulate http.Client wrapping CheckRedirect error in *url.Error.
	// The real http.Client returns *url.Error, which implements Unwrap().
	wrapped := fmt.Errorf("Get https://evil.com: %w", denial)

	got, ok := IsToolDenial(wrapped)
	if !ok {
		t.Fatal("expected IsToolDenial to unwrap error chain")
	}
	if got != denial {
		t.Fatal("expected same denial pointer")
	}
}

// ---------------------------------------------------------------------------
// Redirect enforcement in web_fetch (integration with httptest)
// ---------------------------------------------------------------------------

func TestWebFetch_RedirectToBlockedHost_ReturnsToolDenial(t *testing.T) {
	t.Parallel()

	// Server that redirects to a blocked host.
	redirectTarget := "https://evil.com/steal"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget, http.StatusFound)
	}))
	defer srv.Close()

	wf := NewWebFetchTool()

	// Inject network policy into context.
	policy := &CapabilityPolicy{
		Network:               true,
		NetworkAllowlist:      []string{"127.0.0.1", "localhost"},
		AllowInternalNetworks: true,
	}
	ctx := ContextWithNetworkPolicy(context.Background(), policy)

	_, err := wf.Execute(ctx, srv.URL)
	if err == nil {
		t.Fatal("expected error from redirect to blocked host")
	}

	denial, ok := IsToolDenial(err)
	if !ok {
		t.Fatalf("expected ToolDenial from redirect, got %T: %v", err, err)
	}
	if denial.ToolName != "web_fetch" {
		t.Errorf("denial.ToolName = %q, want web_fetch", denial.ToolName)
	}
	if denial.Family != "network" {
		t.Errorf("denial.Family = %q, want network", denial.Family)
	}
}

func TestWebFetch_AllowedRedirect_Succeeds(t *testing.T) {
	t.Parallel()

	// Server that redirects to itself (same host).
	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			w.Write([]byte("<html><body>Success</body></html>"))
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	})

	srv := httptest.NewServer(finalHandler)
	defer srv.Close()

	wf := NewWebFetchTool()

	policy := &CapabilityPolicy{
		Network:               true,
		NetworkAllowlist:      []string{"127.0.0.1", "localhost"},
		AllowInternalNetworks: true,
	}
	ctx := ContextWithNetworkPolicy(context.Background(), policy)

	result, err := wf.Execute(ctx, srv.URL)
	if err != nil {
		t.Fatalf("expected redirect to allowed host to succeed: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

// ---------------------------------------------------------------------------
// File scheme rejection
// ---------------------------------------------------------------------------

func TestCheckNetworkTarget_FileScheme(t *testing.T) {
	t.Parallel()

	policy := &CapabilityPolicy{Network: true}
	denial := CheckNetworkTarget("browser", "file:///etc/passwd", policy)
	if denial == nil {
		t.Fatal("expected file:// to be denied")
	}
	if denial.Family != "network" {
		t.Errorf("denial.Family = %q, want network", denial.Family)
	}
}

// ---------------------------------------------------------------------------
// Private IP detection
// ---------------------------------------------------------------------------

func TestCheckNetworkTarget_PrivateIPs(t *testing.T) {
	t.Parallel()

	policy := &CapabilityPolicy{Network: true}

	privateHosts := []string{
		"http://localhost/",
		"http://127.0.0.1/",
		"http://0.0.0.0/",
		"http://[::1]/",
		"http://secret.internal/",
		"http://myhost.local/",
	}

	for _, u := range privateHosts {
		denial := CheckNetworkTarget("web_fetch", u, policy)
		if denial == nil {
			t.Errorf("expected %q to be denied as private/loopback", u)
		}
	}

	// With AllowInternalNetworks, these should be allowed.
	policyAllow := &CapabilityPolicy{Network: true, AllowInternalNetworks: true}
	for _, u := range privateHosts {
		denial := CheckNetworkTarget("web_fetch", u, policyAllow)
		if denial != nil {
			t.Errorf("expected %q to be allowed with AllowInternalNetworks: %v", u, denial)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration: ApplyPolicy with network allowlist via resolver
// ---------------------------------------------------------------------------

func TestApplyPolicy_NetworkAllowlistWrapsTools(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "web_fetch"})
	reg.Register(&stubTool{name: "browser"})
	reg.Register(&stubTool{name: "search"})
	reg.Register(&stubTool{name: "file"}) // not a network tool

	policy := &CapabilityPolicy{
		Shell:            true,
		Network:          true,
		Cron:             true,
		MemoryWrite:      true,
		Spawn:            true,
		NetworkAllowlist: []string{"github.com"},
	}

	result := ApplyPolicy(reg, policy)

	// web_fetch with allowed host should work.
	_, err := result.Execute(context.Background(), "web_fetch", "https://github.com")
	if err != nil {
		t.Errorf("web_fetch to github.com should be allowed: %v", err)
	}

	// web_fetch with denied host.
	_, err = result.Execute(context.Background(), "web_fetch", "https://evil.com")
	if err == nil {
		t.Error("web_fetch to evil.com should be denied")
	}

	// browser navigate denied.
	_, err = result.Execute(context.Background(), "browser", "navigate", "https://evil.com")
	if err == nil {
		t.Error("browser navigate to evil.com should be denied")
	}

	// search denied with allowlist.
	_, err = result.Execute(context.Background(), "search", "query")
	if err == nil {
		t.Error("search should be denied with allowlist")
	}

	// file should be unaffected.
	_, err = result.Execute(context.Background(), "file", "read", "/tmp/test")
	if err != nil {
		t.Errorf("file should be unaffected by network policy: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SSRFSafeTransport — transport-layer IP enforcement
// ---------------------------------------------------------------------------

func TestSSRFSafeTransport_BlocksLoopbackAtDialTime(t *testing.T) {
	t.Parallel()

	// Use a test server bound to 127.0.0.1 to verify the transport blocks it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should not reach here"))
	}))
	defer srv.Close()

	transport := SSRFSafeTransport(false)
	client := &http.Client{Transport: transport}

	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("expected SSRFSafeTransport to block loopback connection")
	}
	if !strings.Contains(err.Error(), "private/loopback") {
		t.Errorf("expected private/loopback error, got: %v", err)
	}
}

func TestSSRFSafeTransport_AllowsLoopbackWhenInternal(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	transport := SSRFSafeTransport(true)
	client := &http.Client{Transport: transport}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("expected loopback allowed with allowInternal=true: %v", err)
	}
	resp.Body.Close()
}

func TestApplyPolicy_EmptyAllowlistNoRestriction(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "web_fetch"})
	reg.Register(&stubTool{name: "search"})

	// Fully permissive policy — no allowlist.
	policy := &CapabilityPolicy{
		Shell:       true,
		Network:     true,
		Cron:        true,
		MemoryWrite: true,
		Spawn:       true,
	}

	result := ApplyPolicy(reg, policy)

	// All tools should work.
	for _, name := range []string{"web_fetch", "search"} {
		if _, err := result.Execute(context.Background(), name, "test"); err != nil {
			t.Errorf("expected %q to pass with empty allowlist: %v", name, err)
		}
	}
}
