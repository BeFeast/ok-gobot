package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/config"
	"ok-gobot/internal/role"
	"ok-gobot/internal/rolejob"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

var runRoleWithHubCLI = runRoleWithHubCLIReal

func newRolesCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roles",
		Short: "List, inspect, run, and manage roles",
	}

	cmd.AddCommand(newRolesListCommand(cfg))
	cmd.AddCommand(newRolesShowCommand(cfg))
	cmd.AddCommand(newRolesRunCommand(cfg))
	cmd.AddCommand(newRolesEnableCommand(cfg))
	cmd.AddCommand(newRolesDisableCommand(cfg))

	return cmd
}

// loadRoles returns all roles from the configured roles path plus bundled roles.
func loadRoles(cfg *config.Config) ([]*role.Manifest, error) {
	var all []*role.Manifest

	if cfg.RolesPath != "" {
		manifests, errs := role.LoadDirLenient(cfg.RolesPath)
		for _, e := range errs {
			fmt.Printf("warning: %v\n", e)
		}
		all = append(all, manifests...)
	}

	bundled, err := role.LoadBundled()
	if err != nil {
		return all, fmt.Errorf("loading bundled roles: %w", err)
	}

	// Only add bundled roles not already loaded from disk.
	seen := make(map[string]bool, len(all))
	for _, m := range all {
		seen[m.Name] = true
	}
	for _, m := range bundled {
		if !seen[m.Name] {
			all = append(all, m)
		}
	}

	return all, nil
}

// findRole looks up a role by name from the loaded set.
func findRole(roles []*role.Manifest, name string) *role.Manifest {
	for _, m := range roles {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// --- list ---

func newRolesListCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available roles",
		RunE: func(cmd *cobra.Command, args []string) error {
			roles, err := loadRoles(cfg)
			if err != nil {
				return err
			}

			if len(roles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No roles found.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tWORKER\tSCHEDULE\tTOOLS\tAPPROVAL\tSOURCE")
			for _, m := range roles {
				source := "bundled"
				if m.SourcePath != "" {
					source = "disk"
				}
				schedule := m.Schedule
				if schedule == "" {
					schedule = "-"
				}
				toolsStr := "-"
				if len(m.Tools) > 0 {
					toolsStr = strings.Join(m.Tools, ", ")
					if len(toolsStr) > 30 {
						toolsStr = toolsStr[:27] + "..."
					}
				}
				worker := m.Worker
				if worker == "" {
					worker = "(default)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					m.Name, worker, schedule, toolsStr, m.Approval, source)
			}
			return w.Flush()
		},
	}
}

// --- show ---

func newRolesShowCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show detailed information about a role",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			roles, err := loadRoles(cfg)
			if err != nil {
				return err
			}

			m := findRole(roles, args[0])
			if m == nil {
				return fmt.Errorf("role %q not found", args[0])
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Name:      %s\n", m.Name)
			worker := m.Worker
			if worker == "" {
				worker = "(default)"
			}
			fmt.Fprintf(out, "Worker:    %s\n", worker)
			fmt.Fprintf(out, "Approval:  %s\n", m.Approval)
			if m.Schedule != "" {
				fmt.Fprintf(out, "Schedule:  %s\n", m.Schedule)
			}
			if len(m.Tools) > 0 {
				fmt.Fprintf(out, "Tools:     %s\n", strings.Join(m.Tools, ", "))
			}
			if m.SourcePath != "" {
				fmt.Fprintf(out, "Source:    %s\n", m.SourcePath)
			} else {
				fmt.Fprintf(out, "Source:    bundled\n")
			}
			if m.ReportTemplate != "" {
				fmt.Fprintf(out, "\nReport template:\n%s\n", m.ReportTemplate)
			}
			fmt.Fprintf(out, "\nPrompt:\n%s\n", m.Prompt)

			return nil
		},
	}
}

// --- run ---

