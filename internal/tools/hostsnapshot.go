package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

// Snapshotter takes a restore point before a batch of host operations.
type Snapshotter interface {
	// Snapshot creates a restore point tagged with tag and returns its short id.
	Snapshot(ctx context.Context, tag string) (id string, err error)
}

// ResticSnapshotter takes a restic snapshot of the agent's workspaces before a
// host_task worker runs, so any file the worker touches can be reverted. Scope is
// deliberately the openclaw/ok-gobot workspaces (config + notes + soul), not the
// whole box — the VM itself is the coarse backstop (Proxmox snapshot). Restore:
//
//	restic -r <repo> --password-file <pf> restore <id> --target / --include <path>
type ResticSnapshotter struct {
	Binary       string
	Repo         string
	PasswordFile string
	ExcludeFile  string
	Roots        []string
	Timeout      time.Duration
}

var resticSnapshotID = regexp.MustCompile(`snapshot ([0-9a-f]{8}) saved`)

// NewResticSnapshotter builds a snapshotter with the shtrudel workspace layout.
// Returns nil when the home dir cannot be resolved (snapshots then disabled).
func NewResticSnapshotter() *ResticSnapshotter {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return &ResticSnapshotter{
		Binary:       "restic",
		Repo:         filepath.Join(home, ".ok-gobot-restic"),
		PasswordFile: filepath.Join(home, ".ok-gobot", "restic-repo.pass"),
		ExcludeFile:  filepath.Join(home, ".ok-gobot", "restic-excludes.txt"),
		Roots: []string{
			filepath.Join(home, ".ok-gobot"),
			filepath.Join(home, "ok-gobot-soul"),
			filepath.Join(home, "Obsidian Vault"),
			filepath.Join(home, ".openclaw"),
			filepath.Join(home, ".local", "share", "opencode"),
			filepath.Join(home, ".config", "opencode"),
		},
		Timeout: 5 * time.Minute,
	}
}

// existingRoots returns the configured roots that currently exist (restic errors
// on a missing path, and workspaces come and go).
func (s *ResticSnapshotter) existingRoots() []string {
	var out []string
	for _, r := range s.Roots {
		if _, err := os.Stat(r); err == nil {
			out = append(out, r)
		}
	}
	return out
}

func (s *ResticSnapshotter) Snapshot(ctx context.Context, tag string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("snapshotter not configured")
	}
	if _, err := os.Stat(s.PasswordFile); err != nil {
		return "", fmt.Errorf("restic password file missing: %w", err)
	}
	roots := s.existingRoots()
	if len(roots) == 0 {
		return "", fmt.Errorf("no snapshot roots exist")
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"backup", "--tag", tag}
	if s.ExcludeFile != "" {
		if _, err := os.Stat(s.ExcludeFile); err == nil {
			args = append(args, "--exclude-file", s.ExcludeFile)
		}
	}
	args = append(args, roots...)

	cmd := exec.CommandContext(ctx, s.Binary, args...)
	cmd.Env = append(os.Environ(),
		"RESTIC_REPOSITORY="+s.Repo,
		"RESTIC_PASSWORD_FILE="+s.PasswordFile,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("restic backup failed: %w", err)
	}
	if m := resticSnapshotID.FindSubmatch(out); m != nil {
		return string(m[1]), nil
	}
	// Backup succeeded but the id line was not found (e.g. "unchanged"): report
	// success without an id rather than a false failure.
	return "", nil
}
