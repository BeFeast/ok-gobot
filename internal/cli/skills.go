package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/config"
)

func newSkillsCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage agent skills",
		Long:  `Install, list, remove, audit third-party skills, and manage skill version history.`,
	}

	cmd.AddCommand(newSkillsListCommand(cfg))
	cmd.AddCommand(newSkillsInstallCommand(cfg))
	cmd.AddCommand(newSkillsRemoveCommand(cfg))
	cmd.AddCommand(newSkillsAuditCommand(cfg))
	cmd.AddCommand(newSkillsHistoryCommand(cfg))
	cmd.AddCommand(newSkillsRollbackCommand(cfg))

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

// --- history ---

func newSkillsHistoryCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "history <skill-name>",
		Short: "Show version history for a skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			skillName := args[0]
			soulPath := bootstrap.ExpandPath(cfg.GetSoulPath())
			skillDir := filepath.Join(soulPath, "skills", skillName)

			versions, err := bootstrap.ListSkillVersions(skillDir)
			if err != nil {
				return fmt.Errorf("failed to list versions: %w", err)
			}

			if len(versions) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No version history for skill %q.\n", skillName)
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "#\tFILENAME\tTIMESTAMP")
			for i, v := range versions {
				fmt.Fprintf(w, "%d\t%s\t%s\n", i+1, v.Filename, v.Timestamp.Format("2006-01-02 15:04:05"))
			}
			return w.Flush()
		},
	}
}

// --- rollback ---

func newSkillsRollbackCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <skill-name> <version-filename>",
		Short: "Restore a previous version of a skill",
		Long: `Restore a previous version of a skill.

Use 'skills history <skill-name>' to list available versions.
The version-filename argument is the filename shown in the history output
(e.g. SKILL.md.v20240101120000123456).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			skillName := args[0]
			versionFilename := args[1]
			soulPath := bootstrap.ExpandPath(cfg.GetSoulPath())
			skillDir := filepath.Join(soulPath, "skills", skillName)

			// Save the current version before rolling back so it can be recovered.
			skillFile := filepath.Join(skillDir, "SKILL.md")
			if err := bootstrap.SaveSkillVersion(skillFile, bootstrap.DefaultMaxVersions); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not save current version before rollback: %v\n", err)
			}

			if err := bootstrap.RollbackSkillVersion(skillDir, versionFilename); err != nil {
				return fmt.Errorf("rollback failed: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Skill %q restored from %s.\n", skillName, versionFilename)
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
