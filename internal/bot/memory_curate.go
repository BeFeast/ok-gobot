package bot

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/memory/curate"
)

// handleMemoryCurateCommand routes /memory_curate subcommands. Most actions
// require admin privileges because they generate or apply changes that
// eventually land in MEMORY.md.
//
// Subcommands:
//
//	/memory_curate                 — usage
//	/memory_curate draft [N]       — draft from the last N days (default 7)
//	/memory_curate range YYYY-MM-DD YYYY-MM-DD
//	/memory_curate list
//	/memory_curate show <id>
//	/memory_curate apply <id> yes  — admin only, requires the literal "yes"
//	/memory_curate reject <id> [notes]
//	/memory_curate delete <id>
func (b *Bot) handleMemoryCurateCommand(c telebot.Context) error {
	args := strings.Fields(strings.TrimSpace(c.Message().Payload))
	if len(args) == 0 {
		return c.Send(memoryCurateUsage(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	soul := b.memory.BasePath
	if soul == "" {
		return c.Send("❌ Memory base path not configured.")
	}
	store := curate.NewDraftStore(soul)
	isAdmin := b.authManager.IsAdmin(c.Sender().ID)

	switch strings.ToLower(args[0]) {
	case "draft":
		if !isAdmin {
			return c.Send("🔒 /memory_curate draft is admin-only.")
		}
		days := 7
		if len(args) >= 2 {
			if n, err := parseDaysArg(args[1]); err == nil {
				days = n
			}
		}
		until := time.Now().UTC()
		since := until.AddDate(0, 0, -(days - 1))
		return runBotCurateDraft(c, soul, since, until)

	case "range":
		if !isAdmin {
			return c.Send("🔒 /memory_curate range is admin-only.")
		}
		if len(args) < 3 {
			return c.Send("Usage: `/memory_curate range YYYY-MM-DD YYYY-MM-DD`",
				&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
		}
		since, err := curate.ParseDate(args[1])
		if err != nil {
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		until, err := curate.ParseDate(args[2])
		if err != nil {
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		return runBotCurateDraft(c, soul, since, until)

	case "list":
		summaries, err := store.List()
		if err != nil {
			return c.Send(fmt.Sprintf("❌ Failed to list drafts: %v", err))
		}
		if len(summaries) == 0 {
			return c.Send("No curation drafts found.")
		}
		var sb strings.Builder
		sb.WriteString("*Curation drafts:*\n\n")
		for _, s := range summaries {
			fmt.Fprintf(&sb, "`%s` [%s] %s→%s — %d cand, %d conflicts\n",
				s.ID, s.Status, s.Since, s.Until, s.Candidates, s.Conflicts)
		}
		return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})

	case "show":
		if len(args) < 2 {
			return c.Send("Usage: `/memory_curate show <id>`", &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
		}
		d, err := store.Load(args[1])
		if err != nil {
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		audit := curate.AuditDraft(d)
		body := curate.RenderDraftMarkdown(d, audit)
		return sendChunked(c, body)

	case "apply":
		if !isAdmin {
			return c.Send("🔒 /memory_curate apply is admin-only.")
		}
		if len(args) < 3 || strings.ToLower(args[2]) != "yes" {
			return c.Send("Usage: `/memory_curate apply <id> yes`\n\n"+
				"The literal `yes` is required as explicit confirmation that you want "+
				"to modify MEMORY.md.",
				&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
		}
		label := senderLabel(c)
		d, audit, err := curate.Apply(soul, store, args[1], curate.ApplyOptions{
			Approved:   true,
			AdminLabel: label,
		})
		switch {
		case errors.Is(err, curate.ErrAuditBlocked):
			var sb strings.Builder
			sb.WriteString("❌ Apply blocked by audit. Findings:\n")
			for _, f := range audit.Findings {
				if f.Severity == curate.AuditError {
					fmt.Fprintf(&sb, "- %s\n", f.String())
				}
			}
			return c.Send(sb.String())
		case errors.Is(err, curate.ErrEmptyDraft):
			return c.Send("ℹ️ Nothing to promote: draft has no candidates.")
		case errors.Is(err, curate.ErrAlreadyApplied):
			return c.Send("ℹ️ Draft was already applied.")
		case err != nil:
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		return c.Send(fmt.Sprintf("✅ Applied draft `%s` to MEMORY.md.", d.ID),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})

	case "reject":
		if !isAdmin {
			return c.Send("🔒 /memory_curate reject is admin-only.")
		}
		if len(args) < 2 {
			return c.Send("Usage: `/memory_curate reject <id> [notes]`",
				&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
		}
		notes := ""
		if len(args) > 2 {
			notes = strings.Join(args[2:], " ")
		}
		d, err := store.SetStatus(args[1], curate.StatusRejected, notes)
		if err != nil {
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		return c.Send(fmt.Sprintf("Draft `%s` rejected. Use `/memory_curate delete %s` to remove from disk.",
			d.ID, d.ID), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})

	case "delete":
		if !isAdmin {
			return c.Send("🔒 /memory_curate delete is admin-only.")
		}
		if len(args) < 2 {
			return c.Send("Usage: `/memory_curate delete <id>`",
				&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
		}
		if err := store.Delete(args[1]); err != nil {
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		return c.Send(fmt.Sprintf("Draft `%s` deleted.", args[1]),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})

	default:
		return c.Send(memoryCurateUsage(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}
}

func memoryCurateUsage() string {
	return "*Memory curation*\n\n" +
		"`/memory_curate draft [days]` — draft from last N days (default 7, admin)\n" +
		"`/memory_curate range YYYY-MM-DD YYYY-MM-DD` — draft from a date range (admin)\n" +
		"`/memory_curate list` — list drafts\n" +
		"`/memory_curate show <id>` — show full draft + audit\n" +
		"`/memory_curate apply <id> yes` — apply to MEMORY.md (admin, explicit confirm)\n" +
		"`/memory_curate reject <id> [notes]` — reject but keep on disk (admin)\n" +
		"`/memory_curate delete <id>` — remove from disk (admin)\n"
}

func runBotCurateDraft(c telebot.Context, soul string, since, until time.Time) error {
	curator := curate.NewCurator(soul)
	draft, err := curator.CurateRange(since, until)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ %v", err))
	}
	store := curate.NewDraftStore(soul)
	if err := store.Save(draft); err != nil {
		return c.Send(fmt.Sprintf("❌ Failed to save draft: %v", err))
	}
	if draft.IsEmpty() {
		return c.Send(fmt.Sprintf("ℹ️ Draft `%s` has no useful candidates from %s → %s.",
			draft.ID, draft.Since, draft.Until),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}
	audit := curate.AuditDraft(draft)
	body := curate.RenderDraftMarkdown(draft, audit)
	if err := sendChunked(c, body); err != nil {
		return err
	}
	return c.Send(fmt.Sprintf("Apply with `/memory_curate apply %s yes` (admin).", draft.ID),
		&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// parseDaysArg accepts "7" or "7d" and returns the integer day count, clamped
// to a reasonable window so the command cannot accidentally scan years of
// history.
func parseDaysArg(s string) (int, error) {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "d")
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	if n < 1 {
		n = 1
	}
	if n > 90 {
		n = 90
	}
	return n, nil
}

// sendChunked sends a long body to Telegram, splitting on chunk boundaries to
// stay under the per-message limit.
func sendChunked(c telebot.Context, body string) error {
	const limit = 3500
	for len(body) > limit {
		// Try to break on a newline within the last 200 chars of the limit.
		cut := limit
		if idx := strings.LastIndex(body[:limit], "\n"); idx > limit-300 {
			cut = idx
		}
		if err := c.Send(body[:cut]); err != nil {
			return err
		}
		body = body[cut:]
	}
	if strings.TrimSpace(body) != "" {
		return c.Send(body)
	}
	return nil
}

func senderLabel(c telebot.Context) string {
	s := c.Sender()
	if s == nil {
		return "telegram"
	}
	if s.Username != "" {
		return "@" + s.Username
	}
	return fmt.Sprintf("tg:%d", s.ID)
}
