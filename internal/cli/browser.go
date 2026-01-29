package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"ok-gobot/internal/browser"
)

func newBrowserCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "browser",
		Short: "Manage Chrome browser for automation",
		Long:  `Setup and control Chrome browser for web automation tasks.`,
	}

	cmd.AddCommand(newBrowserSetupCommand())
	cmd.AddCommand(newBrowserStatusCommand())

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
			fmt.Println("========================\n")

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

func newBrowserStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check Chrome browser status",
		Run: func(cmd *cobra.Command, args []string) {
			manager := browser.NewManager("")

			fmt.Println("🌐 Chrome Browser Status")
			fmt.Println("========================\n")

			if manager.IsChromeInstalled() {
				fmt.Println("✅ Chrome installed")

				info, _ := manager.GetProfileInfo()
				if info.Exists {
					fmt.Printf("✅ Profile ready\n   Path: %s\n", info.Path)
					if info.History {
						fmt.Println("   ✓ History available")
					}
					if info.Extensions > 0 {
						fmt.Printf("   ✓ %d extensions\n", info.Extensions)
					}
				} else {
					fmt.Println("⚠️  Profile not configured")
					fmt.Println("   Run: ok-gobot browser setup")
				}
			} else {
				fmt.Println("❌ Chrome not installed")
			}
		},
	}
}
