package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/gmailtriage"
	"ok-gobot/internal/tools"
)

// gmailTriageCallback is the inline-button endpoint of digest cards. Callback
// data is "op|arg|arg"; see handleGmailTriageCallback.
const gmailTriageCallback = "gt"

const (
	gmailTriageRunTimeout = 20 * time.Minute
	gmailTriageEditTTL    = 30 * time.Minute
	gmailTriageSendPause  = 300 * time.Millisecond
	gmailTriageQuietPage  = 15 // keeps a list page under Telegram's 4096 characters
)

// pendingDraftEdit links an Edit prompt to the card whose draft it replaces.
type pendingDraftEdit struct {
	itemID   int64
	promptID int
	cardID   int
	expires  time.Time
}

type gmailTriageEdits struct {
	mu      sync.Mutex
	pending map[int64]pendingDraftEdit // by chat
}

// SetGmailTriage enables the email digest and registers the gmail_triage tool.
func (b *Bot) SetGmailTriage(svc *gmailtriage.Service) {
	if svc == nil {
		return
	}
	b.gmailTriage = svc
	b.gmailTriageEdits = &gmailTriageEdits{pending: map[int64]pendingDraftEdit{}}
	if b.toolRegistry != nil {
		b.toolRegistry.Register(tools.NewGmailTriageTool(b))
	}
}

// FireGmailTriage runs a scheduled digest; the cron entry calls it.
func (b *Bot) FireGmailTriage(p gmailtriage.Profile) {
	go func() {
		parent := b.ctx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, gmailTriageRunTimeout)
		defer cancel()
		summary, err := b.runGmailTriage(ctx, p, false)
		if err != nil {
			log.Printf("[gmail_triage] scheduled run for %s failed: %v", p.Name, err)
			return
		}
		log.Printf("[gmail_triage] scheduled run for %s: %s", p.Name, summary)
	}()
}

// GmailTriageToolCommand implements tools.GmailTriageController.
func (b *Bot) GmailTriageToolCommand(ctx context.Context, chatID int64, params map[string]string) (string, error) {
	if b.gmailTriage == nil {
		return "", errors.New("email triage is not configured")
	}
	p, err := b.gmailTriage.ProfileForChat(chatID)
	if err != nil {
		return "", errors.New("email triage is not set up for this chat")
	}
	runNow := func(ctx context.Context) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, gmailTriageRunTimeout)
		defer cancel()
		return b.runGmailTriage(ctx, p, true)
	}
	return b.gmailTriage.ToolCommand(ctx, p, params, runNow)
}

func (b *Bot) registerGmailTriageHandlers() {
	if b.gmailTriage == nil {
		return
	}
	b.api.Handle("/triage", b.guardUnauthorizedDM(false, b.handleTriageCommand))
	b.api.Handle(&telebot.InlineButton{Unique: gmailTriageCallback}, b.handleGmailTriageCallback)
}

func (b *Bot) handleTriageCommand(c telebot.Context) error {
	if c.Chat() == nil || c.Sender() == nil || !b.triageSenderAllowed(c.Sender().ID, c.Chat().ID) {
		return c.Send("🔒 Not authorized.")
	}
	p, err := b.gmailTriage.ProfileForChat(c.Chat().ID)
	if err != nil {
		return c.Send("Email triage is not set up for this chat.")
	}
	args := ""
	if c.Message() != nil {
		_, args = commandBody(c.Message().Text)
	}
	if strings.EqualFold(strings.TrimSpace(args), "status") {
		text, err := b.gmailTriage.StatusText(p)
		if err != nil {
			return c.Send("⚠️ " + err.Error())
		}
		return c.Send(text)
	}
	if err := c.Send(triageText(p, "📬 Checking mail…", "📬 Проверяю почту…")); err != nil {
		return err
	}
	parent := b.ctx
	if parent == nil {
		parent = context.Background()
	}
	go func() {
		ctx, cancel := context.WithTimeout(parent, gmailTriageRunTimeout)
		defer cancel()
		if _, err := b.runGmailTriage(ctx, p, true); err != nil {
			log.Printf("[gmail_triage] /triage for %s failed: %v", p.Name, err)
		}
	}()
	return nil
}