func newRolesRunCommand(cfg *config.Config) *cobra.Command {
	var (
		input string
		tier  string
	)
	cmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Run a role manually as a durable job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			roles, err := loadRoles(cfg)
			if err != nil {
				return err
			}

			m := findRole(roles, args[0])
			if m == nil {
				return fmt.Errorf("role %q not found", args[0])
			}

			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			worker := m.Worker
			if tier != "" {
				worker = tier
			}

			js := runtime.NewJobService(store)
			sessionKey := rolejob.NewSessionKey("cli-role", 0, m.Name)
			spec := rolejob.BuildSpec(m, input, string(sessionKey), "", worker)
			job, err := js.StartDetached(cmd.Context(), spec, func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
				return runRoleWithHubCLI(ctx, cfg, store, m, input, worker, sessionKey, func(event agent.ToolEvent) {
					switch event.Type {
					case agent.ToolEventStarted:
						_ = store.IncrementJobToolCallCount(job.JobID)
						_ = svc.AppendEvent(job.JobID, runtime.JobEventProgress, "tool started: "+event.ToolName, map[string]any{
							"tool": event.ToolName,
						})
					case agent.ToolEventFinished:
						msg := "tool finished: " + event.ToolName
						if event.Err != nil {
							msg = "tool failed: " + event.ToolName
						}
						_ = svc.AppendEvent(job.JobID, runtime.JobEventProgress, msg, map[string]any{
							"tool": event.ToolName,
						})
					}
				})
			})
			if err != nil {
				return fmt.Errorf("failed to start job: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Job started: %s\n", job.JobID)
			finalJob, err := waitForRoleJob(cmd.Context(), store, job.JobID)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Job finished: %s\n", finalJob.Status)
			if finalJob.Status != string(runtime.JobStatusSucceeded) {
				if finalJob.Error != "" {
					return fmt.Errorf("job %s finished with status %s: %s", finalJob.JobID, finalJob.Status, finalJob.Error)
				}
				return fmt.Errorf("job %s finished with status %s", finalJob.JobID, finalJob.Status)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "additional input for the role")
	cmd.Flags().StringVar(&tier, "tier", "", "override worker tier (cheap, standard, premium, local)")
	return cmd
}

