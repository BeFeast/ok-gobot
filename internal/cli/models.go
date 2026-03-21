package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
)

// modelCatalog is the on-disk cache for remotely fetched model lists.
type modelCatalog struct {
	FetchedAt time.Time           `json:"fetched_at"`
	Providers map[string][]string `json:"providers"`
}

const catalogCacheTTL = 24 * time.Hour

func newModelsCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Browse available AI models",
		Long:  `List available models by provider, with alias information. Use 'models refresh' to fetch remote catalogs.`,
	}

	listCmd := newModelsListCommand(cfg)
	refreshCmd := newModelsRefreshCommand(cfg)

	cmd.AddCommand(listCmd)
	cmd.AddCommand(refreshCmd)

	// Default to list when no subcommand given.
	cmd.RunE = listCmd.RunE

	return cmd
}

func newModelsListCommand(cfg *config.Config) *cobra.Command {
	var providerFlag string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show available models per provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			staticModels := ai.AvailableModels()
			cached := loadCatalogCache()

			// Merge cached models into static list.
			merged := mergeModels(staticModels, cached)

			// Build reverse alias map: canonical -> []alias.
			reverseAliases := buildReverseAliases(cfg)

			providers := sortedKeys(merged)
			if providerFlag != "" {
				if _, ok := merged[providerFlag]; !ok {
					return fmt.Errorf("unknown provider: %s", providerFlag)
				}
				providers = []string{providerFlag}
			}

			for i, prov := range providers {
				if i > 0 {
					fmt.Println()
				}
				activeMarker := ""
				if cfg.AI.Provider == prov {
					activeMarker = fmt.Sprintf(" %s(active)%s", colorCyan, colorReset)
				}
				fmt.Printf("%s%s%s%s\n", colorGreen, prov, colorReset, activeMarker)

				models := merged[prov]
				for _, m := range models {
					aliases := reverseAliases[m]
					currentMarker := ""
					if cfg.AI.Provider == prov && cfg.AI.Model == m {
						currentMarker = " *"
					}
					if len(aliases) > 0 {
						fmt.Printf("  %-45s  (%s)%s\n", m, strings.Join(aliases, ", "), currentMarker)
					} else {
						fmt.Printf("  %s%s\n", m, currentMarker)
					}
				}
			}

			if cached != nil {
				age := time.Since(cached.FetchedAt).Truncate(time.Minute)
				fmt.Printf("\nCached catalog age: %s (refresh with 'ok-gobot models refresh')\n", age)
			} else {
				fmt.Println("\nShowing static models only. Run 'ok-gobot models refresh' to fetch remote catalogs.")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&providerFlag, "provider", "", "Filter models by provider name")
	return cmd
}

func newModelsRefreshCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Fetch and cache remote model catalogs",
		Long:  `Queries provider APIs for their model lists and caches results locally for 24 hours.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Fetching model catalogs...")

			catalog := &modelCatalog{
				FetchedAt: time.Now(),
				Providers: make(map[string][]string),
			}

			// Fetch from providers that expose model list endpoints.
			fetchers := []struct {
				name string
				fn   func(*config.Config) ([]string, error)
			}{
				{"openrouter", fetchOpenRouterModels},
				{"openai", fetchOpenAIModels},
			}

			for _, f := range fetchers {
				fmt.Printf("  %-12s ", f.name)
				models, err := f.fn(cfg)
				if err != nil {
					fmt.Printf("%s%s%s\n", colorYellow, err, colorReset)
					continue
				}
				catalog.Providers[f.name] = models
				fmt.Printf("%s%d models%s\n", colorGreen, len(models), colorReset)
			}

			if err := saveCatalogCache(catalog); err != nil {
				return fmt.Errorf("failed to save catalog cache: %w", err)
			}

			fmt.Println("\nCatalog cached. Run 'ok-gobot models list' to browse.")
			return nil
		},
	}
}

// --- remote fetchers ---

type openRouterModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func fetchOpenRouterModels(cfg *config.Config) ([]string, error) {
	url := "https://openrouter.ai/api/v1/models"

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("request build: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	sort.Strings(models)
	return models, nil
}

type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func fetchOpenAIModels(cfg *config.Config) ([]string, error) {
	apiKey := ""
	if cfg.AI.Provider == "openai" {
		apiKey = cfg.AI.APIKey
	}
	if apiKey == "" {
		return nil, fmt.Errorf("no API key for openai")
	}

	url := "https://api.openai.com/v1/models"
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("request build: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result openAIModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	sort.Strings(models)
	return models, nil
}

// --- cache helpers ---

func catalogCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ok-gobot", "model-cache.json")
}

func loadCatalogCache() *modelCatalog {
	path := catalogCachePath()
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var cat modelCatalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return nil
	}

	if time.Since(cat.FetchedAt) > catalogCacheTTL {
		return nil // expired
	}

	return &cat
}

func saveCatalogCache(cat *modelCatalog) error {
	path := catalogCachePath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o644)
}

// --- merge & alias helpers ---

func mergeModels(static map[string][]string, cached *modelCatalog) map[string][]string {
	merged := make(map[string][]string)
	for k, v := range static {
		merged[k] = v
	}

	if cached == nil {
		return merged
	}

	for prov, models := range cached.Providers {
		if _, ok := merged[prov]; !ok {
			merged[prov] = models
			continue
		}
		// Merge: cached models supplement static ones (keep static order, append new).
		existing := make(map[string]bool)
		for _, m := range merged[prov] {
			existing[m] = true
		}
		for _, m := range models {
			if !existing[m] {
				merged[prov] = append(merged[prov], m)
			}
		}
	}

	return merged
}

func buildReverseAliases(cfg *config.Config) map[string][]string {
	rev := make(map[string][]string)

	// Built-in aliases.
	for alias, canonical := range config.DefaultModelAliases {
		rev[canonical] = append(rev[canonical], alias)
	}

	// User-configured aliases.
	for alias, canonical := range cfg.ModelAliases {
		rev[canonical] = append(rev[canonical], alias)
	}

	// Sort each alias list for stable output.
	for k := range rev {
		sort.Strings(rev[k])
	}

	return rev
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