// triageSenderAllowed limits digest controls to authorized users and, in a
// private chat, to the chat owner.
func (b *Bot) triageSenderAllowed(senderID, chatID int64) bool {
	if chatID > 0 && senderID != chatID {
		return false
	}
	return b.authManager != nil && b.authManager.CheckAccess(senderID, chatID)
}

func triageText(p gmailtriage.Profile, en, ru string) string {
	if p.Language == "ru" {
		return ru
	}
	return en
}

// runGmailTriage fetches, classifies and delivers one digest. Scheduled runs
// stay silent when nothing needs the user; manual runs always answer.
func (b *Bot) runGmailTriage(ctx context.Context, p gmailtriage.Profile, manual bool) (string, error) {
	chat := &telebot.Chat{ID: p.ChatID}
	// Delivery runs under the profile lock so overlapping runs cannot send
	// the same items twice.
	var delivered bool
	digest, err := b.gmailTriage.RunAndDeliver(ctx, p, func(d gmailtriage.Digest) error {
		if d.Empty() && !manual && len(d.Errors) == 0 {
			return nil
		}
		delivered = true
		return b.deliverGmailDigest(d)
	})
	if errors.Is(err, gmailtriage.ErrBusy) {
		if manual {
			_, _ = b.sendTriage(chat, triageText(p, "A mail check is already running.", "Проверка почты уже идёт."), nil)
		}
		return "a run is already in progress", nil
	}
	if err != nil {
		if !delivered {
			_, _ = b.sendTriage(chat, "⚠️ Gmail triage: "+escapeTriageHTML(err.Error()), nil)
		}
		return "", err
	}
	if !delivered {
		return fmt.Sprintf("nothing needs the user; %d quiet items held for the next digest", len(digest.Quiet)), nil
	}
	return fmt.Sprintf("digest delivered to the chat: %d items need the user, %d quiet, %d held for later", len(digest.Items), len(digest.Quiet), digest.Overflow), nil
}

func escapeTriageHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// sendTriage sends HTML and retries once on flood control.
func (b *Bot) sendTriage(chat *telebot.Chat, text string, markup *telebot.ReplyMarkup) (*telebot.Message, error) {
	opts := &telebot.SendOptions{ParseMode: telebot.ModeHTML, DisableWebPagePreview: true}
	if markup != nil {
		opts.ReplyMarkup = markup
	}
	msg, err := b.api.Send(chat, text, opts)
	var flood telebot.FloodError
	if errors.As(err, &flood) {
		time.Sleep(time.Duration(flood.RetryAfter+1) * time.Second)
		msg, err = b.api.Send(chat, text, opts)
	}
	if err != nil {
		log.Printf("[gmail_triage] send to chat %d failed: %v", chat.ID, err)
	}
	return msg, err
}

func (b *Bot) deliverGmailDigest(d gmailtriage.Digest) error {
	p := d.Profile
	store := b.gmailTriage.Store()
	chat := &telebot.Chat{ID: p.ChatID}

	digestID := time.Now().UnixNano()
	var header *telebot.ReplyMarkup
	if len(d.Quiet) > 0 {
		header = &telebot.ReplyMarkup{}
		ui := gmailtriage.Captions(p)
		header.Inline(header.Row(header.Data(ui.ShowQuiet, gmailTriageCallback, "q", strconv.FormatInt(digestID, 10))))
	}
	if _, err := b.sendTriage(chat, gmailtriage.HeaderText(d, time.Now()), header); err != nil {
		return err
	}
	for _, it := range d.Quiet {
		if err := store.MarkReported(it.ID, 0, digestID); err != nil {
			log.Printf("[gmail_triage] mark quiet item %d: %v", it.ID, err)
		}
	}
	for _, it := range d.Items {
		time.Sleep(gmailTriageSendPause)
		markup, err := b.gmailItemKeyboard(p, it)
		if err != nil {
			log.Printf("[gmail_triage] keyboard for item %d: %v", it.ID, err)
		}
		msg, err := b.sendTriage(chat, gmailtriage.CardText(p, it), markup)
		if err != nil {
			continue // stays new and comes back in the next digest
		}
		if err := store.MarkReported(it.ID, msg.ID, digestID); err != nil {
			log.Printf("[gmail_triage] mark item %d: %v", it.ID, err)
		}
	}
	return nil
}

