package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndLoadCatalog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	cat := &ModelCatalog{
		FetchedAt: time.Now().Truncate(time.Second),
		Providers: map[string][]string{
			"openai": {"gpt-4o", "gpt-3.5-turbo"},
		},
	}

	if err := SaveCatalog(path, cat); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}

	loaded, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadCatalog returned nil")
	}

	if len(loaded.Providers["openai"]) != 2 {
		t.Errorf("expected 2 openai models, got %d", len(loaded.Providers["openai"]))
	}
}

func TestLoadCatalog_NotExist(t *testing.T) {
	cat, err := LoadCatalog(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cat != nil {
		t.Error("expected nil catalog for non-existent file")
	}
}

func TestModelCatalog_IsFresh(t *testing.T) {
	fresh := &ModelCatalog{FetchedAt: time.Now()}
	if !fresh.IsFresh() {
		t.Error("catalog fetched just now should be fresh")
	}

	stale := &ModelCatalog{FetchedAt: time.Now().Add(-25 * time.Hour)}
	if stale.IsFresh() {
		t.Error("catalog fetched 25h ago should not be fresh")
	}
}

func TestMergedModels_NilCache(t *testing.T) {
	merged := MergedModels(nil)
	static := AvailableModels()

	if len(merged) != len(static) {
		t.Errorf("expected %d providers, got %d", len(static), len(merged))
	}
}

func TestMergedModels_WithCache(t *testing.T) {
	cached := &ModelCatalog{
		FetchedAt: time.Now(),
		Providers: map[string][]string{
			"openai": {"gpt-4o", "gpt-4o-mini", "new-model-x"},
		},
	}

	merged := MergedModels(cached)
	openaiModels := merged["openai"]

	// Should contain static models plus the new one.
	found := false
	for _, m := range openaiModels {
		if m == "new-model-x" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'new-model-x' in merged openai models")
	}

	// Static models should still be present.
	foundStatic := false
	for _, m := range openaiModels {
		if m == "gpt-4-turbo" {
			foundStatic = true
			break
		}
	}
	if !foundStatic {
		t.Error("expected static model 'gpt-4-turbo' in merged openai models")
	}
}

func TestMergedModels_NoDuplicates(t *testing.T) {
	cached := &ModelCatalog{
		FetchedAt: time.Now(),
		Providers: map[string][]string{
			"openai": {"gpt-4o", "gpt-4o-mini"},
		},
	}

	merged := MergedModels(cached)
	seen := make(map[string]bool)
	for _, m := range merged["openai"] {
		if seen[m] {
			t.Errorf("duplicate model in merged list: %s", m)
		}
		seen[m] = true
	}
}

func TestFetchRemoteModels_OpenAICompatible(t *testing.T) {
	// Spin up a fake /models endpoint.
	models := openAIModelsResponse{
		Data: []struct {
			ID string `json:"id"`
		}{
			{ID: "model-a"},
			{ID: "model-b"},
		},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models)
	}))
	defer ts.Close()

	result, err := FetchRemoteModels(context.Background(), "test-key", "custom", ts.URL)
	if err != nil {
		t.Fatalf("FetchRemoteModels: %v", err)
	}
	if len(result["custom"]) != 2 {
		t.Errorf("expected 2 models, got %d", len(result["custom"]))
	}
}

func TestFetchRemoteModels_AnthropicReturnsEmpty(t *testing.T) {
	result, err := FetchRemoteModels(context.Background(), "key", "anthropic", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Error("expected empty result for anthropic (no remote endpoint)")
	}
}

func TestFetchRemoteModels_DroidReturnsEmpty(t *testing.T) {
	result, err := FetchRemoteModels(context.Background(), "", "droid", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Error("expected empty result for droid")
	}
}

func TestDefaultCachePath(t *testing.T) {
	path, err := DefaultCachePath()
	if err != nil {
		t.Fatalf("DefaultCachePath: %v", err)
	}

	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".ok-gobot", "model-cache.json")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}
