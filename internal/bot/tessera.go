package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v4"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tessera"
	"ok-gobot/internal/tools"
)

func (b *Bot) SetTessera(config tessera.Config) error {
	if !config.Enabled {
		return nil
	}
	if b.api == nil || b.api.Me == nil || config.AccountID != strconv.FormatInt(b.api.Me.ID, 10) {
		return errors.New("Tessera account_id must match the Telegram bot account returned by getMe")
	}
	client, err := tessera.NewClient(config)
	if err != nil {
		return err
	}
	b.tessera, err = tessera.NewCoordinator(config, b.store, client)
	if err != nil {
		return err
	}
	if b.toolRegistry != nil {
		for _, op := range []string{"inbox_capture", "inbox_list", "inbox_get", "attention_list", "attention_get", "attention_reply", "attention_ack"} {
			b.toolRegistry.Register(&tools.TesseraTool{Coordinator: b.tessera, Op: op})
		}
	}
	return nil
}
func telegramTurn(c telebot.Context) *tessera.Turn {
	m := c.Message()
	if m == nil || m.Chat == nil || m.Sender == nil || m.Sender.IsBot || m.SenderChat != nil {
		return nil
	}
	t := tessera.Telegram{SenderID: strconv.FormatInt(m.Sender.ID, 10), ChatID: strconv.FormatInt(m.Chat.ID, 10), MessageID: strconv.Itoa(m.ID), UpdateID: strconv.Itoa(c.Update().ID)}
	if m.ThreadID != 0 {
		topic := strconv.Itoa(m.ThreadID)
		t.TopicID = &topic
	}
	return &tessera.Turn{Telegram: t, Content: m.Text}
}
func (b *Bot) registerTesseraHandlers(ctx context.Context) {
	if b.tessera == nil {
		return
	}
	for _, command := range []string{"/capture", "/inbox", "/attention", "/seen", "/tessera_retry"} {
		b.api.Handle(command, b.guardUnauthorizedDM(false, func(c telebot.Context) error { _, err := b.handleTesseraMessage(ctx, c); return err }))
	}
	cfg := b.tessera.Config()
	if cfg.PollSeconds == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.PollSeconds) * time.Second)
		defer ticker.Stop()
		for {
			for _, route := range cfg.Routes {
				t := tessera.Telegram{SenderID: cfg.SenderID, ChatID: route.ChatID, TopicID: route.TopicID}
				if _, err := b.queueTesseraAttention(ctx, t); err != nil {
					log.Printf("[tessera] attention poll failed: %v", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func commandBody(text string) (string, string) {
	i := strings.IndexAny(text, " \t\r\n")
	if i < 0 {
		return strings.SplitN(text, "@", 2)[0], ""
	}
	return strings.SplitN(text[:i], "@", 2)[0], text[i+1:]
}
func (b *Bot) tesseraResult(c telebot.Context, err error, success string) error {
	if err != nil {
		return c.Send("Tessera: "+err.Error()+"\nYour original message is unchanged. Use /tessera_retry for an uncertain delivery; stale targets require a fresh /attention item.", &telebot.SendOptions{ParseMode: telebot.ModeDefault})
	}
	return c.Send(success)
}
func (b *Bot) handleTesseraMessage(ctx context.Context, c telebot.Context) (bool, error) {
	if b.tessera == nil {
		return false, nil
	}
	turn := telegramTurn(c)
	if turn == nil {
		return false, nil
	}
	t := turn.Telegram
	m := c.Message()
	cmd, body := commandBody(m.Text)
	// An observed successful send is the only source of a reply target. An
	// unbound Tessera prompt must not fall through to provider inference.
	if (b.tessera.Config().Authorize(t, false) == nil || (m.ReplyTo != nil && strings.Contains(m.ReplyTo.Text, "Tessera · Attention"))) && m.ReplyTo != nil && m.ReplyTo.Sender != nil && b.api.Me != nil && m.ReplyTo.Sender.ID == b.api.Me.ID {
		seen := cmd == "/seen"
		_, err := b.tessera.BoundReply(ctx, t, int64(m.ReplyTo.ID), m.Text, seen)
		if !tessera.IsUnbound(err) {
			return true, b.tesseraResult(c, err, replySuccess(seen))
		}
		if strings.Contains(m.ReplyTo.Text, "Tessera · Attention") {
			return true, b.tesseraResult(c, errors.New("this attention message has no known delivery binding; open /attention again"), "")
		}
	}
	switch cmd {
	case "/capture":
		_, err := b.tessera.Mutate(ctx, t, "command-capture", map[string]any{"op": "inbox_capture", "text": body})
		return true, b.tesseraResult(c, err, "Saved in Tessera Inbox.")
	case "/tessera_retry":
		results, err := b.tessera.RetryPending(ctx, t)
		return true, b.tesseraResult(c, err, fmt.Sprintf("Recovered %d retained Tessera deliveries.", len(results)))
	case "/seen":
		return true, b.tesseraResult(c, errors.New("reply /seen to the exact Tessera attention message"), "")
	case "/attention":
		if body != "" {
			fields := strings.Fields(body)
			if len(fields) != 3 {
				return true, b.tesseraResult(c, errors.New("use the exact Details command from the notification"), "")
			}
			r, err := b.tessera.Read(ctx, t, map[string]any{"op": "attention_get", "goal_id": fields[0], "attention_id": fields[1], "revision": fields[2]})
			if err != nil {
				return true, b.tesseraResult(c, err, "")
			}
			var item tessera.Item
			if err = json.Unmarshal(r, &item); err != nil {
				return true, b.tesseraResult(c, err, "")
			}
			return true, sendTesseraPlain(c, item.GoalTitle+"\n\n"+item.Message)
		}
		count, err := b.queueTesseraAttention(ctx, t)
		return true, b.tesseraResult(c, err, fmt.Sprintf("Attention checked: %d current items. New blockers, decisions and results will arrive through the delivery queue.", count))
	case "/inbox":
		command := map[string]any{"op": "inbox_list", "limit": 20}
		if body != "" {
			command = map[string]any{"op": "inbox_get", "capture_id": strings.TrimSpace(body)}
		}
		r, err := b.tessera.Read(ctx, t, command)
		if err != nil {
			return true, b.tesseraResult(c, err, "")
		}
		if body != "" {
			var result struct {
				Item tessera.InboxItem `json:"item"`
				Text string            `json:"text"`
			}
			if err = json.Unmarshal(r, &result); err != nil {
				return true, b.tesseraResult(c, err, "")
			}
			return true, sendTesseraPlain(c, result.Item.Title+"\n\n"+result.Text)
		}
		var page tessera.InboxPage
		if err = json.Unmarshal(r, &page); err != nil {
			return true, b.tesseraResult(c, err, "")
		}
		text := "Tessera · Inbox"
		for _, item := range page.Items {
			text += "\n\n" + item.Title + "\n/inbox " + item.CaptureID
		}
		if !page.Complete {
			text += "\n\nMore items available in Tessera."
		}
		return true, sendTesseraPlain(c, text)
	}
	handled, err := b.tessera.ResumeTools(ctx, t)
	if handled {
		return true, b.tesseraResult(c, err, "This Telegram update already has a retained Tessera action; its original delivery was recovered.")
	}
	return false, nil
}
func replySuccess(seen bool) string {
	if seen {
		return "Marked this Tessera revision seen. The goal is unchanged."
	}
	return "Saved as unverified decision knowledge in Tessera. The goal is unchanged."
}
func sendTesseraPlain(c telebot.Context, text string) error {
	r := []rune(text)
	for len(r) > 0 {
		n := 1400
		if n > len(r) {
			n = len(r)
		}
		if err := c.Send(string(r[:n]), &telebot.SendOptions{ParseMode: telebot.ModeDefault}); err != nil {
			return err
		}
		r = r[n:]
	}
	return nil
}
func (b *Bot) queueTesseraAttention(ctx context.Context, t tessera.Telegram) (int, error) {
	count := 0
	cursor := ""
	seen := map[string]bool{}
	for {
		command := map[string]any{"op": "attention_list", "limit": 50}
		if cursor != "" {
			command["cursor"] = cursor
		}
		r, err := b.tessera.Read(ctx, t, command)
		if err != nil {
			return count, err
		}
		var page tessera.AttentionPage
		if err = json.Unmarshal(r, &page); err != nil {
			return count, err
		}
		for _, item := range page.Items {
			if _, err = b.tessera.EnqueueAttention(t, item); err != nil {
				return count, err
			}
			count++
		}
		if page.Complete {
			return count, nil
		}
		if page.NextCursor == nil || *page.NextCursor == "" || seen[*page.NextCursor] {
			return count, errors.New("Tessera returned a nonadvancing attention cursor")
		}
		cursor = *page.NextCursor
		seen[cursor] = true
	}
}
func (b *Bot) sendTesseraOutbox(msg storage.OutboxMessage, text string) (*telebot.Message, error) {
	var meta tessera.DeliveryMetadata
	if b.tessera == nil || json.Unmarshal([]byte(msg.DeliveryMetadata), &meta) != nil || meta.Kind != "tessera-attention" || meta.Fingerprint != b.tessera.Fingerprint() {
		return nil, errors.New("retained Tessera delivery does not match the active connector")
	}
	cfg := b.tessera.Config()
	t := tessera.Telegram{SenderID: cfg.SenderID, ChatID: strconv.FormatInt(msg.ChatID, 10)}
	if meta.TopicID != 0 {
		v := strconv.FormatInt(meta.TopicID, 10)
		t.TopicID = &v
	}
	if err := cfg.Authorize(t, false); err != nil {
		return nil, err
	}
	options := &telebot.SendOptions{ParseMode: telebot.ModeDefault}
	if meta.ForceReply {
		options.ReplyMarkup = &telebot.ReplyMarkup{ForceReply: true}
	}
	if meta.TopicID != 0 {
		return b.api.Send(telebot.ChatID(msg.ChatID), text, options, &telebot.Topic{ThreadID: int(meta.TopicID)})
	}
	return b.api.Send(telebot.ChatID(msg.ChatID), text, options)
}

func tesseraRunContext(ctx context.Context, d telegramDelivery, content string, media bool) context.Context {
	ctx = tessera.WithTurn(ctx, nil)
	if d.Turn != nil && d.Turn.Content == content && !media {
		ctx = tessera.WithTurn(ctx, d.Turn)
	}
	return ctx
}
