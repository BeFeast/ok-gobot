package gmailtriage

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Rendering produces Telegram HTML. Every value from an email is escaped.

func esc(s string) string { return html.EscapeString(s) }

// HeaderText summarizes a digest: counts per bucket, quiet mail, overflow.
func HeaderText(d Digest, now time.Time) string {
	p := d.Profile
	lang := p.Language
	var sb strings.Builder
	title := tr(lang, "Email digest", "Почта: digest")
	if d.FirstRun {
		n := p.InitialLookbackDays
		title = fmt.Sprintf(tr(lang, "Email digest · first run, last %d days", "Почта: digest · первый прогон, %d "+ruPlural(n, "день", "дня", "дней")), n)
	}
	fmt.Fprintf(&sb, "📬 <b>%s</b> · %s\n", esc(title), now.In(p.location()).Format("02.01 15:04"))

	counts := map[string]int{}
	for _, it := range d.Items {
		counts[it.Bucket]++
	}
	var parts []string
	for _, b := range p.Taxonomy.Buckets() {
		if b.CountOnly {
			continue
		}
		if n := counts[b.Key]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %s: %d", b.Emoji, esc(p.BucketName(b.Key)), n))
		}
	}
	if len(parts) == 0 {
		sb.WriteString(tr(lang, "Nothing needs you.", "Ничего не требует тебя."))
		sb.WriteString("\n")
	} else {
		sb.WriteString(strings.Join(parts, "\n"))
		sb.WriteString("\n")
	}
	if d.Overflow > 0 {
		fmt.Fprintf(&sb, tr(lang, "…%d more will come in the next digest.\n", "…ещё %d придут в следующем digest.\n"), d.Overflow)
	}
	if n := len(d.Quiet); n > 0 {
		quiet := p.Taxonomy.QuietBucket()
		def, _ := p.Taxonomy.Bucket(quiet)
		line := fmt.Sprintf("\n%s %s: %d", def.Emoji, esc(p.BucketName(quiet)), n)
		if archived := d.Archived(); archived > 0 {
			line += fmt.Sprintf(tr(lang, " (archived and marked read: %d)", " (заархивировал и пометил прочитанным: %d)"), archived)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
		for i, it := range d.Quiet {
			if i == 8 {
				fmt.Fprintf(&sb, tr(lang, "…and %d more\n", "…и ещё %d\n"), len(d.Quiet)-i)
				break
			}
			fmt.Fprintf(&sb, "• %s — %s\n", esc(truncate(it.SenderName, 40)), esc(truncate(it.Subject, 60)))
		}
	}
	if len(d.Errors) > 0 {
		fmt.Fprintf(&sb, "\n⚠️ %s %s", tr(lang, "Problems:", "Проблемы:"), esc(truncate(strings.Join(d.Errors, "; "), 300)))
	}
	return strings.TrimSpace(sb.String())
}

// CardText renders one attention item.
func CardText(p Profile, it Item) string {
	lang := p.Language
	def, _ := p.Taxonomy.Bucket(it.Bucket)
	var sb strings.Builder
	sender := it.SenderName
	if it.Sender != "" && !strings.EqualFold(it.SenderName, it.Sender) {
		sender = fmt.Sprintf("%s <%s>", it.SenderName, it.Sender)
	}
	fmt.Fprintf(&sb, "%s <b>%s</b> · %s\n", def.Emoji, esc(p.BucketName(it.Bucket)), esc(truncate(sender, 80)))
	if it.Subject != "" {
		fmt.Fprintf(&sb, "<i>%s</i>\n", esc(truncate(it.Subject, 140)))
	}
	why := it.Why
	if it.Uncertain {
		why += tr(lang, " (not sure)", " (не уверен)")
	}
	fmt.Fprintf(&sb, "%s %s", tr(lang, "Why:", "Почему:"), esc(why))
	if strings.TrimSpace(it.Draft) != "" && it.Bucket == BucketReply && !p.ReadOnly() {
		// Send goes to Reply-To when the sender set one; say so when it differs.
		if it.ReplyTo != "" && it.ReplyTo != it.Sender {
			fmt.Fprintf(&sb, "\n⚠️ %s %s (Reply-To)", tr(lang, "Reply goes to", "Ответ уйдёт на"), esc(it.ReplyTo))
		}
		fmt.Fprintf(&sb, "\n\n✏️ %s\n<blockquote>%s</blockquote>", tr(lang, "Draft:", "Черновик:"), esc(truncate(it.Draft, MaxDraftChars)))
	}
	switch it.Status {
	case StatusSent:
		sb.WriteString("\n\n✅ " + tr(lang, "Sent.", "Отправлено."))
	case StatusTrashed:
		sb.WriteString("\n\n🗑 " + tr(lang, "Moved to Trash.", "Перемещено в корзину."))
	case StatusSkipped:
		sb.WriteString("\n\n⏭ " + tr(lang, "Skipped.", "Пропущено."))
	}
	return sb.String()
}