// gmailItemKeyboard builds the buttons of a card. Trash and Send buttons each
// carry a fresh pending action; pressing one is the confirmation.
func (b *Bot) gmailItemKeyboard(p gmailtriage.Profile, it gmailtriage.Item) (*telebot.ReplyMarkup, error) {
	switch it.Status {
	case gmailtriage.StatusSent, gmailtriage.StatusTrashed, gmailtriage.StatusSkipped:
		return nil, nil
	}
	ui := gmailtriage.Captions(p)
	kb := &telebot.ReplyMarkup{}
	id := strconv.FormatInt(it.ID, 10)
	var rows []telebot.Row
	if !p.ReadOnly() && it.Bucket == gmailtriage.BucketReply && strings.TrimSpace(it.Draft) != "" {
		send, err := b.gmailTriage.NewAction(p, it, gmailtriage.ActionSend)
		if err != nil {
			return nil, err
		}
		rows = append(rows, kb.Row(
			kb.Data(ui.Send, gmailTriageCallback, "a", send.ID),
			kb.Data(ui.Edit, gmailTriageCallback, "e", id),
			kb.Data(ui.Skip, gmailTriageCallback, "k", id),
		))
	}
	second := []telebot.Btn{
		kb.Data(ui.Rebucket, gmailTriageCallback, "b", id),
		kb.Data(ui.Correct, gmailTriageCallback, "y", id),
	}
	if !p.ReadOnly() {
		trash, err := b.gmailTriage.NewAction(p, it, gmailtriage.ActionTrash)
		if err != nil {
			return nil, err
		}
		second = append(second, kb.Data(ui.Trash, gmailTriageCallback, "a", trash.ID))
	}
	rows = append(rows, kb.Row(second...))
	kb.Inline(rows...)
	return kb, nil
}

func (b *Bot) gmailBucketMenu(p gmailtriage.Profile, it gmailtriage.Item) *telebot.ReplyMarkup {
	ui := gmailtriage.Captions(p)
	kb := &telebot.ReplyMarkup{}
	id := strconv.FormatInt(it.ID, 10)
	var rows []telebot.Row
	var row []telebot.Btn
	for _, def := range p.Taxonomy.Buckets() {
		if def.Key == it.Bucket || def.Key == gmailtriage.BucketWaiting {
			continue
		}
		row = append(row, kb.Data(def.Emoji+" "+p.BucketName(def.Key), gmailTriageCallback, "s", id, def.Key))
		if len(row) == 2 {
			rows = append(rows, kb.Row(row...))
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, kb.Row(row...))
	}
	rows = append(rows, kb.Row(kb.Data(ui.Back, gmailTriageCallback, "c", id)))
	kb.Inline(rows...)
	return kb
}

func (b *Bot) gmailScopeMenu(p gmailtriage.Profile, it gmailtriage.Item, bucket string) *telebot.ReplyMarkup {
	ui := gmailtriage.Captions(p)
	kb := &telebot.ReplyMarkup{}
	id := strconv.FormatInt(it.ID, 10)
	rows := []telebot.Row{kb.Row(kb.Data(ui.OnlyThis, gmailTriageCallback, "r", id, bucket, "o"))}
	if it.Sender != "" {
		rows = append(rows, kb.Row(kb.Data(fmt.Sprintf(ui.Sender, it.Sender), gmailTriageCallback, "r", id, bucket, "s")))
		if _, domain, ok := strings.Cut(it.Sender, "@"); ok && !gmailtriage.IsFreemail(domain) {
			rows = append(rows, kb.Row(kb.Data(fmt.Sprintf(ui.Domain, domain), gmailTriageCallback, "r", id, bucket, "d")))
		}
	}
	rows = append(rows, kb.Row(kb.Data(ui.Back, gmailTriageCallback, "b", id)))
	kb.Inline(rows...)
	return kb
}

