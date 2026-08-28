package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"ok-gobot/internal/browser"
	"ok-gobot/internal/config"
)

type remoteBrowserCheckFunc func(context.Context, *config.Config) (browser.RemoteCheckResult, error)

func newBrowserCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "browser",
		Short: "Manage Chrome browser for automation",
		Long:  `Setup and control Chrome browser for web automation tasks.`,
	}

	cmd.AddCommand(newBrowserSetupCommand())
	cmd.AddCommand(newBrowserStatusCommand(cfg))

	return cmd
}

func newBrowserSetupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Setup Chrome browser profile",
		Long: `Setup Chrome browser for automation.

This will:
1. Check if Chrome is installed
2. Create a dedicated profile directory
3. Guide you through initial Chrome setup

The browser profile will be stored in ~/.ok-gobot/chrome-profile/
and will preserve your history, logins, and extensions.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("🌐 Chrome Browser Setup")
			fmt.Println("========================")

			// Check if Chrome is installed
			manager := browser.NewManager("")
			if !manager.IsChromeInstalled() {
				fmt.Println("❌ Chrome not found!")
				fmt.Println("\nPlease install Google Chrome:")
				fmt.Println("  macOS: brew install --cask google-chrome")
				fmt.Println("  Linux: sudo apt install google-chrome-stable")
				fmt.Println("  Or download from: https://www.google.com/chrome/")
				return fmt.Errorf("chrome not installed")
			}

			fmt.Println("✅ Chrome found")

			// Get profile path
			homeDir, _ := os.UserHomeDir()
			profilePath := filepath.Join(homeDir, ".ok-gobot", "chrome-profile")

			// Check existing profile
			info, err := manager.GetProfileInfo()
			if err != nil {
				return fmt.Errorf("failed to check profile: %w", err)
			}

			if info.Exists {
				fmt.Printf("\n📁 Existing profile found at:\n   %s\n", profilePath)
				if info.History {
					fmt.Println("   ✓ History preserved")
				}
				if info.Extensions > 0 {
					fmt.Printf("   ✓ %d extensions installed\n", info.Extensions)
				}
				fmt.Println("\n✅ Browser is ready to use!")
			} else {
				fmt.Printf("\n📁 Creating new profile at:\n   %s\n", profilePath)

				if err := os.MkdirAll(profilePath, 0755); err != nil {
					return fmt.Errorf("failed to create profile: %w", err)
				}

				fmt.Println("\n✅ Profile created!")
				fmt.Println("\n🚀 Next steps:")
				fmt.Println("1. Start the bot: ok-gobot start")
				fmt.Println("2. In Telegram, send: /browser start")
				fmt.Println("3. Chrome will open - sign in to your accounts")
				fmt.Println("4. Install any extensions you need")
				fmt.Println("5. Use /browser commands to automate tasks")
			}

			fmt.Println("\n💡 Tips:")
			fmt.Println("- Your Chrome profile is isolated from your main browser")
			fmt.Println("- All history, cookies, and extensions are preserved")
			fmt.Println("- You can manually open this Chrome profile anytime")

			return nil
		},
	}
}

func newBrowserStatusCommand(cfg *config.Config) *cobra.Command {
	return newBrowserStatusCommandWithChecker(cfg, runRemoteBrowserCheck)
}

func newBrowserStatusCommandWithChecker(cfg *config.Config, checkRemote remoteBrowserCheckFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check Chrome browser status",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if configuredRemoteBrowserEndpoint(cfg) == "" {
				writeLocalBrowserStatus(out)
				return nil
			}

			result, err := checkRemote(cmd.Context(), cfg)
			writeRemoteBrowserStatus(out, cfg, result, err)
			if err != nil {
				return fmt.Errorf("remote CDP status check failed: %w", err)
			}
			return nil
		},
	}
}

func runRemoteBrowserCheck(ctx context.Context, cfg *config.Config) (browser.RemoteCheckResult, error) {
	profilePath := ""
	if cfg != nil {
		profilePath = cfg.Browser.ProfilePath
	}
	manager := browser.NewManager(profilePath)
	if cfg != nil {
		manager.ChromePath = cfg.Browser.ChromePath
		manager.RemoteDebugURL = configuredRemoteBrowserEndpoint(cfg)
	}
	return manager.CheckRemote(ctx)
}

func configuredRemoteBrowserEndpoint(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Browser.DebugURL)
}

func writeLocalBrowserStatus(out io.Writer) {
	manager := browser.NewManager("")

	fmt.Fprintln(out, "🌐 Chrome Browser Status")
	fmt.Fprintln(out, "========================")

	if manager.IsChromeInstalled() {
		fmt.Fprintln(out, "✅ Chrome installed")

		info, _ := manager.GetProfileInfo()
		if info.Exists {
			fmt.Fprintf(out, "✅ Profile ready\n   Path: %s\n", info.Path)
			if info.History {
				fmt.Fprintln(out, "   ✓ History available")
			}
			if info.Extensions > 0 {
				fmt.Fprintf(out, "   ✓ %d extensions\n", info.Extensions)
			}
		} else {
			fmt.Fprintln(out, "⚠️  Profile not configured")
			fmt.Fprintln(out, "   Run: ok-gobot browser setup")
		}
	} else {
		fmt.Fprintln(out, "❌ Chrome not installed")
	}
}

func writeRemoteBrowserStatus(out io.Writer, cfg *config.Config, result browser.RemoteCheckResult, checkErr error) {
	endpoint := result.Endpoint
	if endpoint == "" {
		endpoint = configuredRemoteBrowserEndpoint(cfg)
	}

	fmt.Fprintln(out, "🌐 Remote Browser CDP Status")
	fmt.Fprintln(out, "============================")
	fmt.Fprintf(out, "Endpoint: %s\n", endpoint)

	failedStage, hasFailedStage := remoteBrowserFailureStage(checkErr)
	for _, stage := range []browser.RemoteCheckStage{
		browser.RemoteCheckDiscovery,
		browser.RemoteCheckWebSocket,
		browser.RemoteCheckTarget,
		browser.RemoteCheckEvaluation,
		browser.RemoteCheckCleanup,
	} {
		label := remoteBrowserStageLabel(stage)
		switch {
		case result.Passed(stage):
			fmt.Fprintf(out, "✅ %s\n", label)
		case hasFailedStage && failedStage == stage:
			fmt.Fprintf(out, "❌ %s\n", label)
		case checkErr != nil:
			fmt.Fprintf(out, "➖ %s (not reached)\n", label)
		default:
			fmt.Fprintf(out, "❌ %s\n", label)
		}
	}

	if checkErr != nil {
		fmt.Fprintf(out, "Failure: %v\n", checkErr)
		return
	}
	fmt.Fprintf(out, "✅ Remote CDP healthy: %s (protocol %s)\n", result.BrowserProduct, result.ProtocolVersion)
}

func remoteBrowserFailureStage(err error) (browser.RemoteCheckStage, bool) {
	var checkErr *browser.RemoteCheckError
	if !errors.As(err, &checkErr) {
		return "", false
	}
	return checkErr.Stage, true
}

func remoteBrowserStageLabel(stage browser.RemoteCheckStage) string {
	switch stage {
	case browser.RemoteCheckDiscovery:
		return "Discovery (/json/version)"
	case browser.RemoteCheckWebSocket:
		return "Browser WebSocket (Browser.getVersion)"
	case browser.RemoteCheckTarget:
		return "Isolated target creation"
	case browser.RemoteCheckEvaluation:
		return "Deterministic navigation/evaluation"
	case browser.RemoteCheckCleanup:
		return "Target/context cleanup"
	default:
		return string(stage)
	}
}
