package cli

import (
	"testing"
	"time"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
)

func TestEffectiveAliases_Defaults(t *testing.T) {
	cfg := &config.Config{}
	aliases := effectiveAliases(cfg)
	if aliases["sonnet"] != config.DefaultModelAliases["sonnet"] {
		t.Error("expected default sonnet alias")
	}
}

func TestEffectiveAliases_Override(t *testing.T) {
	cfg := &config.Config{
		ModelAliases: map[string]string{
			"sonnet": "my-custom-model",
		},
	}
	aliases := effectiveAliases(cfg)
	if aliases["sonnet"] != "my-custom-model" {
		t.Errorf("expected override, got %s", aliases["sonnet"])
	}
}

func TestEffectiveAliases_CaseInsensitive(t *testing.T) {
	cfg := &config.Config{
		ModelAliases: map[string]string{
			"MyModel": "some-model",
		},
	}
	aliases := effectiveAliases(cfg)
	if aliases["mymodel"] != "some-model" {
		t.Error("expected case-insensitive alias key")
	}
}

func TestSortedProviders(t *testing.T) {
	models := map[string][]string{
		"droid":      {"glm-5"},
		"openai":     {"gpt-4o"},
		"openrouter": {"kimi"},
		"zebra":      {"z-model"},
	}

	result := sortedProviders(models)

	if result[0] != "openrouter" {
		t.Errorf("expected openrouter first, got %s", result[0])
	}
	if result[1] != "openai" {
		t.Errorf("expected openai second, got %s", result[1])
	}
	// droid should come after the predefined order entries
	// "zebra" should be appended at the end
	if result[len(result)-1] != "zebra" {
		t.Errorf("expected zebra last, got %s", result[len(result)-1])
	}
}

func TestBuildCatalog_MergesExisting(t *testing.T) {
	existing := &ai.ModelCatalog{
		FetchedAt: time.Now().Add(-1 * time.Hour),
		Providers: map[string][]string{
			"openai": {"old-model"},
		},
	}

	remote := map[string][]string{
		"openrouter": {"new-model"},
	}

	cat := buildCatalog(existing, remote)

	if len(cat.Providers["openai"]) != 1 {
		t.Error("expected existing openai models to be preserved")
	}
	if len(cat.Providers["openrouter"]) != 1 {
		t.Error("expected remote openrouter models to be added")
	}
}

func TestBuildCatalog_OverwritesProvider(t *testing.T) {
	existing := &ai.ModelCatalog{
		FetchedAt: time.Now().Add(-1 * time.Hour),
		Providers: map[string][]string{
			"openrouter": {"old-model"},
		},
	}

	remote := map[string][]string{
		"openrouter": {"new-model-a", "new-model-b"},
	}

	cat := buildCatalog(existing, remote)

	if len(cat.Providers["openrouter"]) != 2 {
		t.Errorf("expected 2 openrouter models, got %d", len(cat.Providers["openrouter"]))
	}
}

func TestBuildCatalog_NilExisting(t *testing.T) {
	remote := map[string][]string{
		"openai": {"model-x"},
	}
	cat := buildCatalog(nil, remote)
	if cat.Providers["openai"][0] != "model-x" {
		t.Error("expected model-x")
	}
}

func TestShortDurationSince(t *testing.T) {
	now := time.Now()

	tests := []struct {
		t    time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "30s"},
		{now.Add(-5 * time.Minute), "5m"},
		{now.Add(-3 * time.Hour), "3h"},
	}

	for _, tt := range tests {
		got := shortDurationSince(tt.t)
		if got != tt.want {
			t.Errorf("shortDurationSince(%v ago) = %q, want %q", time.Since(tt.t), got, tt.want)
		}
	}
}