func (b *Bot) handleGmailTriageCallback(c telebot.Context) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Message.Chat == nil || c.Sender() == nil || b.gmailTriage == nil {
		return c.Respond()
	}
	chatID := cb.Message.Chat.ID
	p, err := b.gmailTriage.ProfileForChat(chatID)
	if err != nil || !b.triageSenderAllowed(c.Sender().ID, chatID) {
		return c.Respond(&telebot.CallbackResponse{Text: "Not available here."})
	}
	parent := b.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()

	parts := strings.Split(cb.Data, "|")
	alert := func(text string) error {
		return c.Respond(&telebot.CallbackResponse{Text: text, ShowAlert: true})
	}
	toast := func(text string) error { return c.Respond(&telebot.CallbackResponse{Text: text}) }
	itemArg := func(i int) (gmailtriage.Item, error) {
		if len(parts) <= i {
			return gmailtriage.Item{}, errors.New("bad button")
		}
		id, err := strconv.ParseInt(parts[i], 10, 64)
		if err != nil {
			return gmailtriage.Item{}, errors.New("bad button")
		}
		return b.gmailTriage.Store().Item(p.Name, id)
	}
	refreshCard := func(it gmailtriage.Item) error {
		markup, err := b.gmailItemKeyboard(p, it)
		if err != nil {
			return err
		}
		if markup == nil {
			markup = &telebot.ReplyMarkup{}
		}
		_, err = b.api.Edit(cb.Message, gmailtriage.CardText(p, it), &telebot.SendOptions{ParseMode: telebot.ModeHTML, DisableWebPagePreview: true, ReplyMarkup: markup})
		return err
	}

	switch parts[0] {
	case "a": // confirmed trash or send
		if len(parts) < 2 {
			return alert("bad button")
		}
		item, action, err := b.gmailTriage.Confirm(ctx, parts[1], chatID)
		if errors.Is(err, gmailtriage.ErrActionUnavailable) {
			return alert(triageText(p, "This button is outdated. Use the latest card.", "Кнопка устарела, используй последнюю карточку."))
		}
		if err != nil {
			log.Printf("[gmail_triage] action %s failed: %v", parts[1], err)
			return alert("⚠️ " + truncateTriage(err.Error(), 180))
		}
		log.Printf("[gmail_triage] confirmed %s for item %d (profile %s)", action.Kind, item.ID, p.Name)
		if err := refreshCard(item); err != nil {
			log.Printf("[gmail_triage] refresh card %d: %v", item.ID, err)
		}
		if action.Kind == gmailtriage.ActionSend {
			return toast(triageText(p, "Sent", "Отправлено"))
		}
		return toast(triageText(p, "Moved to Trash", "Перемещено в корзину"))
	case "e": // edit draft
		item, err := itemArg(1)
		if err != nil {
			return alert(err.Error())
		}
		ui := gmailtriage.Captions(p)
		prompt, err := b.api.Send(cb.Message.Chat, ui.EditPrompt, &telebot.SendOptions{
			ReplyTo:     cb.Message,
			ReplyMarkup: &telebot.ReplyMarkup{ForceReply: true, Placeholder: ui.EditPlaceholder},
		})
		if err != nil {
			return alert(err.Error())
		}
		b.gmailTriageEdits.mu.Lock()
		b.gmailTriageEdits.pending[chatID] = pendingDraftEdit{itemID: item.ID, promptID: prompt.ID, cardID: cb.Message.ID, expires: time.Now().Add(gmailTriageEditTTL)}
		b.gmailTriageEdits.mu.Unlock()
		return c.Respond()
	case "k": // skip
		item, err := itemArg(1)
		if err != nil {
			return alert(err.Error())
		}
		item, err = b.gmailTriage.Skip(p, item.ID)
		if err != nil {
			return alert(err.Error())
		}
		_ = refreshCard(item)
		return c.Respond()
	case "b": // bucket menu
		item, err := itemArg(1)
		if err != nil {
			return alert(err.Error())
		}
		if _, err := b.api.EditReplyMarkup(cb.Message, b.gmailBucketMenu(p, item)); err != nil {
			log.Printf("[gmail_triage] bucket menu: %v", err)
		}
		return c.Respond()
	case "s": // scope menu for a chosen bucket
		item, err := itemArg(1)
		if err != nil || len(parts) < 3 {
			return alert("bad button")
		}
		if _, ok := p.Taxonomy.Bucket(parts[2]); !ok {
			return alert("bad button")
		}
		_, _ = b.api.EditReplyMarkup(cb.Message, b.gmailScopeMenu(p, item, parts[2]))
		return c.Respond()
	case "r": // apply correction
		item, err := itemArg(1)
		if err != nil || len(parts) < 4 {
			return alert("bad button")
		}
		scope := map[string]string{"o": gmailtriage.ScopeOnce, "s": gmailtriage.ScopeSender, "d": gmailtriage.ScopeDomain}[parts[3]]
		item, err = b.gmailTriage.Recategorize(ctx, p, item.ID, parts[2], scope)
		if err != nil {
			return alert("⚠️ " + truncateTriage(err.Error(), 180))
		}
		if err := refreshCard(item); err != nil {
			log.Printf("[gmail_triage] refresh card %d: %v", item.ID, err)
		}
		return toast(triageText(p, "Got it, I'll remember", "Понял, запомнил"))
	case "y": // classification was right
		item, err := itemArg(1)
		if err != nil {
			return alert(err.Error())
		}
		if _, err := b.gmailTriage.ConfirmBucket(p, item.ID); err != nil {
			return alert(err.Error())
		}
		return toast(triageText(p, "Noted 👍", "Запомнил 👍"))
	case "c": // back to the card buttons
		item, err := itemArg(1)
		if err != nil {
			return alert(err.Error())
		}
		markup, err := b.gmailItemKeyboard(p, item)
		if err != nil {
			return alert(err.Error())
		}
		_, _ = b.api.EditReplyMarkup(cb.Message, markup)
		return c.Respond()
	case "v": // open a card from the quiet list
		item, err := itemArg(1)
		if err != nil {
			return alert(err.Error())
		}
		markup, err := b.gmailItemKeyboard(p, item)
		if err != nil {
			return alert(err.Error())
		}
		if msg, err := b.sendTriage(cb.Message.Chat, gmailtriage.CardText(p, item), markup); err == nil {
			_ = b.gmailTriage.Store().SetCardMessage(item.ID, msg.ID)
		}
		return c.Respond()
	case "q": // list quiet mail of a digest, one page at a time
		if len(parts) < 2 {
			return alert("bad button")
		}
		digestID, err := strconv.ParseInt(parts[1], 10, 64)
		offset := 0
		if len(parts) > 2 {
			offset, _ = strconv.Atoi(parts[2])
		}
		if err != nil || offset < 0 {
			return alert("bad button")
		}
		items, err := b.gmailTriage.Store().QuietItems(p.Name, p.Taxonomy.QuietBucket(), digestID, offset, gmailTriageQuietPage+1)
		if err != nil {
			return alert(err.Error())
		}
		if len(items) == 0 {
			return toast(triageText(p, "Nothing left there", "Там уже ничего нет"))
		}
		more := len(items) > gmailTriageQuietPage
		if more {
			items = items[:gmailTriageQuietPage]
		}
		kb := &telebot.ReplyMarkup{}
		var rows []telebot.Row
		var row []telebot.Btn
		for i, it := range items {
			row = append(row, kb.Data(strconv.Itoa(offset+i+1), gmailTriageCallback, "v", strconv.FormatInt(it.ID, 10)))
			if len(row) == 5 {
				rows = append(rows, kb.Row(row...))
				row = nil
			}
		}
		if len(row) > 0 {
			rows = append(rows, kb.Row(row...))
		}
		if more {
			ui := gmailtriage.Captions(p)
			rows = append(rows, kb.Row(kb.Data(ui.More, gmailTriageCallback, "q", parts[1], strconv.Itoa(offset+gmailTriageQuietPage))))
		}
		kb.Inline(rows...)
		_, _ = b.sendTriage(cb.Message.Chat, gmailtriage.QuietListText(p, items, offset+1), kb)
		return c.Respond()
	}
	return c.Respond()
}

