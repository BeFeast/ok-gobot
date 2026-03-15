package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

func newEstopCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "estop [on|off|status]",
		Short: "Toggle the runtime emergency stop for dangerous tool families",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			action := "status"
			if len(args) > 0 {
				action = strings.ToLower(strings.TrimSpace(args[0]))
			}

			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			switch action {
			case "status":
				enabled, err := store.IsEmergencyStopEnabled()
				if err != nil {
					return fmt.Errorf("failed to read estop state: %w", err)
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), formatCLIEstopStatus(enabled))
				return nil
			case "on", "off":
				enabled := action == "on"
				if err := store.SetEmergencyStopEnabled(enabled); err != nil {
					return fmt.Errorf("failed to update estop state: %w", err)
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), formatCLIEstopStatus(enabled))
				return nil
			default:
				return fmt.Errorf("invalid action %q: use on, off, or status", action)
			}
		},
	}
}

func formatCLIEstopStatus(enabled bool) string {
	families := strings.Join(tools.DangerousToolFamilies(), ", ")
	if enabled {
		return fmt.Sprintf("estop is ON. Disabled tool families: %s", families)
	}
	return fmt.Sprintf("estop is OFF. Dangerous tool families enabled: %s", families)
}
