package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
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
		Short: "List installed skills with their utility scores",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Discover skills from the soul path.
			soulPath := cfg.GetSoulPath()
			skillsDir := filepath.Join(bootstrap.ExpandPath(soulPath), "skills")

			skills, err := discoverSkillEntries(skillsDir)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to discover skills: %w", err)
			}

			// Load persisted scores.
			scores := map[string]int{}
			uses := map[string]int{}
			successes := map[string]int{}
			updatedAt := map[string]string{}

			if cfg.StoragePath != "" {
				store, storeErr := storage.New(cfg.StoragePath)
				if storeErr == nil {
					defer store.Close() //nolint:errcheck
					if stored, listErr := store.ListSkillScores(); listErr == nil {
						for _, ss := range stored {
							scores[ss.SkillName] = ss.Score
							uses[ss.SkillName] = ss.Uses
							successes[ss.SkillName] = ss.Successes
							updatedAt[ss.SkillName] = ss.UpdatedAt
						}
					}
				}
			}

			// Also include DB-only entries (skills removed from disk but still scored).
			seen := make(map[string]bool)
			for _, s := range skills {
				seen[s.Name] = true
			}
			for name := range scores {
				if !seen[name] {
					skills = append(skills, bootstrap.SkillEntry{
						Name:        name,
						Description: "(removed from disk)",
					})
				}
			}

			// Apply scores and sort.
			for i := range skills {
				if score, ok := scores[skills[i].Name]; ok {
					skills[i].UtilityScore = score
				}
			}
			sortSkillsByScore(skills)

			if len(skills) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No skills installed.")
				fmt.Fprintln(cmd.OutOrStdout(), "\nInstall a skill with: ok-gobot skills install <path-or-git-url>")
				return nil
			}

			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSCORE\tUSES\tSUCCESSES\tLAST USED\tDESCRIPTION")
			for _, s := range skills {
				u := uses[s.Name]
				su := successes[s.Name]
				ua := updatedAt[s.Name]
				if ua == "" {
					ua = "-"
				} else {
					ua = formatTime(ua)
				}
				desc := truncate(s.Description, 50)
				fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\t%s\n",
					s.Name, s.UtilityScore, u, su, ua, desc)
			}
			return w.Flush()
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

// discoverSkillEntries reads skill directories and returns a list of entries.
func discoverSkillEntries(skillsDir string) ([]bootstrap.SkillEntry, error) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, err
	}

	var skills []bootstrap.SkillEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		skillFile := filepath.Join(skillsDir, name, "SKILL.md")
		content, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		description := parseSkillDescription(string(content))
		skills = append(skills, bootstrap.SkillEntry{
			Name:        name,
			Description: description,
			Path:        skillFile,
		})
	}
	return skills, nil
}

// parseSkillDescription extracts the description from SKILL.md frontmatter or
// first non-heading line.
func parseSkillDescription(content string) string {
	lines := strings.Split(content, "\n")
	inFrontmatter := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			inFrontmatter = !inFrontmatter
			continue
		}
		if inFrontmatter {
			if strings.HasPrefix(trimmed, "description:") {
				return strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			}
			continue
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return trimmed
		}
	}
	return "No description available"
}

func sortSkillsByScore(skills []bootstrap.SkillEntry) {
	n := len(skills)
	for i := 1; i < n; i++ {
		for j := i; j > 0; j-- {
			a, b := skills[j-1], skills[j]
			if a.UtilityScore < b.UtilityScore ||
				(a.UtilityScore == b.UtilityScore && a.Name > b.Name) {
				skills[j-1], skills[j] = b, a
			} else {
				break
			}
		}
	}
}
