package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func countChunksGlob(t *testing.T, store *storage.Store, glob string) int {
	t.Helper()
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM memory_chunks WHERE source_file GLOB ?`, glob).Scan(&count); err != nil {
		t.Fatalf("count chunks failed: %v", err)
	}
	return count
}

func newIndexScopeConfig(t *testing.T) (*storage.Store, *config.Config) {
	t.Helper()
	store, cfg := newTestStore(t)
	cfg.Memory.Enabled = true
	cfg.SoulPath = t.TempDir()
	writeCLITestFile(t, filepath.Join(cfg.SoulPath, "MEMORY.md"), "# Memory\n\nDurable fact.")
	vault := t.TempDir()
	writeCLITestFile(t, filepath.Join(vault, "note.md"), "# Note\n\nVault fact.")
	cfg.Memory.ExtraPaths = []config.MemoryExtraPathConfig{{Name: "vault", Path: vault}}
	return store, cfg
}

func runMemoryIndexCommand(t *testing.T, cfg *config.Config, args ...string) (string, error) {
	t.Helper()
	cmd := newMemoryCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"index"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestMemoryIndexScopeFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		wantManaged int
		wantExtra   int
		wantOutput  []string
	}{
		{name: "default indexes managed only", args: nil, wantManaged: 1, wantExtra: 0, wantOutput: []string{"Indexed 1 managed memory source file(s)"}},
		{name: "--extra indexes extra paths only", args: []string{"--extra"}, wantManaged: 0, wantExtra: 1, wantOutput: []string{"Indexed 1 extra source file(s)"}},
		{name: "--all indexes both", args: []string{"--all"}, wantManaged: 1, wantExtra: 1, wantOutput: []string{"Indexed 1 managed memory source file(s)", "Indexed 1 extra source file(s)"}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, cfg := newIndexScopeConfig(t)
			output, err := runMemoryIndexCommand(t, cfg, tc.args...)
			if err != nil {
				t.Fatalf("Execute error = %v (output %q)", err, output)
			}
			for _, want := range tc.wantOutput {
				if !strings.Contains(output, want) {
					t.Fatalf("expected %q in output %q", want, output)
				}
			}
			if got := countChunksGlob(t, store, "MEMORY.md"); got != tc.wantManaged {
				t.Fatalf("managed chunks = %d, want %d", got, tc.wantManaged)
			}
			if got := countChunksGlob(t, store, "extra:*"); got != tc.wantExtra {
				t.Fatalf("extra chunks = %d, want %d", got, tc.wantExtra)
			}
		})
	}
}

func TestMemoryIndexForceClearsOnlySelectedScope(t *testing.T) {
	t.Parallel()

	store, cfg := newIndexScopeConfig(t)
	if _, err := runMemoryIndexCommand(t, cfg, "--all"); err != nil {
		t.Fatalf("initial --all failed: %v", err)
	}
	// Remove the vault note: --force --extra must drop its stale chunk while
	// leaving the managed chunk untouched.
	if err := os.Remove(filepath.Join(cfg.Memory.ExtraPaths[0].Path, "note.md")); err != nil {
		t.Fatalf("remove note: %v", err)
	}
	if _, err := runMemoryIndexCommand(t, cfg, "--force", "--extra"); err != nil {
		t.Fatalf("--force --extra failed: %v", err)
	}
	if got := countChunksGlob(t, store, "extra:*"); got != 0 {
		t.Fatalf("extra chunks after --force --extra = %d, want 0", got)
	}
	if got := countChunksGlob(t, store, "MEMORY.md"); got != 1 {
		t.Fatalf("managed chunks after --force --extra = %d, want 1 (untouched)", got)
	}
}

func TestMemoryIndexRefusesAIKeyFallbackForThirdPartyEndpoint(t *testing.T) {
	t.Parallel()

	_, cfg := newIndexScopeConfig(t)
	cfg.Memory.EmbeddingsBaseURL = "https://api.cloudflare.com/client/v4/accounts/abc/ai/v1"
	cfg.Memory.EmbeddingsAPIKey = ""
	cfg.AI.APIKey = "pool-secret"

	for _, sub := range [][]string{{"index"}, {"search", "anything"}, {"pack", "anything"}} {
		cmd := newMemoryCommand(cfg)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(sub)
		err := cmd.Execute()
		if err == nil {
			t.Fatalf("%v: expected refusal without memory.embeddings_api_key", sub)
		}
		if !strings.Contains(err.Error(), "OKGOBOT_MEMORY_EMBEDDINGS_API_KEY") {
			t.Fatalf("%v: error should point at the memory key: %v", sub, err)
		}
		if strings.Contains(err.Error(), "pool-secret") || strings.Contains(out.String(), "pool-secret") {
			t.Fatalf("%v: the ai key leaked into the output", sub)
		}
	}
}
