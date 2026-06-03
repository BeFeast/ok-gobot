package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/config"
	"ok-gobot/internal/rolejob"
	"ok-gobot/internal/storage"
)

func TestRolesList_ShowsBundled(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	// Bundled roles should be listed.
	if !strings.Contains(output, "researcher") {
		t.Errorf("expected 'researcher' in output: %q", output)
	}
	if !strings.Contains(output, "bundled") {
		t.Errorf("expected 'bundled' in output: %q", output)
	}
}

func TestRolesList_ShowsDiskRoles(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	// Create a temp roles directory with a custom role.
	rolesDir := filepath.Join(t.TempDir(), "roles")
	if err := os.MkdirAll(rolesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolesDir, "custom-role.md"), []byte(`---
worker: cheap
---
# Custom Role
You are a custom role.
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg.RolesPath = rolesDir

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "custom-role") {
		t.Errorf("expected 'custom-role' in output: %q", output)
	}
	if !strings.Contains(output, "disk") {
		t.Errorf("expected 'disk' in output: %q", output)
	}
}

func TestRolesShow(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"show", "researcher"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	for _, want := range []string{"researcher", "standard", "Prompt:", "auto"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in output: %q", want, output)
		}
	}
}

func TestRolesShow_BudgetsAndModel(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	rolesDir := filepath.Join(t.TempDir(), "roles")
	if err := os.MkdirAll(rolesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolesDir, "audited.md"), []byte(`---
worker: standard
model: claude-sonnet-4-6
max_tool_calls: 7
max_duration: 90s
max_tokens: 12000
max_cost_usd: 0.42
memory_policy: read_only
---
# Audited
Test budgets render.
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg.RolesPath = rolesDir

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"show", "audited"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	for _, want := range []string{
		"Model:     claude-sonnet-4-6",
		"MaxToolCalls: 7",
		"MaxDuration:  1m30s",
		"MaxTokens:    12000",
		"MaxCostUSD:   0.42",
		"MemoryPolicy: read_only",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in output: %q", want, output)
		}
	}
}

func TestRolesShow_OmitsZeroBudgets(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	rolesDir := filepath.Join(t.TempDir(), "roles")
	if err := os.MkdirAll(rolesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolesDir, "minimal.md"), []byte(`---
worker: cheap
---
# Minimal
No budgets.
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg.RolesPath = rolesDir

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"show", "minimal"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	for _, unwanted := range []string{"Model:", "MaxToolCalls:", "MaxDuration:", "MaxTokens:", "MaxCostUSD:", "MemoryPolicy:"} {
		if strings.Contains(output, unwanted) {
			t.Errorf("did not expect %q in output: %q", unwanted, output)
		}
	}
}

func TestRolesList_IncludesModelColumn(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	rolesDir := filepath.Join(t.TempDir(), "roles")
	if err := os.MkdirAll(rolesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolesDir, "tiered.md"), []byte(`---
worker: standard
model: claude-opus-4-7
---
# Tiered
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg.RolesPath = rolesDir

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "MODEL") {
		t.Errorf("expected MODEL column header in output: %q", output)
	}
	if !strings.Contains(output, "claude-opus-4-7") {
		t.Errorf("expected model value 'claude-opus-4-7' in output: %q", output)
	}
}

func TestRolesShow_NotFound(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"show", "nonexistent"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestRolesRun(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)
	deps, _ := newRolesRunTestDeps("role completed from worker")

	cmd := newRolesCommandWithDeps(cfg, deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"run", "researcher"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "Job started:") {
		t.Errorf("expected 'Job started:' in output: %q", output)
	}
	if !strings.Contains(output, "job-") {
		t.Errorf("expected job ID in output: %q", output)
	}

	jobs, err := store.ListJobs(1)
	if err != nil {
		t.Fatalf("ListJobs error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("job count = %d, want 1", len(jobs))
	}
	if jobs[0].Summary != "role completed from worker" {
		t.Fatalf("Summary = %q, want worker result", jobs[0].Summary)
	}
	if jobs[0].RoleName != "researcher" || jobs[0].Kind != "role" || jobs[0].MaxToolCalls == 0 {
		t.Fatalf("unexpected role metadata: %+v", jobs[0])
	}
}

func TestRolesRun_WithInput(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)
	deps, hub := newRolesRunTestDeps("input result")

	cmd := newRolesCommandWithDeps(cfg, deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"run", "researcher", "--input", "Go programming news"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if !strings.Contains(out.String(), "Job started:") {
		t.Errorf("expected 'Job started:' in output: %q", out.String())
	}
	req := hub.request(t)
	if !strings.Contains(req.Content, "User input: Go programming news") {
		t.Fatalf("role input not passed to runner: %q", req.Content)
	}
}

func TestRolesRun_WithTier(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)
	deps, _ := newRolesRunTestDeps("tier result")

	cmd := newRolesCommandWithDeps(cfg, deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"run", "researcher", "--tier", "premium"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if !strings.Contains(out.String(), "Job started:") {
		t.Errorf("expected 'Job started:' in output: %q", out.String())
	}
	jobs, err := store.ListJobs(1)
	if err != nil {
		t.Fatalf("ListJobs error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("job count = %d, want 1", len(jobs))
	}
	if jobs[0].Worker != "premium" {
		t.Fatalf("Worker = %q, want premium", jobs[0].Worker)
	}
}

func TestRolesRun_NotFound(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"run", "nonexistent"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestRolesEnable_NoCronJob(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"enable", "researcher"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no cron job exists")
	}
	if !strings.Contains(err.Error(), "no cron job found") {
		t.Fatalf("expected 'no cron job found' error, got: %v", err)
	}
}

func TestRolesDisable_WithCronJob(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	// Create a role cron job.
	_, err := store.SaveCronJob("0 0 9 * * *", "[role:researcher] # Researcher prompt", 42)
	if err != nil {
		t.Fatal(err)
	}

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"disable", "researcher"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if !strings.Contains(out.String(), "disabled") {
		t.Errorf("expected 'disabled' in output: %q", out.String())
	}
}

func TestRolesEnable_WithCronJob(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	// Create a disabled role cron job.
	id, err := store.SaveCronJob("0 0 9 * * *", "[role:researcher] # Researcher prompt", 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ToggleCronJob(id, false); err != nil {
		t.Fatal(err)
	}

	cmd := newRolesCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"enable", "researcher"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if !strings.Contains(out.String(), "enabled") {
		t.Errorf("expected 'enabled' in output: %q", out.String())
	}
}

type rolesRunTestHub struct {
	content string
	reqs    chan agent.RunRequest
}

func (h *rolesRunTestHub) Submit(req agent.RunRequest) <-chan agent.RunEvent {
	h.reqs <- req
	ch := make(chan agent.RunEvent, 1)
	ch <- agent.RunEvent{Type: agent.RunEventDone, Result: &agent.AgentResponse{Message: h.content}}
	close(ch)
	return ch
}

func (h *rolesRunTestHub) request(t *testing.T) agent.RunRequest {
	t.Helper()
	select {
	case req := <-h.reqs:
		return req
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for role request")
		return agent.RunRequest{}
	}
}

func newRolesRunTestDeps(content string) (roleRunDeps, *rolesRunTestHub) {
	hub := &rolesRunTestHub{content: content, reqs: make(chan agent.RunRequest, 1)}
	return roleRunDeps{
		newSubmitter: func(_ *config.Config, _ *storage.Store) (rolejob.AgentSubmitter, error) {
			return hub, nil
		},
		waitPoll: 10 * time.Millisecond,
	}, hub
}
