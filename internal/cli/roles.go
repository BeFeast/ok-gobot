package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/role"
)

func newRolesCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roles",
		Short: "Manage prebuilt role manifests",
		Long:  `List bundled roles and scaffold them into a directory for customization.`,
	}

	cmd.AddCommand(newRolesListCommand())
	cmd.AddCommand(newRolesInitCommand(cfg))

	return cmd
}

func newRolesListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List bundled role manifests",
		RunE: func(cmd *cobra.Command, args []string) error {
			manifests, err := role.LoadBundled()
			if err != nil {
				return fmt.Errorf("loading bundled roles: %w", err)
			}

			if len(manifests) == 0 {
				fmt.Println("No bundled roles found.")
				return nil
			}

			fmt.Printf("Bundled roles (%d):\n\n", len(manifests))
			for _, m := range manifests {
				schedule := "(no schedule)"
				if m.HasSchedule() {
					schedule = m.Schedule
				}
				worker := "default"
				if m.Worker != "" {
					worker = m.Worker
				}
				tools := "all"
				if m.HasToolRestrictions() {
					tools = strings.Join(m.Tools, ", ")
				}

				fmt.Printf("  %-16s  schedule=%-14s  worker=%-10s  tools=%s\n",
					m.Name, schedule, worker, tools)
			}

			fmt.Println()
			fmt.Println("Run 'ok-gobot roles init --dir <path>' to scaffold these into a directory.")
			return nil
		},
	}
}

func newRolesInitCommand(cfg *config.Config) *cobra.Command {
	var dir string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold bundled role manifests into a directory",
		Long: `Copy all bundled role manifests into the specified directory.
Existing files are not overwritten. Edit the copied files to customize
roles for your deployment.

After scaffolding, set roles_dir in config.yaml to point at the directory:

  roles_dir: "/path/to/roles"
  roles_chat: 123456789  # Telegram chat ID for reports`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" {
				if cfg.RolesDir != "" {
					dir = cfg.RolesDir
				} else {
					return fmt.Errorf("--dir is required (or set roles_dir in config)")
				}
			}

			written, err := role.Scaffold(dir)
			if err != nil {
				return fmt.Errorf("scaffolding roles: %w", err)
			}

			if len(written) == 0 {
				fmt.Printf("All bundled roles already exist in %s — nothing to do.\n", dir)
				return nil
			}

			fmt.Printf("Scaffolded %d role(s) into %s:\n", len(written), dir)
			for _, path := range written {
				fmt.Printf("  %s\n", path)
			}

			// Check if roles_dir is configured
			if cfg.RolesDir == "" {
				fmt.Fprintf(os.Stderr, "\nHint: add to config.yaml:\n  roles_dir: %q\n  roles_chat: <your-chat-id>\n", dir)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "Target directory for role manifests")

	return cmd
}
