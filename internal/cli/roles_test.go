package cli

import (
	"os"
	"path/filepath"
	"testing"

	"ok-gobot/internal/config"
	"ok-gobot/internal/role"
)

func TestRolesListCommand(t *testing.T) {
	cfg := &config.Config{}
	cmd := newRolesCommand(cfg)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("roles list: %v", err)
	}
}

func TestRolesInitCommand_NoDirFlag(t *testing.T) {
	cfg := &config.Config{}
	cmd := newRolesInitCommand(cfg)

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error when --dir not set and no config")
	}
}

func TestRolesInitCommand_WithDir(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cmd := newRolesCommand(cfg)
	cmd.SetArgs([]string{"init", "--dir", dir})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("roles init --dir %s: %v", dir, err)
	}

	// Verify files were scaffolded
	names, _ := role.BundledNames()
	for _, name := range names {
		path := filepath.Join(dir, name+".md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file %s to exist after init", path)
		}
	}
}
