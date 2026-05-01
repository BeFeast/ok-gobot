package bot

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/bootstrap"
)

// handleSkillSuggestCommand handles /skill_suggest <job-id>.
// It is admin-only because generated drafts can contain private job context.
func (b *Bot) handleSkillSuggestCommand(c telebot.Context) error {
	senderID := int64(0)
	if sender := c.Sender(); sender != nil {
		senderID = sender.ID
	}
	if b.authManager == nil || !b.authManager.IsAdmin(senderID) {
		return c.Send("🔒 /skill_suggest is admin-only.")
	}

	jobID := strings.TrimSpace(c.Message().Payload)
	if jobID == "" {
		return c.Send("Usage: `/skill_suggest <job-id>`", &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}
	if b.store == nil {
		return c.Send("❌ Job storage is not available.")
	}
	soul := b.skillSuggestionSoulPath()
	if soul == "" {
		return c.Send("❌ Soul path is not configured.")
	}

	suggestion, err := bootstrap.SuggestSkillFromJob(soul, b.store, jobID)
	if suggestion != nil {
		return c.Send(formatSkillSuggestionTelegram(suggestion, err), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}
	if err != nil {
		return c.Send(fmt.Sprintf("❌ %v", err))
	}
	return c.Send("❌ Failed to generate skill draft.")
}

func (b *Bot) skillSuggestionSoulPath() string {
	if b == nil {
		return ""
	}
	if b.memory != nil && strings.TrimSpace(b.memory.BasePath) != "" {
		return b.memory.BasePath
	}
	if b.personality != nil && strings.TrimSpace(b.personality.BasePath) != "" {
		return b.personality.BasePath
	}
	return ""
}

func formatSkillSuggestionTelegram(suggestion *bootstrap.SkillSuggestion, err error) string {
	if suggestion == nil {
		return ""
	}
	relSkillFile := filepath.ToSlash(filepath.Join("skill-drafts", suggestion.DraftID, suggestion.SkillName, "SKILL.md"))
	relDraftDir := filepath.ToSlash(filepath.Join("skill-drafts", suggestion.DraftID, suggestion.SkillName))
	if suggestion.Unsafe || errors.Is(err, bootstrap.ErrSkillSuggestionUnsafe) {
		return fmt.Sprintf("⚠️ *Skill draft saved but audit failed*\nJob: `%s`\nDraft: `%s`\nAudit errors: %d\n\nNext: edit the draft, then run `ok-gobot skills audit <soul>/%s`.",
			suggestion.JobID, relSkillFile, bootstrap.CountAuditErrors(suggestion.AuditFindings), relDraftDir)
	}
	return fmt.Sprintf("✅ *Skill draft saved*\nJob: `%s`\nDraft: `%s`\nAudit: passed\n\nNext: review it, then install only after approval with `ok-gobot skills install <soul>/%s`.",
		suggestion.JobID, relSkillFile, relDraftDir)
}
