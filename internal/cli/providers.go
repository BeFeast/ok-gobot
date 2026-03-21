package cli

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
)

type providerInfo struct {
	name    string
	status  string // "ok", "error", "no-key"
	message string
	active  bool
}

func newProvidersCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List configured AI providers and their status",
		Long:  `Show all known AI providers with connection status (ok / error / no-key).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			providers := checkProviders(cfg)

			fmt.Println("AI Providers")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			for _, p := range providers {
				var color, symbol string
				switch p.status {
				case "ok":
					color = colorGreen
					symbol = "✓"
				case "error":
					color = colorRed
					symbol = "✗"
				case "no-key":
					color = colorYellow
					symbol = "–"
				}

				activeMarker := ""
				if p.active {
					activeMarker = " (active)"
				}

				fmt.Printf("%s%s%s %-12s  %s%s%s",
					color, symbol, colorReset,
					p.name,
					color, p.status, colorReset)
				if activeMarker != "" {
					fmt.Printf("%s%s%s", colorCyan, activeMarker, colorReset)
				}
				fmt.Println()
				if p.message != "" {
					fmt.Printf("  %s\n", p.message)
				}
			}

			fmt.Println()
			fmt.Println("Run 'ok-gobot models list' to see available models.")
			return nil
		},
	}
}

func checkProviders(cfg *config.Config) []providerInfo {
	knownProviders := []struct {
		name       string
		defaultURL string
	}{
		{"openrouter", "https://openrouter.ai"},
		{"openai", "https://api.openai.com"},
		{"anthropic", "https://api.anthropic.com"},
		{"chatgpt", "https://chatgpt.com"},
		{"droid", ""},
	}

	var results []providerInfo
	for _, kp := range knownProviders {
		info := providerInfo{
			name:   kp.name,
			active: cfg.AI.Provider == kp.name,
		}

		// Check if this provider has credentials configured
		hasKey := false
		if cfg.AI.Provider == kp.name && cfg.AI.APIKey != "" {
			hasKey = true
		}
		// Anthropic OAuth is also valid
		if kp.name == "anthropic" {
			if creds, err := ai.LoadAnthropicOAuthCredentials(""); err == nil && creds != nil {
				hasKey = true
			}
		}
		// Droid doesn't need an API key
		if kp.name == "droid" {
			hasKey = true
		}

		if !hasKey && cfg.AI.Provider != kp.name {
			info.status = "no-key"
			info.message = "No API key configured"
			results = append(results, info)
			continue
		}

		if !hasKey {
			info.status = "no-key"
			info.message = "Active provider but no API key set"
			results = append(results, info)
			continue
		}

		// Try to reach the provider endpoint
		baseURL := kp.defaultURL
		if cfg.AI.Provider == kp.name && cfg.AI.BaseURL != "" {
			baseURL = cfg.AI.BaseURL
		}

		if baseURL == "" {
			// No URL to check (e.g. droid is a local binary)
			info.status = "ok"
			info.message = "Local provider"
			results = append(results, info)
			continue
		}

		client := &http.Client{Timeout: 5 * time.Second}
		req, err := http.NewRequest("HEAD", baseURL, nil)
		if err != nil {
			info.status = "error"
			info.message = fmt.Sprintf("Invalid URL: %v", err)
			results = append(results, info)
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			info.status = "error"
			info.message = fmt.Sprintf("Unreachable: %v", err)
			results = append(results, info)
			continue
		}
		resp.Body.Close()

		info.status = "ok"
		info.message = fmt.Sprintf("Reachable: %s", baseURL)
		results = append(results, info)
	}

	return results
}
