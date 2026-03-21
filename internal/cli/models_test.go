package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ok-gobot/internal/config"
)

func TestMergeModels_AppendsNewFromCache(t *testing.T) {
	t.Parallel()

	static := map[string][]string{
		"openai": {"gpt-4o", "gpt-4o-mini"},
	}
	cached := &modelCatalog{
		FetchedAt: time.Now(),
		Providers: map[string][]string{
			"openai": {"gpt-4o", "gpt-3.5-turbo"},
			"custom": {"my-model"},
		},
	}

	merged := mergeModels(static, cached)

	// gpt-4o should not be duplicated.
	if got := len(merged["openai"]); got != 3 {
		t.Fatalf("expected 3 openai models, got %d: %v", got, merged["openai"])
	}

	// custom provider should appear.
	if _, ok := merged["custom"]; !ok {
		t.Fatal("expected custom provider from cache")
	}
}

func TestBuildReverseAliases_IncludesDefaultAndUser(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		ModelAliases: map[string]string{
			"my-sonnet": "claude-sonnet-4-5-20250929",
		},
	}

	rev := buildReverseAliases(cfg)

	aliases := rev["claude-sonnet-4-5-20250929"]
	if len(aliases) < 2 {
		t.Fatalf("expected at least 2 aliases for sonnet, got %v", aliases)
	}

	found := false
	for _, a := range aliases {
		if a == "my-sonnet" {
			found = true
		}
	}
	if !found {
		t.Fatalf("user alias 'my-sonnet' not found in %v", aliases)
	}
}

func TestCatalogCache_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "model-cache.json")

	cat := &modelCatalog{
		FetchedAt: time.Now(),
		Providers: map[string][]string{
			"openai": {"gpt-4o"},
		},
	}

	data, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Read it back and verify.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var loaded modelCatalog
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}

	if len(loaded.Providers["openai"]) != 1 || loaded.Providers["openai"][0] != "gpt-4o" {
		t.Fatalf("unexpected loaded catalog: %+v", loaded)
	}
}

func TestModelsListCommand_NoError(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		AI: config.AIConfig{
			Provider: "openrouter",
			Model:    "moonshotai/kimi-k2.5",
		},
	}

	cmd := newModelsListCommand(cfg)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("models list failed: %v", err)
	}
}

func TestProvidersCommand_NoError(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		AI: config.AIConfig{
			Provider: "openrouter",
		},
	}

	cmd := newProvidersCommand(cfg)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("providers command failed: %v", err)
	}
}
