package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
)

func newAuthCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage provider authentication",
	}

	cmd.AddCommand(newAuthAnthropicCommand(cfg))
	return cmd
}

func newAuthAnthropicCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "anthropic",
		Short: "Manage Anthropic OAuth authentication",
	}

	cmd.AddCommand(newAuthAnthropicLoginCommand(cfg))
	cmd.AddCommand(newAuthAnthropicStatusCommand(cfg))
	cmd.AddCommand(newAuthAnthropicLogoutCommand(cfg))
	return cmd
}

func newAuthAnthropicLoginCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate Anthropic via OAuth (Claude MAX)",
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := ai.NewAnthropicOAuthAuthRequest()
			if err != nil {
				return err
			}

			fmt.Println("Open this URL in your browser to authenticate with Anthropic:")
			fmt.Println(req.URL)
			fmt.Println()
			if err := openBrowser(req.URL); err != nil {
				fmt.Printf("Could not open browser automatically: %v\n", err)
			}
			fmt.Println("After approving access, copy the code from Anthropic and paste it below.")
			fmt.Print("> OAuth code: ")

			reader := bufio.NewReader(os.Stdin)
			rawCode, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("failed to read OAuth code: %w", err)
			}
			code := ai.ExtractAnthropicOAuthCode(rawCode)
			if code == "" {
				return fmt.Errorf("OAuth code is empty")
			}

			creds, err := ai.ExchangeAnthropicOAuthCode(cmd.Context(), code, req.Verifier, req.State)
			if err != nil {
				return err
			}

			storePath, err := ai.DefaultAnthropicOAuthStorePath()
			if err != nil {
				return err
			}
			if err := ai.SaveAnthropicOAuthCredentials(storePath, creds); err != nil {
				return err
			}

			if cfg.ConfigPath == "" {
				configPath, err := defaultConfigPath()
				if err != nil {
					return fmt.Errorf("failed to resolve config path: %w", err)
				}
				if _, err := ensureDefaultConfig(configPath, cfg.GetSoulPath()); err != nil {
					return fmt.Errorf("failed to initialize config: %w", err)
				}
				cfg.ConfigPath = configPath
			}

			cfg.AI.Provider = "anthropic"
			cfg.AI.APIKey = "oauth:" + creds.AccessToken
			if strings.TrimSpace(cfg.AI.Model) == "" {
				cfg.AI.Model = "claude-sonnet-4-5-20250929"
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Printf("✅ Anthropic OAuth configured\n")
			fmt.Printf("Credentials store: %s\n", storePath)
			fmt.Println("Provider set to anthropic in your config.")
			return nil
		},
	}
}

func newAuthAnthropicStatusCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Anthropic OAuth credential status",
		RunE: func(cmd *cobra.Command, args []string) error {
			storePath, err := ai.DefaultAnthropicOAuthStorePath()
			if err != nil {
				return err
			}
			creds, err := ai.LoadAnthropicOAuthCredentials(storePath)
			if err != nil {
				fmt.Printf("Anthropic OAuth: not configured (%v)\n", err)
				fmt.Printf("Store path: %s\n", storePath)
				return nil
			}

			now := time.Now()
			status := "valid"
			switch {
			case creds.IsExpired(now):
				status = "expired"
			case creds.IsExpiringSoon(now, 10*time.Minute):
				status = "expiring_soon"
			}

			expiresAt := "not set"
			if creds.ExpiresAt > 0 {
				expiresAt = time.UnixMilli(creds.ExpiresAt).Format(time.RFC3339)
			}

			fmt.Printf("Anthropic OAuth status: %s\n", status)
			fmt.Printf("Store path: %s\n", storePath)
			fmt.Printf("Expires at: %s\n", expiresAt)
			fmt.Printf("Refresh token: %v\n", strings.TrimSpace(creds.RefreshToken) != "")
			fmt.Printf("Configured provider: %s\n", cfg.AI.Provider)
			return nil
		},
	}
}

func newAuthAnthropicLogoutCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove saved Anthropic OAuth credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			storePath, err := ai.DefaultAnthropicOAuthStorePath()
			if err != nil {
				return err
			}
			if err := ai.DeleteAnthropicOAuthCredentials(storePath); err != nil {
				return err
			}

			if cfg.AI.Provider == "anthropic" && strings.HasPrefix(strings.TrimSpace(cfg.AI.APIKey), "oauth:") {
				cfg.AI.APIKey = ""
				if cfg.ConfigPath != "" {
					if err := cfg.Save(); err != nil {
						return fmt.Errorf("removed credentials but failed to save config: %w", err)
					}
				}
			}

			fmt.Printf("✅ Removed Anthropic OAuth credentials (%s)\n", storePath)
			return nil
		},
	}
}

func openBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Start()
	case "linux":
		return exec.Command("xdg-open", rawURL).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
