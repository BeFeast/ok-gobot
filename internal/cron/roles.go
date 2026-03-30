package cron

import (
	"fmt"
	"log"
	"strings"

	"ok-gobot/internal/role"
)

const roleTaskPrefix = "[role:"

// roleTaskFor returns the task string used to identify a role cron job.
func roleTaskFor(name, prompt string) string {
	return fmt.Sprintf("%s%s] %s", roleTaskPrefix, name, prompt)
}

// isRoleTask reports whether task was created by RegisterRoleJobs.
func isRoleTask(task string) bool {
	return strings.HasPrefix(task, roleTaskPrefix)
}

// roleNameFromTask extracts the role name from a role task string.
// Returns "" if the string is not a role task.
func roleNameFromTask(task string) string {
	if !isRoleTask(task) {
		return ""
	}
	rest := task[len(roleTaskPrefix):]
	idx := strings.IndexByte(rest, ']')
	if idx < 0 {
		return ""
	}
	return rest[:idx]
}

// RegisterRoleJobs ensures that each manifest with a schedule has a
// corresponding enabled cron job in the store. Jobs are identified by the
// [role:<name>] prefix in the task field so that duplicate registration is
// avoided across restarts.
//
// chatID is the Telegram chat that will receive scheduled reports.
// It is typically the operator's admin chat ID.
//
// Only manifests that define a schedule are registered; others are silently
// skipped.
func (s *Scheduler) RegisterRoleJobs(manifests []*role.Manifest, chatID int64) error {
	if chatID == 0 {
		return fmt.Errorf("register role jobs: chatID must be non-zero")
	}

	// Build a set of role names that already have a cron job so we do not
	// create duplicates on restart.
	existing, err := s.store.GetCronJobs()
	if err != nil {
		return fmt.Errorf("register role jobs: fetching existing jobs: %w", err)
	}
	registered := make(map[string]bool, len(existing))
	for _, j := range existing {
		if name := roleNameFromTask(j.Task); name != "" {
			registered[name] = true
		}
	}

	for _, m := range manifests {
		if !m.HasSchedule() {
			continue
		}
		if registered[m.Name] {
			log.Printf("[roles] cron job for role %q already registered, skipping", m.Name)
			continue
		}

		task := roleTaskFor(m.Name, m.Prompt)
		if _, err := s.AddJob(m.Schedule, task, chatID); err != nil {
			log.Printf("[roles] failed to register cron job for role %q: %v", m.Name, err)
			continue
		}
		log.Printf("[roles] registered cron job for role %q (schedule: %s)", m.Name, m.Schedule)
	}
	return nil
}
