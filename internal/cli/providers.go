package cli

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
)

// knownProviders is the ordered list of providers we report on.
var knownProviders = []struct {
	name string
	desc string
}{
	{"openrouter", "OpenRouter aggregator"},
	{"openai", "OpenAI API"},
	{"anthropic", "Anthropic Messages API"},
	{"chatgpt", "ChatGPT Codex Responses API"},
	{"droid", "factory.ai droid subprocess"},
	{"custom", "OpenAI-compatible custom endpoint"},
}

func newProvidersCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List configured AI providers and their status",
		Long:  `Show all known AI providers, which one is active, and whether credentials are configured.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			active := cfg.AI.Provider

			fmt.Println("AI Providers")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			for _, p := range knownProviders {
				marker := "  "
				if p.name == active {
					marker = "> "
				}
				status := providerStatus(cfg, p.name)
				statusColor := statusColorCode(status)
				fmt.Printf("%s%-12s %s%-8s%s  %s\n", marker, p.name, statusColor, status, colorReset, p.desc)
			}

			fmt.Println()
			fmt.Printf("Active provider: %s (model: %s)\n", active, cfg.AI.Model)
			return nil
		},
	}
}

// providerStatus determines the health status for a provider.
func providerStatus(cfg *config.Config, provider string) string {
	isActive := cfg.AI.Provider == provider

	switch provider {
	case "droid":
		// Droid doesn't require an API key.
		if isActive {
			return "ok"
		}
		return "ready"
	case "anthropic":
		if isActive {
			if cfg.AI.APIKey != "" {
				return "ok"
			}
			// Check OAuth credentials.
			if creds, err := ai.LoadAnthropicOAuthCredentials(""); err == nil && creds != nil {
				return "ok"
			}
			return "no-key"
		}
		// Not active — check if OAuth creds exist anyway.
		if creds, err := ai.LoadAnthropicOAuthCredentials(""); err == nil && creds != nil {
			return "ready"
		}
		return "no-key"
	case "custom":
		if isActive {
			if cfg.AI.APIKey != "" && cfg.AI.BaseURL != "" {
				return "ok"
			}
			if cfg.AI.BaseURL == "" {
				return "no-url"
			}
			return "no-key"
		}
		return "-"
	default:
		// openrouter, openai, chatgpt
		if isActive {
			if cfg.AI.APIKey != "" {
				return "ok"
			}
			return "no-key"
		}
		return "-"
	}
}

// statusColorCode returns an ANSI color code for a provider status string.
func statusColorCode(status string) string {
	switch status {
	case "ok", "ready":
		return colorGreen
	case "no-key", "no-url", "error":
		return colorRed
	default:
		return colorYellow
	}
}

// CheckProviderReachable does a quick HEAD request to the provider's base URL.
// Not called by default (too slow for a list view) but available for doctor-style checks.
func CheckProviderReachable(provider, baseURL string) error {
	url := baseURL
	if url == "" {
		switch provider {
		case "openrouter":
			url = "https://openrouter.ai"
		case "openai":
			url = "https://api.openai.com"
		case "anthropic":
			url = "https://api.anthropic.com"
		default:
			return nil // skip unknown
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", url, err)
	}
	resp.Body.Close()
	return nil
}
