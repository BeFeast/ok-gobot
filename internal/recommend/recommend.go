// Package recommend analyzes chat and job patterns to propose new agent roles.
package recommend

import (
	"fmt"
	"sort"
	"strings"

	"ok-gobot/internal/storage"
)

// PatternStore is the subset of storage.Store the recommender needs.
type PatternStore interface {
	GetChatActivityStats(limit int) ([]storage.ChatActivityRow, error)
	GetRecentUserMessages(limit int) ([]storage.UserMessageRow, error)
	CronJobSummary() ([]storage.CronJob, error)
	JobKindCounts(limit int) (map[string]int, error)
}

// RoleRecommendation is a concrete proposal for a new agent role.
type RoleRecommendation struct {
	Name          string // e.g. "monitor"
	Description   string // what this role does
	Schedule      string // suggested cron expression (5-field)
	OutputExample string // sample output the role would produce
	Rationale     string // why we recommend it, based on observed data
}

// topicDef maps keyword groups to a role archetype.
type topicDef struct {
	keywords      []string
	role          string
	description   string
	schedule      string
	outputExample string
}

// builtinTopics defines the role archetypes we can detect.
var builtinTopics = []topicDef{
	{
		keywords:    []string{"check", "status", "monitor", "running", "health", "alive", "up", "ping", "uptime"},
		role:        "monitor",
		description: "Periodically check service health and alert on anomalies.",
		schedule:    "*/15 * * * *",
		outputExample: `All systems operational.
  - api: 200 OK (43ms)
  - db:  connected (2 replicas)
  - queue: 0 backlog`,
	},
	{
		keywords:    []string{"summary", "report", "digest", "stats", "analytics", "overview", "numbers"},
		role:        "reporter",
		description: "Generate periodic summaries of activity, metrics, or progress.",
		schedule:    "0 9 * * *",
		outputExample: `Daily digest (2026-03-21):
  - 42 messages processed
  - 3 jobs completed, 1 failed
  - Top topic: deployment`,
	},
	{
		keywords:    []string{"remind", "reminder", "follow up", "don't forget", "later", "schedule", "alert me"},
		role:        "reminder",
		description: "Send scheduled reminders and follow-up nudges.",
		schedule:    "0 9 * * 1-5",
		outputExample: `Reminder: standup in 15 minutes.
Open items from yesterday:
  - Review PR #42
  - Reply to infra thread`,
	},
	{
		keywords:    []string{"search", "find", "look up", "research", "google", "browse", "fetch"},
		role:        "researcher",
		description: "Perform scheduled web research and compile findings.",
		schedule:    "0 8 * * 1",
		outputExample: `Weekly research brief:
  1. Go 1.24 released — notable: range-over-func stabilised
  2. SQLite 3.48 — new JSON improvements
  3. Telegram Bot API 8.1 — reaction support`,
	},
	{
		keywords:    []string{"deploy", "build", "test", "ci", "cd", "release", "update", "upgrade", "migration"},
		role:        "ops",
		description: "Run maintenance tasks: builds, deploys, dependency updates.",
		schedule:    "0 3 * * 0",
		outputExample: `Weekly maintenance completed:
  - Dependencies updated (2 minor bumps)
  - go test ./... — all 147 tests pass
  - Docker image rebuilt and pushed`,
	},
	{
		keywords:    []string{"clean", "tidy", "archive", "prune", "delete old", "rotate", "backup"},
		role:        "janitor",
		description: "Housekeeping: prune old data, rotate logs, run backups.",
		schedule:    "0 2 * * 0",
		outputExample: `Housekeeping report:
  - Pruned 230 old session messages (>30d)
  - Log rotation: 3 files archived
  - Disk usage: 42% (was 58%)`,
	},
}

// Recommender analyzes stored patterns and generates role proposals.
type Recommender struct {
	store PatternStore
}

// New creates a Recommender backed by the given store.
func New(store PatternStore) *Recommender {
	return &Recommender{store: store}
}

// Analyze gathers data and returns zero or more role recommendations.
func (r *Recommender) Analyze() ([]RoleRecommendation, error) {
	messages, err := r.store.GetRecentUserMessages(500)
	if err != nil {
		return nil, fmt.Errorf("failed to read recent messages: %w", err)
	}

	cronJobs, err := r.store.CronJobSummary()
	if err != nil {
		return nil, fmt.Errorf("failed to read cron jobs: %w", err)
	}

	jobKinds, err := r.store.JobKindCounts(50)
	if err != nil {
		return nil, fmt.Errorf("failed to read job kinds: %w", err)
	}

	activity, err := r.store.GetChatActivityStats(50)
	if err != nil {
		return nil, fmt.Errorf("failed to read activity stats: %w", err)
	}

	// Count keyword hits across user messages.
	topicHits := r.countTopicHits(messages)

	// Build set of existing cron task keywords to avoid redundant recommendations.
	existingCronKeywords := r.existingCronKeywords(cronJobs)

	var recs []RoleRecommendation

	// Score each topic and recommend if hits exceed the threshold.
	threshold := r.hitThreshold(len(messages))
	for _, td := range builtinTopics {
		hits := topicHits[td.role]
		if hits < threshold {
			continue
		}

		// Skip if there's already a cron job that covers this role.
		if r.cronAlreadyCovers(td, existingCronKeywords) {
			continue
		}

		rationale := fmt.Sprintf(
			"Detected %d messages matching %q patterns across %d recent user messages.",
			hits, td.role, len(messages),
		)

		recs = append(recs, RoleRecommendation{
			Name:          td.role,
			Description:   td.description,
			Schedule:      td.schedule,
			OutputExample: td.outputExample,
			Rationale:     rationale,
		})
	}

	// If there are many durable jobs of a specific kind, suggest a dedicated role.
	recs = append(recs, r.recommendFromJobKinds(jobKinds)...)

	// If there are many active sessions, suggest a coordinator role.
	recs = append(recs, r.recommendFromActivity(activity)...)

	// Sort by name for stable output.
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].Name < recs[j].Name
	})

	return recs, nil
}

