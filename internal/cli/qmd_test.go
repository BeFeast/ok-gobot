package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/config"
	"ok-gobot/internal/memory"
)

func TestQMDStatusSkippedWhenConfigDisabled(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)
	cfg.Memory.Enabled = false
	cfg.Memory.Backend = "builtin"
	cfg.Memory.QMD.BinaryPath = "definitely-missing-qmd"

	output, err := executeQMDTestCommand(cfg, "status")
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	for _, want := range []string{"QMD: skipped", "memory.enabled=false", "Fallback: not needed"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output: %q", want, output)
		}
	}
}

func TestQMDStatusReportsMissingBinary(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)
	cfg.Memory.Enabled = true
	cfg.Memory.Backend = "qmd"
	cfg.Memory.QMD.BinaryPath = "definitely-missing-qmd"

	output, err := executeQMDTestCommand(cfg, "status")
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	for _, want := range []string{"QMD: unavailable", "qmd binary not found", "Fallback: builtin"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output: %q", want, output)
		}
	}
}

func TestQMDStatusReportsUnreadableIndex(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)
	cfg.Memory.Enabled = true
	cfg.Memory.Backend = "qmd"
	cfg.Memory.QMD.BinaryPath = writeCLIqmdTestBinary(t, `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' 'qmd 2.1.0'
  exit 0
fi
printf '%s\n' '[]'
`)
	cfg.Memory.QMD.IndexPath = filepath.Join(t.TempDir(), "qmd.sqlite")
	if err := os.WriteFile(cfg.Memory.QMD.IndexPath, []byte("not sqlite"), 0o644); err != nil {
		t.Fatalf("write unreadable index: %v", err)
	}

	output, err := executeQMDTestCommand(cfg, "status")
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	for _, want := range []string{"QMD: unavailable", "index unreadable", "Fallback: builtin"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output: %q", want, output)
		}
	}
}

func TestQMDSmokeFallsBackWhenServerUnhealthy(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)
	cfg.Memory.Enabled = true
	cfg.Memory.Backend = "qmd"
	cfg.Memory.QMD.BinaryPath = writeCLIqmdTestBinary(t, `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' 'qmd 2.1.0'
  exit 0
fi
printf '%s\n' 'server unavailable' >&2
exit 1
`)
	cfg.SoulPath = t.TempDir()
	writeCLITestFile(t, filepath.Join(cfg.SoulPath, "MEMORY.md"), "# Memory\n\nphoenix fallback memory.")
	memStore, err := memory.NewMemoryStore(store.DB())
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}
	if _, _, _, err := runMemoryIndex(context.Background(), cfg, memStore, nil, true, memoryIndexScope{Managed: true, Extra: true}); err != nil {
		t.Fatalf("runMemoryIndex failed: %v", err)
	}

	output, err := executeQMDTestCommand(cfg, "smoke", "phoenix")
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	for _, want := range []string{"QMD: unavailable", "server unavailable", "Fallback: builtin used", "MEMORY.md"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output: %q", want, output)
		}
	}
}

func TestQMDUpdateRunsExplicitLifecycleCommand(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)
	cfg.Memory.Enabled = true
	cfg.Memory.Backend = "qmd"
	cfg.Memory.QMD.Index = "work"
	argsPath := filepath.Join(t.TempDir(), "qmd-args.txt")
	cfg.Memory.QMD.BinaryPath = writeCLIqmdTestBinary(t, `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' 'qmd 2.1.0'
  exit 0
fi
printf '%s\n' "$@" > "`+argsPath+`"
printf '%s\n' 'updated ok'
`)

	output, err := executeQMDTestCommand(cfg, "update")
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !strings.Contains(output, "QMD update: used") || !strings.Contains(output, "updated ok") {
		t.Fatalf("unexpected output: %q", output)
	}
	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(argsBytes)), "\n")
	want := []string{"--index", "work", "update"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args=%q, want %q", args, want)
	}
}

func executeQMDTestCommand(cfg *config.Config, args ...string) (string, error) {
	cmd := newQMDCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func writeCLIqmdTestBinary(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qmd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake qmd: %v", err)
	}
	return path
}
