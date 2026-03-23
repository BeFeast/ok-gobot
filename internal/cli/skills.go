package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/config"
)

func newSkillsCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage third-party skills",
		Long:  `Install, list, remove, and audit third-party skills.`,
	}

	cmd.AddCommand(newSkillsListCommand(cfg))
	cmd.AddCommand(newSkillsInstallCommand(cfg))
	cmd.AddCommand(newSkillsRemoveCommand(cfg))
	cmd.AddCommand(newSkillsAuditCommand(cfg))

	return cmd
}

func newSkillsListCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			soulPath := cfg.GetSoulPath()
			skills, err := bootstrap.ListSkills(soulPath)
			if err != nil {
				return fmt.Errorf("failed to list skills: %w", err)
			}

			if len(skills) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No skills installed.")
				fmt.Fprintln(cmd.OutOrStdout(), "\nInstall a skill with: ok-gobot skills install <path-or-git-url>")
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Installed skills (%d):\n\n", len(skills))
			for _, skill := range skills {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", skill.Name, skill.Description)
			}
			return nil
		},
	}
}

func newSkillsInstallCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "install <path-or-git-url>",
		Short: "Install a skill from a local path or git URL",
		Long: `Install a third-party skill into the workspace.

The source must be a directory containing a SKILL.md file.
A safety audit runs automatically before installation.
Skills with symlinks, scripts, pipe-to-shell patterns, or
escaping markdown links will be rejected.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			soulPath := cfg.GetSoulPath()
			source := args[0]

			fmt.Fprintf(cmd.OutOrStdout(), "Auditing skill from %s ...\n", source)

			name, findings, err := bootstrap.InstallSkill(soulPath, source)

			// Print findings regardless of outcome.
			if len(findings) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "\nAudit findings:")
				for _, f := range findings {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", f)
				}
			}

			if err != nil {
				return err
			}

			if len(findings) > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Installed skill %q.\n", name)
			return nil
		},
	}
}

func newSkillsRemoveCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			soulPath := cfg.GetSoulPath()
			name := args[0]

			if err := bootstrap.RemoveSkill(soulPath, name); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Removed skill %q.\n", name)
			return nil
		},
	}
}

func newSkillsAuditCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "audit <path-or-name>",
		Short: "Run a safety audit on a skill",
		Long: `Audit a skill for safety issues.

Accepts either:
  - A local directory path to audit
  - The name of an installed skill

Checks for:
  - Symlinks (may escape the skill sandbox)
  - Script or executable files (.sh, .py, .exe, etc.)
  - Pipe-to-shell patterns (curl|bash, wget|sh, etc.)
  - Markdown links escaping the skill directory (../)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			// If it doesn't look like a path, treat it as an installed skill name.
			if !strings.Contains(target, "/") && !strings.Contains(target, "\\") && !strings.HasPrefix(target, ".") {
				soulPath := bootstrap.ExpandPath(cfg.GetSoulPath())
				candidate := fmt.Sprintf("%s/skills/%s", soulPath, target)
				if info, err := bootstrap.AuditSkill(candidate); err == nil || info != nil {
					target = candidate
				}
			}

			findings, err := bootstrap.AuditSkill(target)
			if err != nil {
				return fmt.Errorf("audit failed: %w", err)
			}

			if len(findings) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Audit passed: no issues found.")
				return nil
			}

			hasErrors := bootstrap.AuditHasErrors(findings)
			fmt.Fprintf(cmd.OutOrStdout(), "Audit findings (%d):\n\n", len(findings))
			for _, f := range findings {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", f)
			}

			if hasErrors {
				return fmt.Errorf("audit failed with %d error(s); fix the issues before installing", countErrors(findings))
			}

			fmt.Fprintln(cmd.OutOrStdout(), "\nAudit passed with warnings only.")
			return nil
		},
	}
}

func countErrors(findings []bootstrap.AuditFinding) int {
	n := 0
	for _, f := range findings {
		if f.Severity == bootstrap.SeverityError {
			n++
		}
	}
	return n
}
