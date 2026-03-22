package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
)

func newModelsCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Browse available AI models",
		Long:  `List available models per provider, optionally filtered. Use "models refresh" to fetch remote catalogs.`,
	}

	cmd.AddCommand(newModelsListCommand(cfg))
	cmd.AddCommand(newModelsRefreshCommand(cfg))

	return cmd
}

func newModelsListCommand(cfg *config.Config) *cobra.Command {
	var providerFlag string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show available models per provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load cached catalog if available.
			cachePath, err := ai.DefaultCachePath()
			if err != nil {
				return err
			}
			cached, err := ai.LoadCatalog(cachePath)
			if err != nil {
				// Non-fatal: just use static models.
				cached = nil
			}

			models := ai.MergedModels(cached)

			// Build alias reverse map: canonical -> []alias.
			aliases := effectiveAliases(cfg)
			reverseAliases := make(map[string][]string)
			for alias, canonical := range aliases {
				reverseAliases[canonical] = append(reverseAliases[canonical], alias)
			}
			// Sort alias lists for deterministic output.
			for _, aliasList := range reverseAliases {
				sort.Strings(aliasList)
			}

			// Determine which providers to show.
			providers := sortedProviders(models)
			if providerFlag != "" {
				providerFlag = strings.ToLower(providerFlag)
				if _, ok := models[providerFlag]; !ok {
					return fmt.Errorf("unknown provider: %s", providerFlag)
				}
				providers = []string{providerFlag}
			}

			cacheNote := ""
			if cached != nil && cached.IsFresh() {
				cacheNote = fmt.Sprintf(" (cached %s ago)", shortDurationSince(cached.FetchedAt))
			} else if cached != nil {
				cacheNote = " (cache expired — run 'models refresh')"
			}

			fmt.Printf("Available Models%s\n", cacheNote)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			for _, p := range providers {
				modelList := models[p]
				fmt.Printf("\n%s%s%s (%d models)\n", colorCyan, strings.ToUpper(p), colorReset, len(modelList))
				for _, m := range modelList {
					aliasTags := ""
					if a, ok := reverseAliases[m]; ok {
						aliasTags = fmt.Sprintf("  %s[%s]%s", colorYellow, strings.Join(a, ", "), colorReset)
					}
					fmt.Printf("  %s%s\n", m, aliasTags)
				}
			}

			// Show aliases section.
			if providerFlag == "" && len(aliases) > 0 {
				fmt.Printf("\n%sAliases%s\n", colorCyan, colorReset)
				sortedAliasKeys := make([]string, 0, len(aliases))
				for k := range aliases {
					sortedAliasKeys = append(sortedAliasKeys, k)
				}
				sort.Strings(sortedAliasKeys)
				for _, alias := range sortedAliasKeys {
					fmt.Printf("  %s → %s\n", alias, aliases[alias])
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&providerFlag, "provider", "p", "", "filter by provider name")
	return cmd
}

func newModelsRefreshCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Fetch and cache remote model catalogs",
		Long:  `Query provider APIs for their current model list and cache locally for 24 hours.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := cfg.AI.Provider
			apiKey := cfg.AI.APIKey
			baseURL := cfg.AI.BaseURL

			fmt.Printf("Refreshing model catalog for provider %q...\n", provider)

			remote, err := ai.FetchRemoteModels(cmd.Context(), apiKey, provider, baseURL)
			if err != nil {
				return fmt.Errorf("refresh failed: %w", err)
			}

			if len(remote) == 0 {
				fmt.Println("Provider does not support remote model listing. Using static catalog only.")
				return nil
			}

			cachePath, err := ai.DefaultCachePath()
			if err != nil {
				return err
			}

			// Merge with any existing cache (preserve other providers).
			existing, _ := ai.LoadCatalog(cachePath)
			cat := buildCatalog(existing, remote)

			if err := ai.SaveCatalog(cachePath, cat); err != nil {
				return fmt.Errorf("saving cache: %w", err)
			}

			for p, models := range remote {
				fmt.Printf("  %s: %d models fetched\n", p, len(models))
			}
			fmt.Printf("Cache saved to %s\n", cachePath)
			return nil
		},
	}
}

// buildCatalog merges newly fetched remote models into an existing cached catalog.
func buildCatalog(existing *ai.ModelCatalog, remote map[string][]string) *ai.ModelCatalog {
	cat := &ai.ModelCatalog{
		FetchedAt: time.Now(),
		Providers: make(map[string][]string),
	}
	// Carry forward existing providers.
	if existing != nil {
		for p, models := range existing.Providers {
			cat.Providers[p] = models
		}
	}
	// Overwrite with freshly fetched data.
	for p, models := range remote {
		cat.Providers[p] = models
	}
	return cat
}

// effectiveAliases returns the merged alias map: defaults overlaid with user config.
func effectiveAliases(cfg *config.Config) map[string]string {
	merged := make(map[string]string, len(config.DefaultModelAliases))
	for k, v := range config.DefaultModelAliases {
		merged[k] = v
	}
	for k, v := range cfg.ModelAliases {
		merged[strings.ToLower(k)] = v
	}
	return merged
}

// sortedProviders returns provider names in a stable display order.
func sortedProviders(models map[string][]string) []string {
	order := []string{"openrouter", "openai", "anthropic", "chatgpt", "droid", "custom"}
	var result []string
	seen := make(map[string]bool)
	for _, p := range order {
		if _, ok := models[p]; ok {
			result = append(result, p)
			seen[p] = true
		}
	}
	// Append any providers not in the predefined order.
	extra := make([]string, 0)
	for p := range models {
		if !seen[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	return append(result, extra...)
}

// shortDurationSince formats the elapsed time since t as a compact string.
func shortDurationSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}