// QuietListText lists count-only items so the user can rescue a mistake.
// first is the number of the first item on this page.
func QuietListText(p Profile, items []Item, first int) string {
	quiet := p.Taxonomy.QuietBucket()
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%s</b>\n", esc(p.BucketName(quiet)))
	for i, it := range items {
		fmt.Fprintf(&sb, "%d. %s — %s\n   <i>%s</i>\n", first+i, esc(truncate(it.SenderName, 40)), esc(truncate(it.Subject, 70)), esc(truncate(it.Why, 80)))
	}
	sb.WriteString(tr(p.Language, "\nTap a number to move it to another bucket.", "\nНажми номер, чтобы переложить письмо в другую корзину."))
	return sb.String()
}

// UI holds button captions in the profile language.
type UI struct {
	Send, Edit, Skip, Trash, Rebucket, Correct, ShowQuiet, Back, More string
	OnlyThis, Sender, Domain                                          string
	EditPrompt, EditPlaceholder                                       string
}

// Captions returns button captions for a profile.
func Captions(p Profile) UI {
	if p.Language == "ru" {
		return UI{
			Send: "✉️ Отправить", Edit: "✏️ Изменить", Skip: "⏭ Пропустить", Trash: "🗑 В корзину",
			Rebucket: "↺ Другая корзина", Correct: "👍 Верно", ShowQuiet: "Показать список", Back: "← Назад", More: "Ещё →",
			OnlyThis: "Только это письмо", Sender: "Всегда от %s", Domain: "Весь домен %s",
			EditPrompt: "Пришли новый текст черновика ответом на это сообщение.", EditPlaceholder: "Текст ответа",
		}
	}
	return UI{
		Send: "✉️ Send", Edit: "✏️ Edit", Skip: "⏭ Skip", Trash: "🗑 Trash",
		Rebucket: "↺ Other bucket", Correct: "👍 Correct", ShowQuiet: "Show list", Back: "← Back", More: "More →",
		OnlyThis: "Only this email", Sender: "Always from %s", Domain: "Whole domain %s",
		EditPrompt: "Reply to this message with the new draft text.", EditPlaceholder: "Reply text",
	}
}

// StatusText describes schedule, pause state and learning for a profile.
func (s *Service) StatusText(p Profile) (string, error) {
	lang := p.Language
	sched, overridden, err := s.Schedule(p)
	if err != nil {
		return "", err
	}
	paused, err := s.Paused(p)
	if err != nil {
		return "", err
	}
	rules, err := s.store.Rules(p.Name)
	if err != nil {
		return "", err
	}
	pending, err := s.store.NewItems(p.Name)
	if err != nil {
		return "", err
	}
	lastRun, err := s.store.LastRun(p.Name)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	mode := tr(lang, "read-only", "только чтение")
	if !p.ReadOnly() {
		mode = tr(lang, "assistant (labels, archives Bulk; trash and send only after your button)", "assistant (labels, архив Bulk; trash и send только по кнопке)")
	}
	fmt.Fprintf(&sb, "%s: %s\n", tr(lang, "Mode", "Режим"), mode)
	src := tr(lang, "config default", "дефолт из config")
	if overridden {
		src = tr(lang, "set in chat", "задано в чате")
	}
	fmt.Fprintf(&sb, "%s: %s (%s)\n", tr(lang, "Schedule", "Расписание"), sched.Describe(lang), src)
	if paused {
		sb.WriteString(tr(lang, "Scheduled digests: paused\n", "Digest по расписанию: на паузе\n"))
	} else if next := s.NextRun(p); !next.IsZero() {
		fmt.Fprintf(&sb, "%s: %s\n", tr(lang, "Next digest", "Следующий digest"), next.In(p.location()).Format("Mon 02.01 15:04"))
	}
	if lastRun.IsZero() {
		n := p.InitialLookbackDays
		fmt.Fprintf(&sb, tr(lang, "First run not done yet (will cover the last %d days)\n", "Первого прогона ещё не было (охватит %d "+ruPlural(n, "день", "дня", "дней")+")\n"), n)
	} else {
		fmt.Fprintf(&sb, "%s: %s\n", tr(lang, "Last check", "Последняя проверка"), lastRun.In(p.location()).Format("02.01 15:04"))
	}
	fmt.Fprintf(&sb, "%s: %d · %s: %d\n", tr(lang, "Rules", "Правил"), len(rules), tr(lang, "waiting for next digest", "ждут digest"), len(pending))
	return strings.TrimSpace(sb.String()), nil
}

// RulesText lists rules with their IDs.
func RulesText(p Profile, rules []Rule) string {
	if len(rules) == 0 {
		return tr(p.Language, "No rules yet.", "Правил пока нет.")
	}
	var sb strings.Builder
	for _, r := range rules {
		fmt.Fprintf(&sb, "#%d %s %s → %s", r.ID, r.Scope, r.Value, r.Bucket)
		if r.Note != "" {
			fmt.Fprintf(&sb, " (%s)", r.Note)
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}