func truncateTriage(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// handleGmailTriageReply consumes replies to digest messages: a reply to an
// Edit prompt replaces the draft; a reply to a card is a correction note. It
// reports whether the message was handled.
func (b *Bot) handleGmailTriageReply(ctx context.Context, c telebot.Context) (bool, error) {
	msg := c.Message()
	if b.gmailTriage == nil || msg == nil || msg.ReplyTo == nil || msg.Chat == nil || b.api == nil || b.api.Me == nil {
		return false, nil
	}
	if msg.ReplyTo.Sender == nil || msg.ReplyTo.Sender.ID != b.api.Me.ID || strings.HasPrefix(msg.Text, "/") {
		return false, nil
	}
	if msg.Sender == nil || !b.triageSenderAllowed(msg.Sender.ID, msg.Chat.ID) {
		return false, nil
	}
	p, err := b.gmailTriage.ProfileForChat(msg.Chat.ID)
	if err != nil {
		return false, nil
	}
	store := b.gmailTriage.Store()

	b.gmailTriageEdits.mu.Lock()
	edit, ok := b.gmailTriageEdits.pending[msg.Chat.ID]
	if ok && (edit.promptID != msg.ReplyTo.ID || time.Now().After(edit.expires)) {
		ok = false
	}
	if ok {
		delete(b.gmailTriageEdits.pending, msg.Chat.ID)
	}
	b.gmailTriageEdits.mu.Unlock()
	if ok {
		item, err := b.gmailTriage.UpdateDraft(p, edit.itemID, msg.Text)
		if err != nil {
			return true, c.Send("⚠️ " + err.Error())
		}
		markup, err := b.gmailItemKeyboard(p, item)
		if err != nil {
			return true, c.Send("⚠️ " + err.Error())
		}
		card, err := b.sendTriage(msg.Chat, gmailtriage.CardText(p, item), markup)
		if err != nil {
			return true, err
		}
		_ = store.SetCardMessage(item.ID, card.ID)
		// Only now retire the old card's buttons (UpdateDraft already
		// invalidated its actions); the new card carries the new draft.
		old := &telebot.StoredMessage{MessageID: strconv.Itoa(edit.cardID), ChatID: msg.Chat.ID}
		_, _ = b.api.EditReplyMarkup(old, &telebot.ReplyMarkup{})
		return true, nil
	}

	item, err := store.ItemByChatMessage(p.Name, msg.ReplyTo.ID)
	if err != nil {
		return false, nil
	}
	if _, err := b.gmailTriage.AddNote(p, item.ID, msg.Text); err != nil {
		return true, c.Send("⚠️ " + err.Error())
	}
	return true, b.dispatchAgentTurn(ctx, c, gmailTriageNotePrompt(p, item, msg.Text), nil)
}

// gmailTriageNotePrompt hands a correction note to the agent so it can turn
// "always ignore these" into a standing rule. Only the sender address and
// bucket travel; the email body stays out of the agent turn.
func gmailTriageNotePrompt(p gmailtriage.Profile, it gmailtriage.Item, note string) string {
	lang := "English"
	if p.Language == "ru" {
		lang = "Russian"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "The user replied to an email digest item (sender %s, current bucket %s).\n", it.Sender, it.Bucket)
	sb.WriteString("Their note is already saved as a classification example for similar mail.\n")
	sb.WriteString("If the note asks for a standing rule (for example \"always ignore these\" or \"anything from this company is sales\"), call the gmail_triage tool with action=rules, op=add and the bucket the user wants. Pick value by what the user named: this exact address; the domain (example.com) for a company; the local part on any domain (root@*) for \"from root\" and similar. The tool also re-sorts this item and other open ones the rule covers. Otherwise do not call tools.\n")
	fmt.Fprintf(&sb, "Then confirm in one or two short sentences in %s, saying exactly what the rule covers (from the tool reply).\n\nUser note:\n%s", lang, strings.TrimSpace(note))
	return sb.String()
}
