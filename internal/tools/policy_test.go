package tools

import (
	"context"
	"testing"
)

func TestCapabilityPolicy_DeniedCapability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policy   CapabilityPolicy
		tool     string
		wantDeny string
	}{
		{"shell denied blocks local", CapabilityPolicy{Network: true, Cron: true, MemoryWrite: true, Spawn: true}, "local", "shell"},
		{"shell denied blocks ssh", CapabilityPolicy{Network: true, Cron: true, MemoryWrite: true, Spawn: true}, "ssh", "shell"},
		{"network denied blocks web_fetch", CapabilityPolicy{Shell: true, Cron: true, MemoryWrite: true, Spawn: true}, "web_fetch", "network"},
		{"network denied blocks search", CapabilityPolicy{Shell: true, Cron: true, MemoryWrite: true, Spawn: true}, "search", "network"},
		{"network denied blocks browser", CapabilityPolicy{Shell: true, Cron: true, MemoryWrite: true, Spawn: true}, "browser", "network"},
		{"network denied blocks browser_task", CapabilityPolicy{Shell: true, Cron: true, MemoryWrite: true}, "browser_task", "network"},
		{"spawn denied blocks browser_task", CapabilityPolicy{Shell: true, Network: true, Cron: true, MemoryWrite: true}, "browser_task", "spawn"},
		{"cron denied blocks cron", CapabilityPolicy{Shell: true, Network: true, MemoryWrite: true, Spawn: true}, "cron", "cron"},
		{"all allowed passes file", CapabilityPolicy{Shell: true, Network: true, Cron: true, MemoryWrite: true, Spawn: true}, "file", ""},
		{"unmapped tool allowed", CapabilityPolicy{}, "image", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.policy.DeniedCapability(tt.tool)
			if got != tt.wantDeny {
				t.Errorf("DeniedCapability(%q) = %q, want %q", tt.tool, got, tt.wantDeny)
			}
		})
	}
}

func TestApplyPolicy_DeniedToolReturnsDenial(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "local"})
	reg.Register(&stubTool{name: "file"})

	policy := &CapabilityPolicy{
		Shell:       false,
		Network:     true,
		Cron:        true,
		MemoryWrite: true,
		Spawn:       true,
	}

	result := ApplyPolicy(reg, policy)

	// local should be denied.
	_, err := result.Execute(context.Background(), "local", "ls")
	if err == nil {
		t.Fatal("expected policy denial for local tool")
	}
	denial, isDenial := IsToolDenial(err)
	if !isDenial {
		t.Fatalf("expected ToolDenial, got %v", err)
	}
	if denial.ToolName != "local" {
		t.Errorf("denial.ToolName = %q, want local", denial.ToolName)
	}
	if denial.Family != "shell" {
		t.Errorf("denial.Family = %q, want shell", denial.Family)
	}

	// file should pass through.
	got, err := result.Execute(context.Background(), "file", "read", "/tmp/test")
	if err != nil {
		t.Fatalf("expected file tool to be allowed, got error: %v", err)
	}
	if got != "ok" {
		t.Errorf("file tool result = %q, want ok", got)
	}
}

func TestApplyPolicy_NilPolicyReturnsUnchanged(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "local"})

	result := ApplyPolicy(reg, nil)
	if result != reg {
		t.Error("nil policy should return the same registry")
	}
}

func TestApplyPolicy_PreservesToolSchema(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	schemaTool := &stubSchemaAndJSONTool{
		stubTool: &stubTool{name: "browser_task"},
		schema:   map[string]interface{}{"type": "object"},
	}
	reg.Register(schemaTool)

	policy := &CapabilityPolicy{
		Shell:   true,
		Network: true,
		Cron:    true,
		Spawn:   false, // denies browser_task
	}

	result := ApplyPolicy(reg, policy)
	tool, ok := result.Get("browser_task")
	if !ok {
		t.Fatal("expected browser_task to be in registry")
	}

	// Schema should be preserved.
	ts, ok := tool.(ToolSchema)
	if !ok {
		t.Fatal("expected wrapped tool to preserve ToolSchema")
	}
	schema := ts.GetSchema()
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}

	// ExecuteJSON should be denied.
	je, ok := tool.(interface {
		ExecuteJSON(context.Context, map[string]string) (string, error)
	})
	if !ok {
		t.Fatal("expected wrapped tool to preserve ExecuteJSON")
	}
	_, err := je.ExecuteJSON(context.Background(), map[string]string{"task": "test"})
	if err == nil {
		t.Fatal("expected policy denial via ExecuteJSON")
	}
	if _, ok := IsToolDenial(err); !ok {
		t.Fatalf("expected ToolDenial from ExecuteJSON, got %v", err)
	}
}

