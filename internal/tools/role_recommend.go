package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"ok-gobot/internal/storage"
)

// RoleRecommendStore abstracts the storage queries needed for role recommendation.
type RoleRecommendStore interface {
	RecentUserMessages(limit int) ([]storage.SessionMessageV2, error)
	RecentCompletedJobs(limit int) ([]storage.Job, error)
	AllCronJobs() ([]storage.CronJob, error)
}

// RoleRecommendTool analyzes chat and job patterns to recommend new agent roles.
type RoleRecommendTool struct {
	store         RoleRecommendStore
	existingRoles []string // names of currently configured roles
}

// NewRoleRecommendTool creates a role recommendation tool.
func NewRoleRecommendTool(store RoleRecommendStore, existingRoles []string) *RoleRecommendTool {
	return &RoleRecommendTool{
		store:         store,
		existingRoles: existingRoles,
	}
}

func (r *RoleRecommendTool) Name() string {
	return "role_recommend"
}

func (r *RoleRecommendTool) Description() string {
	return "Analyze chat and job patterns to recommend new agent roles with schedules and output examples"
}

func (r *RoleRecommendTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Command: 'analyze' to get role recommendations",
				"enum":        []string{"analyze"},
			},
		},
		"required": []string{"command"},
	}
}

func (r *RoleRecommendTool) Execute(ctx context.Context, args ...string) (string, error) {
	return r.analyze()
}

func (r *RoleRecommendTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	cmd := params["command"]
	if cmd == "" {
		cmd = "analyze"
	}
	if cmd != "analyze" {
		return "", fmt.Errorf("unknown command: %s (use 'analyze')", cmd)
	}
	return r.analyze()
}

// patternCategory groups related user intents.
type patternCategory struct {
	Name     string
	Keywords []string
	Schedule string // suggested cron expression
	Example  string // example output
}

var knownPatterns = []patternCategory{
	{
		Name:     "code-reviewer",
		Keywords: []string{"review", "pr", "pull request", "code review", "diff", "merge"},
		Schedule: "0 9 * * 1-5",
		Example:  "3 open PRs need review: #42 (auth refactor, 2 days old), #45 (bugfix, minor), #47 (new feature, needs tests)",
	},
	{
		Name:     "deployment-monitor",
		Keywords: []string{"deploy", "release", "rollback", "pipeline", "ci", "cd", "build status"},
		Schedule: "*/30 * * * *",
		Example:  "Deploy #891 to production: healthy (3/3 replicas). Last error rate: 0.02%. Next scheduled release: Thursday 14:00",
	},
	{
		Name:     "log-analyst",
		Keywords: []string{"log", "logs", "error", "exception", "stack trace", "debug", "trace", "warning"},
		Schedule: "0 8 * * *",
		Example:  "Overnight log summary: 12 unique errors (3 new). Top: NullPointerException in PaymentService (47 hits). Alert: disk usage at 89%",
	},
	{
		Name:     "content-writer",
		Keywords: []string{"write", "blog", "article", "draft", "post", "content", "copy", "newsletter"},
		Schedule: "0 10 * * 1",
		Example:  "Weekly content digest: 2 drafts ready for review, 1 published last week (1.2k views). Suggested topic: 'How we reduced API latency by 40%'",
	},
	{
		Name:     "data-reporter",
		Keywords: []string{"report", "metrics", "analytics", "dashboard", "stats", "kpi", "chart", "data"},
		Schedule: "0 7 * * 1",
		Example:  "Weekly KPI report: DAU +12%, API p95 latency 180ms (down from 220ms), error rate 0.3%. Revenue: $42k (+8% WoW)",
	},
	{
		Name:     "security-auditor",
		Keywords: []string{"security", "vulnerability", "cve", "audit", "scan", "patch", "update dependencies"},
		Schedule: "0 6 * * 1",
		Example:  "Security scan: 2 high-severity CVEs in dependencies (lodash, openssl). 5 packages have updates available. No exposed secrets detected",
	},
	{
		Name:     "backup-checker",
		Keywords: []string{"backup", "restore", "snapshot", "dump", "database backup", "archive"},
		Schedule: "0 5 * * *",
		Example:  "Daily backup status: DB snapshot completed (2.3GB, verified). Last 7 backups healthy. Storage used: 45/100GB",
	},
	{
		Name:     "meeting-preparer",
		Keywords: []string{"meeting", "agenda", "standup", "retro", "sprint", "sync", "calendar"},
		Schedule: "0 8 * * 1-5",
		Example:  "Standup prep: Yesterday you merged 2 PRs and fixed the auth bug. Today: API pagination (#181) is next. Blocker: waiting on design review for settings page",
	},
	{
		Name:     "infra-watchdog",
		Keywords: []string{"server", "cpu", "memory", "disk", "uptime", "health", "monitor", "alert", "infrastructure"},
		Schedule: "*/15 * * * *",
		Example:  "Infra health: all 5 services green. CPU avg 34%, memory 62%. Warning: disk on worker-2 at 78% (growing ~2%/day)",
	},
	{
		Name:     "dependency-updater",
		Keywords: []string{"update", "upgrade", "dependency", "package", "npm", "go mod", "pip", "outdated"},
		Schedule: "0 9 * * 1",
		Example:  "Dependency report: 8 updates available (3 minor, 4 patch, 1 major). Major: react 18→19 (breaking changes in concurrent mode). Recommended: update patch versions first",
	},
	{
		Name:     "research-digest",
		Keywords: []string{"research", "learn", "investigate", "explore", "search", "find out", "look into"},
		Schedule: "0 9 * * 1,4",
		Example:  "Research digest: 3 topics explored this week. Key finding: switching to connection pooling could reduce DB latency by ~30%. Saved 2 bookmarks for follow-up",
	},
	{
		Name:     "issue-triager",
		Keywords: []string{"issue", "bug", "ticket", "triage", "backlog", "priority", "jira", "github issue"},
		Schedule: "0 9 * * 1-5",
		Example:  "Triage summary: 5 new issues (2 bugs, 3 features). Suggested priorities: #201 (P1, user-facing crash), #203 (P2, performance regression), rest P3",
	},
}

