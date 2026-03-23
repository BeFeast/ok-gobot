package agent

import (
	"context"
	"testing"

	"ok-gobot/internal/tools"
)

func TestBuildToolRegistry_PolicyDeniesShell(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistry()
	base.Register(&resolverDangerousTool{name: "local"})
	base.Register(&resolverDangerousTool{name: "file"})

	resolver := &RunResolver{ToolRegistry: base}
	profile := &AgentProfile{
		Policy: &tools.CapabilityPolicy{
			Shell:       false,
			Network:     true,
			Cron:        true,
			MemoryWrite: true,
			Spawn:       true,
		},
	}

	reg := resolver.buildToolRegistry(0, profile, false, nil)

	// local should be denied.
	_, err := reg.Execute(context.Background(), "local", "ls")
	if err == nil {
		t.Fatal("expected policy to block local tool")
	}
	denial, isDenial := tools.IsToolDenial(err)
	if !isDenial {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
	if denial.Family != "shell" {
		t.Errorf("denial.Family = %q, want shell", denial.Family)
	}

	// file should work.
	_, err = reg.Execute(context.Background(), "file", "read")
	if err != nil {
		t.Fatalf("expected file tool to be allowed: %v", err)
	}
}

func TestBuildToolRegistry_PolicyDeniesNetwork(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistry()
	base.Register(&resolverDangerousTool{name: "web_fetch"})
	base.Register(&resolverDangerousTool{name: "local"})

	resolver := &RunResolver{ToolRegistry: base}
	profile := &AgentProfile{
		Policy: &tools.CapabilityPolicy{
			Shell:       true,
			Network:     false,
			Cron:        true,
			MemoryWrite: true,
			Spawn:       true,
		},
	}

	reg := resolver.buildToolRegistry(0, profile, false, nil)

	// web_fetch should be denied.
	_, err := reg.Execute(context.Background(), "web_fetch", "https://example.com")
	if err == nil {
		t.Fatal("expected policy to block web_fetch")
	}
	if _, ok := tools.IsToolDenial(err); !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}

	// local should work.
	_, err = reg.Execute(context.Background(), "local", "echo hi")
	if err != nil {
		t.Fatalf("expected local to be allowed: %v", err)
	}
}

func TestBuildToolRegistry_PolicyDeniesPerChatCronTool(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistry()
	base.Register(&resolverDangerousTool{name: "local"})

	resolver := &RunResolver{
		ToolRegistry: base,
		Scheduler:    noopCronScheduler{},
	}
	profile := &AgentProfile{
		Policy: &tools.CapabilityPolicy{
			Shell:       true,
			Network:     true,
			Cron:        false,
			MemoryWrite: true,
			Spawn:       true,
		},
	}

	// chatID != 0 triggers per-chat tool injection (cron).
	reg := resolver.buildToolRegistry(123, profile, false, nil)

	// cron should be denied even though it was injected per-chat.
	_, err := reg.Execute(context.Background(), "cron", "list")
	if err == nil {
		t.Fatal("expected policy to block per-chat cron tool")
	}
	if _, ok := tools.IsToolDenial(err); !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
}

func TestBuildToolRegistry_NilPolicyAllowsAll(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistry()
	base.Register(&resolverDangerousTool{name: "local"})
	base.Register(&resolverDangerousTool{name: "web_fetch"})

	resolver := &RunResolver{ToolRegistry: base}
	profile := &AgentProfile{} // nil Policy

	reg := resolver.buildToolRegistry(0, profile, false, nil)

	for _, name := range []string{"local", "web_fetch"} {
		if _, err := reg.Execute(context.Background(), name, "test"); err != nil {
			t.Errorf("expected %q to be allowed with nil policy: %v", name, err)
		}
	}
}

func TestBuildToolRegistry_PolicyWithAllowedTools(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistry()
	base.Register(&resolverDangerousTool{name: "local"})
	base.Register(&resolverDangerousTool{name: "file"})
	base.Register(&resolverDangerousTool{name: "web_fetch"})

	resolver := &RunResolver{ToolRegistry: base}
	profile := &AgentProfile{
		AllowedTools: []string{"file", "web_fetch"}, // local filtered out by allowlist
		Policy: &tools.CapabilityPolicy{
			Shell:       true,
			Network:     false, // denies web_fetch via policy
			Cron:        true,
			MemoryWrite: true,
			Spawn:       true,
		},
	}

	reg := resolver.buildToolRegistry(0, profile, false, nil)

	// local filtered by allowed_tools — not in registry at all.
	_, ok := reg.Get("local")
	if ok {
		t.Error("expected local to be filtered out by allowed_tools")
	}

	// web_fetch is in allowed_tools but denied by policy.
	_, err := reg.Execute(context.Background(), "web_fetch", "https://example.com")
	if err == nil {
		t.Fatal("expected web_fetch denied by policy even though in allowed_tools")
	}
	if _, ok := tools.IsToolDenial(err); !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}

	// file should work.
	_, err = reg.Execute(context.Background(), "file", "read")
	if err != nil {
		t.Fatalf("expected file to be allowed: %v", err)
	}
}

func TestBuildToolRegistry_PolicyAndEstopStack(t *testing.T) {
	t.Parallel()

	// Estop is OFF but policy denies shell.
	base := tools.NewRegistryWithEmergencyStop(testEmergencyStopProvider{enabled: false})
	localTool := &resolverDangerousTool{name: "local"}
	base.Register(localTool)

	resolver := &RunResolver{ToolRegistry: base}
	profile := &AgentProfile{
		Policy: &tools.CapabilityPolicy{
			Shell:       false,
			Network:     true,
			Cron:        true,
			MemoryWrite: true,
			Spawn:       true,
		},
	}

	reg := resolver.buildToolRegistry(0, profile, false, nil)

	_, err := reg.Execute(context.Background(), "local", "ls")
	if err == nil {
		t.Fatal("expected policy to block local even with estop off")
	}
	denial, isDenial := tools.IsToolDenial(err)
	if !isDenial {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
	// Policy denial should reference capability, not estop.
	if denial.Family != "shell" {
		t.Errorf("denial.Family = %q, want shell", denial.Family)
	}
	if localTool.called != 0 {
		t.Errorf("expected local tool not to execute, called=%d", localTool.called)
	}
}

func TestBuildToolRegistry_FileReadOnlyViaBuildToolRegistry(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistry()
	base.Register(&resolverDangerousTool{name: "file"})

	resolver := &RunResolver{ToolRegistry: base}
	profile := &AgentProfile{
		Policy: &tools.CapabilityPolicy{
			Shell:        true,
			Network:      true,
			Cron:         true,
			MemoryWrite:  true,
			Spawn:        true,
			FileReadOnly: true,
		},
	}

	reg := resolver.buildToolRegistry(0, profile, false, nil)

	// Read should work.
	_, err := reg.Execute(context.Background(), "file", "read", "/tmp/test")
	if err != nil {
		t.Fatalf("expected file read to pass: %v", err)
	}

	// Write should be denied.
	_, err = reg.Execute(context.Background(), "file", "write", "/tmp/test", "data")
	if err == nil {
		t.Fatal("expected file write to be denied by policy")
	}
	if _, ok := tools.IsToolDenial(err); !ok {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
}
