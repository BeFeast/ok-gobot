package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/config"
	"ok-gobot/internal/role"
	"ok-gobot/internal/runtime"
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
	withFakeRoleRunner(t)
	_, cfg := newTestStore(t)

	cmd := newRolesCommand(cfg)
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
}

func TestRolesRun_WithInput(t *testing.T) {
	withFakeRoleRunner(t)
	_, cfg := newTestStore(t)

	cmd := newRolesCommand(cfg)
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
}

func TestRolesRun_WithTier(t *testing.T) {
	withFakeRoleRunner(t)
	_, cfg := newTestStore(t)

	cmd := newRolesCommand(cfg)
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
}

func withFakeRoleRunner(t *testing.T) {
	t.Helper()
	old := runRoleWithHubCLI
	runRoleWithHubCLI = func(
		ctx context.Context,
		cfg *config.Config,
		store *storage.Store,
		m *role.Manifest,
		input string,
		worker string,
		sessionKey agent.SessionKey,
		onToolEvent func(agent.ToolEvent),
	) (runtime.JobRunResult, error) {
		if onToolEvent != nil {
			onToolEvent(agent.ToolEvent{ToolName: "file", Type: agent.ToolEventStarted})
			onToolEvent(agent.ToolEvent{ToolName: "file", Type: agent.ToolEventFinished})
		}
		return runtime.JobRunResult{Summary: "role completed"}, nil
	}
	t.Cleanup(func() {
		runRoleWithHubCLI = old
	})
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