func TestApplyPolicy_FileReadOnlyBlocksWrites(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "file"})
	reg.Register(&stubTool{name: "patch"})

	policy := &CapabilityPolicy{
		Shell:        true,
		Network:      true,
		Cron:         true,
		MemoryWrite:  true,
		Spawn:        true,
		FileReadOnly: true,
	}

	result := ApplyPolicy(reg, policy)

	// file read should work.
	_, err := result.Execute(context.Background(), "file", "read", "/tmp/test")
	if err != nil {
		t.Fatalf("expected file read to be allowed: %v", err)
	}

	// file write should be denied.
	_, err = result.Execute(context.Background(), "file", "write", "/tmp/test", "content")
	if err == nil {
		t.Fatal("expected file write to be denied")
	}
	denial, isDenial := IsToolDenial(err)
	if !isDenial {
		t.Fatalf("expected ToolDenial for file write, got %v", err)
	}
	if denial.Family != "file_write" {
		t.Errorf("denial.Family = %q, want file_write", denial.Family)
	}

	// patch should be denied entirely (always a write).
	_, err = result.Execute(context.Background(), "patch", "/tmp/test", "diff content")
	if err == nil {
		t.Fatal("expected patch to be denied")
	}
	denial, isDenial = IsToolDenial(err)
	if !isDenial {
		t.Fatalf("expected ToolDenial for patch, got %v", err)
	}
	if denial.Family != "file_write" {
		t.Errorf("denial.Family = %q, want file_write", denial.Family)
	}
}

func TestApplyPolicy_FilesystemRootsBlocksAbsolutePath(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "file"})

	policy := &CapabilityPolicy{
		Shell:           true,
		Network:         true,
		Cron:            true,
		MemoryWrite:     true,
		Spawn:           true,
		FilesystemRoots: []string{"/home/bot/workspace"},
	}

	result := ApplyPolicy(reg, policy)

	// Allowed path.
	_, err := result.Execute(context.Background(), "file", "read", "/home/bot/workspace/test.txt")
	if err != nil {
		t.Fatalf("expected path under workspace to be allowed: %v", err)
	}

	// Disallowed absolute path.
	_, err = result.Execute(context.Background(), "file", "read", "/etc/passwd")
	if err == nil {
		t.Fatal("expected path outside workspace to be denied")
	}
	denial, isDenial := IsToolDenial(err)
	if !isDenial {
		t.Fatalf("expected ToolDenial for path outside roots, got %v", err)
	}
	if denial.Family != "filesystem" {
		t.Errorf("denial.Family = %q, want filesystem", denial.Family)
	}

	// Relative path should be allowed (tool's own BasePath scopes it).
	_, err = result.Execute(context.Background(), "file", "read", "relative/file.txt")
	if err != nil {
		t.Fatalf("expected relative path to be allowed: %v", err)
	}
}

func TestApplyPolicy_BrowserTaskDeniedByEitherCapability(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "browser_task"})

	// Deny network but allow spawn.
	policy1 := &CapabilityPolicy{
		Shell:   true,
		Network: false,
		Cron:    true,
		Spawn:   true,
	}
	r1 := ApplyPolicy(reg, policy1)
	_, err := r1.Execute(context.Background(), "browser_task", "test")
	if err == nil {
		t.Fatal("expected browser_task denied when network=false")
	}
	d, ok := IsToolDenial(err)
	if !ok || d.Family != "network" {
		t.Fatalf("expected denial Family=network, got %v", d)
	}

	// Allow network but deny spawn.
	policy2 := &CapabilityPolicy{
		Shell:   true,
		Network: true,
		Cron:    true,
		Spawn:   false,
	}
	r2 := ApplyPolicy(reg, policy2)
	_, err = r2.Execute(context.Background(), "browser_task", "test")
	if err == nil {
		t.Fatal("expected browser_task denied when spawn=false")
	}
	d, ok = IsToolDenial(err)
	if !ok || d.Family != "spawn" {
		t.Fatalf("expected denial Family=spawn, got %v", d)
	}
}

func TestApplyPolicy_FullyPermissivePolicyAllowsAll(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(&stubTool{name: "local"})
	reg.Register(&stubTool{name: "web_fetch"})
	reg.Register(&stubTool{name: "cron"})
	reg.Register(&stubTool{name: "browser_task"})

	policy := &CapabilityPolicy{
		Shell:       true,
		Network:     true,
		Cron:        true,
		MemoryWrite: true,
		Spawn:       true,
	}

	result := ApplyPolicy(reg, policy)
	for _, name := range []string{"local", "web_fetch", "cron", "browser_task"} {
		if _, err := result.Execute(context.Background(), name, "test"); err != nil {
			t.Errorf("expected %q to be allowed with permissive policy, got %v", name, err)
		}
	}
}

func TestIsPathInRoots(t *testing.T) {
	t.Parallel()

	roots := []string{"/home/bot/workspace", "/tmp/data"}

	tests := []struct {
		path string
		want bool
	}{
		{"/home/bot/workspace/file.txt", true},
		{"/home/bot/workspace", true},
		{"/tmp/data/deep/file", true},
		{"/etc/passwd", false},
		{"/home/bot", false},
		{"/home/bot/workspacex", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			got := isPathInRoots(tt.path, roots)
			if got != tt.want {
				t.Errorf("isPathInRoots(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// Test helpers.

type stubSchemaAndJSONTool struct {
	*stubTool
	schema     map[string]interface{}
	jsonCalled int
}

func (s *stubSchemaAndJSONTool) GetSchema() map[string]interface{} {
	return s.schema
}

func (s *stubSchemaAndJSONTool) ExecuteJSON(_ context.Context, _ map[string]string) (string, error) {
	s.jsonCalled++
	return "ok", nil
}
