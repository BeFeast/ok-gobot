package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Snapshotter takes a restore point before a batch of host operations.
type Snapshotter interface {
	// Snapshot creates a restore point tagged with tag and returns a short id
	// describing what was captured.
	Snapshot(ctx context.Context, tag string) (id string, err error)
}

// BtrfsSnapshotter takes instant, read-only btrfs snapshots of the agent's
// migrated workspace subvolumes (~/.ok-gobot, ~/ok-gobot-soul). Snapshots are
// CoW — creation is ~10ms regardless of size — so they can be taken before every
// run cheaply. Old snapshots are pruned to Keep per subvolume. Restore a file:
//
//	cp ~/.btrfs/snapshots/<subvol>-<ts>/<relpath> ~/.btrfs/<subvol>/<relpath>
type BtrfsSnapshotter struct {
	Binary       string
	Subvolumes   []string // absolute paths to the subvolumes to snapshot
	SnapshotsDir string   // where read-only snapshots are written
	Keep         int      // snapshots to retain per subvolume
	Timeout      time.Duration
}

// NewBtrfsSnapshotter returns a snapshotter for the host btrfs workspace layout, or nil
// if the btrfs mount is not present (falls back to restic-only).
func NewBtrfsSnapshotter() *BtrfsSnapshotter {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	root := filepath.Join(home, ".btrfs")
	snaps := filepath.Join(root, "snapshots")
	if _, err := os.Stat(snaps); err != nil {
		return nil
	}
	var subs []string
	for _, sv := range []string{"ok-gobot", "soul"} {
		p := filepath.Join(root, sv)
		if _, err := os.Stat(p); err == nil {
			subs = append(subs, p)
		}
	}
	if len(subs) == 0 {
		return nil
	}
	return &BtrfsSnapshotter{
		Binary:       "btrfs",
		Subvolumes:   subs,
		SnapshotsDir: snaps,
		Keep:         50,
		Timeout:      30 * time.Second,
	}
}

func (b *BtrfsSnapshotter) Snapshot(ctx context.Context, tag string) (string, error) {
	if b == nil || len(b.Subvolumes) == 0 {
		return "", fmt.Errorf("btrfs snapshotter not configured")
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stamp := time.Now().Format("20060102T150405.000")
	var made []string
	for _, sv := range b.Subvolumes {
		base := filepath.Base(sv)
		dest := filepath.Join(b.SnapshotsDir, base+"-"+stamp)
		cmd := exec.CommandContext(ctx, b.Binary, "subvolume", "snapshot", "-r", sv, dest)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("btrfs snapshot %s failed: %w: %s", base, err, strings.TrimSpace(string(out)))
		}
		made = append(made, base)
		b.prune(ctx, base)
	}
	return stamp, nil
}

// prune deletes all but the newest Keep snapshots for a subvolume base name.
func (b *BtrfsSnapshotter) prune(ctx context.Context, base string) {
	if b.Keep <= 0 {
		return
	}
	entries, err := os.ReadDir(b.SnapshotsDir)
	if err != nil {
		return
	}
	var names []string
	prefix := base + "-"
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= b.Keep {
		return
	}
	sort.Strings(names) // timestamp-sortable
	for _, old := range names[:len(names)-b.Keep] {
		_ = exec.CommandContext(ctx, b.Binary, "subvolume", "delete", filepath.Join(b.SnapshotsDir, old)).Run()
	}
}

// ResticSnapshotter takes a restic snapshot of the ext4-resident workspaces the
// agent may write to that are not on btrfs (the syncthing-managed vault and
// openclaw workspace, plus opencode config). Restore:
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

// NewResticSnapshotter builds a snapshotter for the ext4-resident workspaces.
// Returns nil when the home dir cannot be resolved.
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
			filepath.Join(home, "Obsidian Vault"),
			filepath.Join(home, ".openclaw"),
			filepath.Join(home, ".local", "share", "opencode"),
			filepath.Join(home, ".config", "opencode"),
		},
		Timeout: 5 * time.Minute,
	}
}

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
	return "", nil
}

// RunSnapshotter captures a restore point across both storage backends: instant
// btrfs snapshots for the migrated workspaces and a restic snapshot for the
// ext4-resident ones. Best-effort — a failure in one backend never blocks the
// run (yolo); the note records what was actually captured.
type RunSnapshotter struct {
	Btrfs  *BtrfsSnapshotter
	Restic *ResticSnapshotter
}

// NewRunSnapshotter builds the composite snapshotter from the host layout.
func NewRunSnapshotter() *RunSnapshotter {
	return &RunSnapshotter{
		Btrfs:  NewBtrfsSnapshotter(),
		Restic: NewResticSnapshotter(),
	}
}

func (r *RunSnapshotter) Snapshot(ctx context.Context, tag string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("snapshotter not configured")
	}
	var parts []string
	if r.Btrfs != nil {
		if id, err := r.Btrfs.Snapshot(ctx, tag); err != nil {
			parts = append(parts, "btrfs:FAILED")
		} else {
			parts = append(parts, "btrfs:"+id)
		}
	}
	if r.Restic != nil {
		if id, err := r.Restic.Snapshot(ctx, tag); err != nil {
			parts = append(parts, "restic:FAILED")
		} else if id != "" {
			parts = append(parts, "restic:"+id)
		} else {
			parts = append(parts, "restic:unchanged")
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no snapshot backend available")
	}
	return strings.Join(parts, " "), nil
}
