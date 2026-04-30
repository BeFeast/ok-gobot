package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/logger"
	"ok-gobot/internal/memory/curate"
)

// handleMemoryCurateCommand routes /memory_curate subcommands. All actions are
// admin-only: drafts can contain extracted private notes and even raw
// secret-like strings the audit blocked from apply, so non-admin reads must
// not be possible either.
//
// Subcommands:
//
//	/memory_curate                 — usage
//	/memory_curate draft [N]       — draft from the last N days (default 7)
//	/memory_curate range YYYY-MM-DD YYYY-MM-DD
//	/memory_curate list
//	/memory_curate show <id>       — alias: preview
//	/memory_curate apply <id> yes  — alias: approve; requires literal "yes"
//	/memory_curate reject <id> [notes]
//	/memory_curate delete <id> yes — requires literal "yes"
func (b *Bot) handleMemoryCurateCommand(c telebot.Context) error {
	args := strings.Fields(strings.TrimSpace(c.Message().Payload))
	senderID := int64(0)
	if sender := c.Sender(); sender != nil {
		senderID = sender.ID
	}
	if b.authManager == nil || !b.authManager.IsAdmin(senderID) {
		return c.Send("🔒 /memory_curate is admin-only.")
	}
	if len(args) == 0 {
		return c.Send(memoryCurateUsage(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}
	soul := ""
	if b.memory != nil {
		soul = b.memory.BasePath
	}
	if soul == "" {
		return c.Send("❌ Memory base path not configured.")
	}
	store := curate.NewDraftStore(soul)

	switch strings.ToLower(args[0]) {
	case "draft":
		days := 7
		if len(args) >= 2 {
			if n, err := parseDaysArg(args[1]); err == nil {
				days = n
			}
		}
		// Daily-note files are written from local time elsewhere in the bot,
		// so the default window must be in local time too — otherwise on
		// non-UTC hosts the scan misses today's note around UTC midnight.
		until := time.Now()
		since := until.AddDate(0, 0, -(days - 1))
		return runBotCurateDraft(c, soul, since, until)

	case "range":
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

	case "show", "preview":
		if len(args) < 2 {
			return c.Send("Usage: `/memory_curate preview <id>`", &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
		}
		if err := curate.ValidateDraftID(args[1]); err != nil {
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		d, err := store.Load(args[1])
		if err != nil {
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		audit := curate.AuditDraft(d)
		body := renderMemoryCuratePreview(d, audit)
		return sendChunked(c, body)

	case "apply", "approve":
		if len(args) < 3 || strings.ToLower(args[2]) != "yes" {
			return c.Send("Usage: `/memory_curate approve <id> yes`\n\n"+
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
		logMemoryCurateAudit("apply", c, d.ID, d.Status)
		return c.Send(fmt.Sprintf("✅ Applied draft `%s` to MEMORY.md.", d.ID),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})

	case "reject":
		if len(args) < 2 {
			return c.Send("Usage: `/memory_curate reject <id> [notes]`",
				&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
		}
		notes := ""
		if len(args) > 2 {
			notes = strings.Join(args[2:], " ")
		}
		label := senderLabel(c)
		if notes == "" {
			notes = "rejected by " + label
		} else {
			notes = "rejected by " + label + ": " + notes
		}
		d, err := store.SetStatus(args[1], curate.StatusRejected, notes)
		if err != nil {
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		logMemoryCurateAudit("reject", c, d.ID, d.Status)
		return c.Send(fmt.Sprintf("Draft `%s` rejected. Use `/memory_curate delete %s yes` to remove from disk.",
			d.ID, d.ID), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})

	case "delete":
		if len(args) < 3 || strings.ToLower(args[2]) != "yes" {
			return c.Send("Usage: `/memory_curate delete <id> yes`\n\n"+
				"The literal `yes` is required as explicit confirmation that you want "+
				"to remove the draft files.",
				&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
		}
		d, err := store.Load(args[1])
		if err != nil {
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		if err := store.Delete(args[1]); err != nil {
			return c.Send(fmt.Sprintf("❌ %v", err))
		}
		logMemoryCurateAudit("delete", c, d.ID, d.Status)
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
		"`/memory_curate preview <id>` — show candidates, source dates, and audit findings\n" +
		"`/memory_curate approve <id> yes` — apply to MEMORY.md (admin, explicit confirm)\n" +
		"`/memory_curate reject <id> [notes]` — reject but keep on disk (admin)\n" +
		"`/memory_curate delete <id> yes` — remove from disk (admin, explicit confirm)\n"
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
	body := renderMemoryCuratePreview(draft, audit)
	if err := sendChunked(c, body); err != nil {
		return err
	}
	return c.Send(fmt.Sprintf("Apply with `/memory_curate approve %s yes` (admin).", draft.ID),
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
	for _, chunk := range splitTelegramChunks(body, limit) {
		if err := c.Send(chunk); err != nil {
			return err
		}
	}
	return nil
}

func splitTelegramChunks(body string, limit int) []string {
	if limit <= 0 || strings.TrimSpace(body) == "" {
		return nil
	}

	var chunks []string
	for len([]rune(body)) > limit {
		runes := []rune(body)
		cut := limit
		windowStart := limit - 300
		if windowStart < 0 {
			windowStart = 0
		}
		for i := limit; i > windowStart; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		chunk := strings.TrimRight(string(runes[:cut]), "\n")
		if strings.TrimSpace(chunk) != "" {
			chunks = append(chunks, chunk)
		}
		body = strings.TrimLeft(string(runes[cut:]), "\n")
	}
	if strings.TrimSpace(body) != "" {
		chunks = append(chunks, body)
	}
	return chunks
}

func renderMemoryCuratePreview(d *curate.Draft, audit curate.AuditReport) string {
	if d == nil {
		return ""
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Memory curation draft `%s`\n\n", d.ID)
	fmt.Fprintf(&sb, "Status: `%s`\n", d.Status)
	fmt.Fprintf(&sb, "Source dates: %s -> %s (%d notes)\n", d.Since, d.Until, d.SourceCount)
	fmt.Fprintf(&sb, "Candidates: %d\n", len(d.Candidates))
	if d.Notes != "" {
		fmt.Fprintf(&sb, "Reviewer notes: %s\n", compactLine(d.Notes, 240))
	}
	sb.WriteString("\nCandidates:\n")
	if d.IsEmpty() {
		sb.WriteString("- none\n")
	} else {
		grouped := d.CandidatesBySection()
		for _, section := range curate.AllSections() {
			candidates := grouped[section]
			if len(candidates) == 0 {
				continue
			}
			fmt.Fprintf(&sb, "\n%s:\n", curate.SectionTitle(section))
			for _, candidate := range candidates {
				fmt.Fprintf(&sb, "- `%s` [%s, conf %.2f] %s\n", candidate.ID, candidate.Section, candidate.Confidence, compactLine(candidate.Text, 280))
				fmt.Fprintf(&sb, "  source dates: %s\n", strings.Join(candidateSourceDates(candidate), ", "))
				if len(candidate.Conflicts) > 0 {
					fmt.Fprintf(&sb, "  risk: conflicts with %s\n", strings.Join(candidate.Conflicts, ", "))
				}
			}
		}
	}

	sb.WriteString("\nAudit findings:\n")
	if len(audit.Findings) == 0 {
		sb.WriteString("- none\n")
	} else {
		for _, finding := range audit.Findings {
			fmt.Fprintf(&sb, "- %s\n", compactLine(finding.String(), 240))
		}
	}

	fmt.Fprintf(&sb, "\nApprove with `/memory_curate approve %s yes`.\n", d.ID)
	fmt.Fprintf(&sb, "Reject with `/memory_curate reject %s [notes]`. Delete with `/memory_curate delete %s yes`.\n", d.ID, d.ID)
	return sb.String()
}

func candidateSourceDates(candidate curate.Candidate) []string {
	seen := make(map[string]bool)
	var dates []string
	for _, source := range candidate.Sources {
		date := strings.TrimSpace(source.Date)
		if date == "" {
			date = dateFromSourcePath(source.Path)
		}
		if date == "" || seen[date] {
			continue
		}
		seen[date] = true
		dates = append(dates, date)
	}
	if len(dates) == 0 {
		return []string{"unknown"}
	}
	return dates
}

func dateFromSourcePath(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		path = path[idx+1:]
	}
	path = strings.TrimSuffix(path, ".md")
	if len(path) == len("2006-01-02") && path[4] == '-' && path[7] == '-' {
		return path
	}
	return ""
}

func compactLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if limit <= 3 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit-3]) + "..."
}

type memoryCurateAuditEntry struct {
	Timestamp int64  `json:"ts"`
	Action    string `json:"action"`
	DraftID   string `json:"draft_id"`
	Status    string `json:"status"`
	SenderID  int64  `json:"sender_id"`
	Username  string `json:"username"`
	ChatID    int64  `json:"chat_id"`
	ChatType  string `json:"chat_type"`
}

func logMemoryCurateAudit(action string, c telebot.Context, draftID string, status curate.Status) {
	entry := memoryCurateAuditEntry{
		Timestamp: time.Now().Unix(),
		Action:    action,
		DraftID:   draftID,
		Status:    string(status),
	}
	if sender := c.Sender(); sender != nil {
		entry.SenderID = sender.ID
		entry.Username = sender.Username
	}
	if chat := c.Chat(); chat != nil {
		entry.ChatID = chat.ID
		entry.ChatType = string(chat.Type)
	}
	data, _ := json.Marshal(entry)
	logger.Infof("[AUDIT] memory_curate %s", data)
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
