package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func TestEstopCommand_TogglesAndReportsStatus(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "cli.db")
	cfg := &config.Config{StoragePath: dbPath}
	cmd := newEstopCommand(cfg)

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	cmd.SetArgs([]string{"on"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(on) error = %v", err)
	}
	if !strings.Contains(out.String(), "estop is ON") {
		t.Fatalf("unexpected on output: %q", out.String())
	}

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close() //nolint:errcheck

	enabled, err := store.IsEmergencyStopEnabled()
	if err != nil {
		t.Fatalf("IsEmergencyStopEnabled() error = %v", err)
	}
	if !enabled {
		t.Fatal("expected estop to be enabled after CLI on")
	}

	out.Reset()
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(status) error = %v", err)
	}
	if !strings.Contains(out.String(), "Disabled tool families") {
		t.Fatalf("unexpected status output: %q", out.String())
	}

	out.Reset()
	cmd.SetArgs([]string{"off"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(off) error = %v", err)
	}
	if !strings.Contains(out.String(), "estop is OFF") {
		t.Fatalf("unexpected off output: %q", out.String())
	}
}