func (r *RoleRecommendTool) analyze() (string, error) {
	messages, err := r.store.RecentUserMessages(200)
	if err != nil {
		return "", fmt.Errorf("failed to load recent messages: %w", err)
	}

	jobs, err := r.store.RecentCompletedJobs(100)
	if err != nil {
		return "", fmt.Errorf("failed to load recent jobs: %w", err)
	}

	cronJobs, err := r.store.AllCronJobs()
	if err != nil {
		return "", fmt.Errorf("failed to load cron jobs: %w", err)
	}

	if len(messages) == 0 && len(jobs) == 0 && len(cronJobs) == 0 {
		return "No chat or job history found yet. Use the bot for a while and then ask for role recommendations.", nil
	}

	// Build a corpus of all user-facing text for pattern matching.
	var corpus []string
	for _, m := range messages {
		corpus = append(corpus, m.Content)
	}
	for _, j := range jobs {
		corpus = append(corpus, j.Description)
		corpus = append(corpus, j.Kind)
	}
	for _, c := range cronJobs {
		corpus = append(corpus, c.Task)
	}

	// Score each pattern category.
	type scoredPattern struct {
		Pattern patternCategory
		Hits    int
	}
	var scored []scoredPattern
	for _, p := range knownPatterns {
		hits := countPatternHits(corpus, p.Keywords)
		if hits > 0 {
			scored = append(scored, scoredPattern{Pattern: p, Hits: hits})
		}
	}

	// Sort by hit count descending.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Hits > scored[j].Hits
	})

	// Filter out roles that already exist.
	existingSet := make(map[string]bool, len(r.existingRoles))
	for _, name := range r.existingRoles {
		existingSet[strings.ToLower(name)] = true
	}

	var recommendations []scoredPattern
	for _, s := range scored {
		if existingSet[strings.ToLower(s.Pattern.Name)] {
			continue
		}
		recommendations = append(recommendations, s)
	}

	// Build the output.
	var sb strings.Builder
	sb.WriteString("## Role Recommendations\n\n")

	sb.WriteString(fmt.Sprintf("Analyzed %d messages, %d completed jobs, and %d scheduled tasks.\n\n",
		len(messages), len(jobs), len(cronJobs)))

	if len(r.existingRoles) > 0 {
		sb.WriteString("**Current roles:** ")
		sb.WriteString(strings.Join(r.existingRoles, ", "))
		sb.WriteString("\n\n")
	}

	if len(recommendations) == 0 {
		sb.WriteString("No new role recommendations at this time. Your existing roles cover the observed patterns well.\n")
		if len(scored) > 0 {
			sb.WriteString("\n**Patterns detected (already covered):**\n")
			for _, s := range scored {
				sb.WriteString(fmt.Sprintf("- %s (%d matches)\n", s.Pattern.Name, s.Hits))
			}
		}
		return sb.String(), nil
	}

	// Cap at 5 recommendations.
	if len(recommendations) > 5 {
		recommendations = recommendations[:5]
	}

	for i, rec := range recommendations {
		sb.WriteString(fmt.Sprintf("### %d. %s\n", i+1, rec.Pattern.Name))
		sb.WriteString(fmt.Sprintf("**Signal strength:** %d pattern matches\n", rec.Hits))
		sb.WriteString(fmt.Sprintf("**Suggested schedule:** `%s`\n", rec.Pattern.Schedule))
		sb.WriteString(fmt.Sprintf("**Example output:**\n> %s\n\n", rec.Pattern.Example))
	}

	// Show summary of existing cron coverage.
	if len(cronJobs) > 0 {
		sb.WriteString("---\n**Existing scheduled tasks** (for reference):\n")
		for _, c := range cronJobs {
			status := "enabled"
			if !c.Enabled {
				status = "disabled"
			}
			sb.WriteString(fmt.Sprintf("- `%s` %s (%s)\n", c.Expression, c.Task, status))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("To create a role, configure it in your `config.yaml` under `agents:` with a dedicated soul directory.\n")

	return sb.String(), nil
}

// countPatternHits counts how many corpus entries match any of the keywords.
func countPatternHits(corpus []string, keywords []string) int {
	hits := 0
	for _, text := range corpus {
		lower := strings.ToLower(text)
		for _, kw := range keywords {
			if containsWord(lower, strings.ToLower(kw)) {
				hits++
				break // count each corpus entry at most once per category
			}
		}
	}
	return hits
}

// containsWord checks if text contains the keyword as a word (not just a substring).
// For multi-word keywords it falls back to simple substring matching.
func containsWord(text, keyword string) bool {
	if strings.Contains(keyword, " ") {
		return strings.Contains(text, keyword)
	}
	idx := 0
	for {
		pos := strings.Index(text[idx:], keyword)
		if pos < 0 {
			return false
		}
		pos += idx
		start := pos
		end := pos + len(keyword)

		startOK := start == 0
		if !startOK {
			r, _ := utf8.DecodeLastRuneInString(text[:start])
			startOK = !unicode.IsLetter(r)
		}
		endOK := end >= len(text)
		if !endOK {
			r, _ := utf8.DecodeRuneInString(text[end:])
			endOK = !unicode.IsLetter(r)
		}
		if startOK && endOK {
			return true
		}
		idx = pos + 1
		if idx >= len(text) {
			return false
		}
	}
}