// hitThreshold returns the minimum number of keyword matches required.
// With very few messages we require at least 2 hits; with more data, 3+.
func (r *Recommender) hitThreshold(totalMessages int) int {
	if totalMessages < 20 {
		return 2
	}
	return 3
}

// countTopicHits scores each topic archetype against the message corpus.
func (r *Recommender) countTopicHits(messages []storage.UserMessageRow) map[string]int {
	hits := make(map[string]int)
	for _, msg := range messages {
		lower := strings.ToLower(msg.Content)
		for _, td := range builtinTopics {
			for _, kw := range td.keywords {
				if strings.Contains(lower, kw) {
					hits[td.role]++
					break // count each message at most once per topic
				}
			}
		}
	}
	return hits
}

// existingCronKeywords extracts lowercase keywords from existing cron task descriptions.
func (r *Recommender) existingCronKeywords(jobs []storage.CronJob) map[string]bool {
	kw := make(map[string]bool)
	for _, job := range jobs {
		for _, word := range strings.Fields(strings.ToLower(job.Task)) {
			kw[word] = true
		}
	}
	return kw
}

// cronAlreadyCovers checks if existing cron keywords overlap significantly
// with a topic's keyword set.
func (r *Recommender) cronAlreadyCovers(td topicDef, cronKW map[string]bool) bool {
	matches := 0
	for _, kw := range td.keywords {
		if cronKW[kw] {
			matches++
		}
	}
	// If 2+ keywords from this topic already appear in cron tasks, skip.
	return matches >= 2
}

// recommendFromJobKinds looks for frequently-used job kinds that don't have
// a matching cron job and suggests scheduling them.
func (r *Recommender) recommendFromJobKinds(kinds map[string]int) []RoleRecommendation {
	var recs []RoleRecommendation
	for kind, count := range kinds {
		if count < 3 {
			continue
		}
		recs = append(recs, RoleRecommendation{
			Name:        fmt.Sprintf("%s-scheduler", kind),
			Description: fmt.Sprintf("Automate recurring %q jobs that are currently triggered manually.", kind),
			Schedule:    "0 8 * * 1-5",
			OutputExample: fmt.Sprintf(`Scheduled %s job completed.
  - Status: succeeded
  - Duration: 2m 15s`, kind),
			Rationale: fmt.Sprintf(
				"Found %d manually-triggered %q jobs. A scheduled role could automate these.",
				count, kind,
			),
		})
	}
	return recs
}

// recommendFromActivity checks if there are many active chats that could
// benefit from a coordinator or triage role.
func (r *Recommender) recommendFromActivity(activity []storage.ChatActivityRow) []RoleRecommendation {
	if len(activity) < 5 {
		return nil
	}

	// Count unique agents in use.
	agents := make(map[string]int)
	totalMessages := 0
	for _, a := range activity {
		agents[a.AgentID] += a.MessageCount
		totalMessages += a.MessageCount
	}

	if len(agents) < 2 || totalMessages < 50 {
		return nil
	}

	return []RoleRecommendation{
		{
			Name:        "triage",
			Description: "Route incoming messages to the best-suited agent based on content and history.",
			Schedule:    "0 8 * * *",
			OutputExample: fmt.Sprintf(`Triage summary:
  - %d active sessions across %d agents
  - Busiest agent: %s
  - Suggestion: redistribute load from busiest agent`, len(activity), len(agents), busiestAgent(agents)),
			Rationale: fmt.Sprintf(
				"Observed %d active sessions using %d distinct agents with %d total messages. A triage role could improve routing.",
				len(activity), len(agents), totalMessages,
			),
		},
	}
}

// busiestAgent returns the agent with the highest message count.
func busiestAgent(agents map[string]int) string {
	best := ""
	bestCount := 0
	for name, count := range agents {
		if count > bestCount {
			best = name
			bestCount = count
		}
	}
	return best
}

// Format renders recommendations as human-readable text.
func Format(recs []RoleRecommendation) string {
	if len(recs) == 0 {
		return "No role recommendations at this time. Keep chatting and scheduling jobs — patterns will emerge."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d role recommendation(s) based on observed patterns:\n", len(recs)))

	for i, rec := range recs {
		sb.WriteString(fmt.Sprintf("\n--- %d. %s ---\n", i+1, rec.Name))
		sb.WriteString(fmt.Sprintf("Description: %s\n", rec.Description))
		sb.WriteString(fmt.Sprintf("Suggested schedule: %s\n", rec.Schedule))
		sb.WriteString(fmt.Sprintf("Rationale: %s\n", rec.Rationale))
		sb.WriteString("Sample output:\n")
		for _, line := range strings.Split(rec.OutputExample, "\n") {
			sb.WriteString(fmt.Sprintf("  | %s\n", line))
		}
	}

	return sb.String()
}
