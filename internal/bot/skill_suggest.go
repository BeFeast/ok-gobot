package bot

import (
	"fmt"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/skillsuggest"
)

// handleSkillSuggestCommand handles /skill_suggest <job-id>.
func (b *Bot) handleSkillSuggestCommand(c telebot.Context) error {
	if !b.authManager.IsAdmin(c.Sender().ID) {
		return c.Send("This command is only available to administrators.")
	}
	jobID := strings.TrimSpace(c.Message().Payload)
	if jobID == "" {
		return c.Send("Usage: /skill_suggest <job-id>")
	}
	if b.personality == nil || b.personality.BasePath == "" {
		return c.Send("Skill drafts need a configured soul_path.")
	}

	job, err := b.store.GetJob(jobID)
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to get job: %v", err))
	}
	if job == nil {
		return c.Send(fmt.Sprintf("Job %s not found.", jobID))
	}
	events, err := b.store.ListJobEvents(jobID, 100)
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to list job events: %v", err))
	}
	artifacts, err := b.store.ListJobArtifacts(jobID, 100)
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to list job artifacts: %v", err))
	}

	draft, err := skillsuggest.CreateDraft(b.personality.BasePath, job, events, artifacts)
	if err != nil && draft == nil {
		return c.Send(fmt.Sprintf("Failed to create skill draft: %v", err))
	}
	if err != nil {
		msg := fmt.Sprintf("Skill draft created but failed audit: %s\nPath: %s\n\n%s",
			err.Error(), draft.Dir, formatAuditFindings(draft.Findings))
		return c.Send(msg)
	}
	msg := fmt.Sprintf("Skill draft created: %s\nPath: %s\nAudit: passed\n\nInstall after review with: ok-gobot skills install %s",
		draft.Name, draft.Dir, draft.Dir)
	return c.Send(msg)
}

func formatAuditFindings(findings []bootstrap.AuditFinding) string {
	if len(findings) == 0 {
		return "No audit findings."
	}
	var b strings.Builder
	b.WriteString("Audit findings:\n")
	for _, f := range findings {
		b.WriteString("- ")
		b.WriteString(f.String())
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}