func waitForRoleJob(ctx context.Context, store *storage.Store, jobID string) (*storage.Job, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := store.GetJob(jobID)
		if err != nil {
			return nil, err
		}
		if job != nil && isRoleTerminalStatus(job.Status) {
			return job, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func isRoleTerminalStatus(status string) bool {
	switch status {
	case string(runtime.JobStatusSucceeded),
		string(runtime.JobStatusFailed),
		string(runtime.JobStatusCancelled),
		string(runtime.JobStatusTimedOut),
		string(runtime.JobStatusBudgetExceeded):
		return true
	default:
		return false
	}
}

func runRoleWithHubCLIReal(
	ctx context.Context,
	cfg *config.Config,
	store *storage.Store,
	m *role.Manifest,
	input string,
	_ string,
	sessionKey agent.SessionKey,
	onToolEvent func(agent.ToolEvent),
) (runtime.JobRunResult, error) {
	hub, err := buildCLIRoleHub(cfg, store)
	if err != nil {
		return runtime.JobRunResult{}, err
	}
	return rolejob.RunWithHub(ctx, hub, m, input, rolejob.RunOptions{
		SessionKey:  sessionKey,
		ChatID:      0,
		OnToolEvent: onToolEvent,
	})
}

func buildCLIRoleHub(cfg *config.Config, store *storage.Store) (*agent.RuntimeHub, error) {
	soulPath := cfg.GetSoulPath()
	personality, err := agent.NewPersonality(soulPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load personality: %w", err)
	}
	personality.SetScoreProvider(store)

	var registry *agent.AgentRegistry
	if len(cfg.Agents) > 0 {
		registry, err = agent.NewAgentRegistry(cfg.Agents, cfg.AI.Model, soulPath)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize agent registry: %w", err)
		}
	}

	apiKey := strings.TrimSpace(cfg.AI.APIKey)
	if apiKey == "" && cfg.AI.Provider == "anthropic" {
		if creds, err := ai.LoadAnthropicOAuthCredentials(""); err == nil && creds != nil {
			apiKey = "oauth:" + creds.AccessToken
		}
	}
	client, err := ai.NewClientWithDroid(ai.ProviderConfig{
		Name:       cfg.AI.Provider,
		APIKey:     apiKey,
		Model:      cfg.AI.Model,
		BaseURL:    cfg.AI.BaseURL,
		ThinkLevel: cfg.AI.DefaultThinking,
	}, ai.DroidConfig{
		BinaryPath: cfg.AI.Droid.BinaryPath,
		AutoLevel:  cfg.AI.Droid.AutoLevel,
		WorkDir:    cfg.AI.Droid.WorkDir,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize AI client: %w", err)
	}

	toolRegistry, err := tools.LoadFromConfigWithOptions(personality.BasePath, &tools.ToolsConfig{
		OpenAIAPIKey:         cfg.AI.APIKey,
		OpenAIBaseURL:        cfg.AI.BaseURL,
		ChromePath:           cfg.Browser.ChromePath,
		BrowserProfile:       cfg.Browser.ProfilePath,
		BrowserDebugURL:      cfg.Browser.DebugURL,
		AIClient:             client,
		Contacts:             cfg.Contacts,
		SkillVersionSaveFunc: func(path string) error { return bootstrap.SaveSkillVersion(path, bootstrap.DefaultMaxVersions) },
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load tools: %w", err)
	}

	var router *ai.Router
	if len(cfg.AI.Routing.Routes) > 0 {
		router = ai.NewRouter(cfg.AI.Routing.Routes, cfg.AI.Model)
	}
	resolver := &agent.RunResolver{
		Store:              store,
		Registry:           registry,
		DefaultPersonality: personality,
		AIConfig: agent.AIResolverConfig{
			Provider:        cfg.AI.Provider,
			Model:           cfg.AI.Model,
			APIKey:          apiKey,
			BaseURL:         cfg.AI.BaseURL,
			DefaultThinking: cfg.AI.DefaultThinking,
			DefaultClient:   client,
			ModelAliases:    cfg.ModelAliases,
		},
		ToolRegistry: toolRegistry,
		Router:       router,
	}
	hub := agent.NewRuntimeHub(resolver)
	resolver.SubagentSubmitter = hub
	return hub, nil
}

// --- enable ---

func newRolesEnableCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a scheduled role's cron job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return toggleRoleCronJob(cmd, cfg, args[0], true)
		},
	}
}

// --- disable ---

func newRolesDisableCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name>",
		Short: "Disable a scheduled role's cron job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return toggleRoleCronJob(cmd, cfg, args[0], false)
		},
	}
}

func toggleRoleCronJob(cmd *cobra.Command, cfg *config.Config, roleName string, enable bool) error {
	store, err := storage.New(cfg.StoragePath)
	if err != nil {
		return fmt.Errorf("failed to open storage: %w", err)
	}
	defer store.Close() //nolint:errcheck

	jobs, err := store.GetAllCronJobs()
	if err != nil {
		return fmt.Errorf("failed to list cron jobs: %w", err)
	}

	// Find the cron job for this role name.
	prefix := "[role:" + roleName + "]"
	for _, j := range jobs {
		if strings.HasPrefix(j.Task, prefix) {
			if err := store.ToggleCronJob(j.ID, enable); err != nil {
				return fmt.Errorf("failed to toggle cron job: %w", err)
			}
			action := "enabled"
			if !enable {
				action = "disabled"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Role %q cron job %s.\n", roleName, action)
			return nil
		}
	}

	return fmt.Errorf("no cron job found for role %q (is it scheduled?)", roleName)
}
