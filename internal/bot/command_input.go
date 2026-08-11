package bot

import (
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/videosummary"
	"ok-gobot/internal/youtubekaraoke"
)

const commandInputTTL = 5 * time.Minute

type commandInputKind string

const (
	commandInputVideoSummary   commandInputKind = "video_summary"
	commandInputYouTubeKaraoke commandInputKind = "youtube_karaoke"
)

type commandInputKey struct {
	chatID int64
	userID int64
}

type pendingCommandInput struct {
	kind      commandInputKind
	expiresAt time.Time
}

type commandInputSpec struct {
	prompt      string
	placeholder string
}

func commandInputSpecFor(kind commandInputKind) commandInputSpec {
	switch kind {
	case commandInputVideoSummary:
		return commandInputSpec{
			prompt:      "Send the YouTube URL to summarize.",
			placeholder: "Paste a YouTube URL",
		}
	case commandInputYouTubeKaraoke:
		return commandInputSpec{
			prompt:      "Send the YouTube URL for karaoke.",
			placeholder: "Paste a YouTube URL",
		}
	default:
		return commandInputSpec{}
	}
}

func commandInputKeyForContext(c telebot.Context) (commandInputKey, bool) {
	if c == nil || c.Chat() == nil || c.Sender() == nil {
		return commandInputKey{}, false
	}
	return commandInputKey{chatID: c.Chat().ID, userID: c.Sender().ID}, true
}

func (b *Bot) promptForCommandInput(c telebot.Context, kind commandInputKind) error {
	key, ok := commandInputKeyForContext(c)
	if !ok || c.Message() == nil {
		return c.Send("Unable to start guided input. Send the command with its argument instead.")
	}

	spec := commandInputSpecFor(kind)
	options := &telebot.SendOptions{
		ReplyTo: c.Message(),
		ReplyMarkup: &telebot.ReplyMarkup{
			ForceReply:  true,
			Selective:   true,
			Placeholder: spec.placeholder,
		},
	}
	if err := c.Send(spec.prompt, options); err != nil {
		return err
	}

	b.commandInputMu.Lock()
	if b.pendingCommandInputs == nil {
		b.pendingCommandInputs = make(map[commandInputKey]pendingCommandInput)
	}
	b.pendingCommandInputs[key] = pendingCommandInput{
		kind:      kind,
		expiresAt: time.Now().Add(commandInputTTL),
	}
	b.commandInputMu.Unlock()
	return nil
}

func (b *Bot) clearPendingCommandInput(c telebot.Context) {
	key, ok := commandInputKeyForContext(c)
	if !ok {
		return
	}
	b.commandInputMu.Lock()
	delete(b.pendingCommandInputs, key)
	b.commandInputMu.Unlock()
}

func (b *Bot) takePendingCommandInput(key commandInputKey, now time.Time) (pendingCommandInput, bool) {
	b.commandInputMu.Lock()
	defer b.commandInputMu.Unlock()

	pending, ok := b.pendingCommandInputs[key]
	if !ok {
		return pendingCommandInput{}, false
	}
	delete(b.pendingCommandInputs, key)
	if !now.Before(pending.expiresAt) {
		return pendingCommandInput{}, false
	}
	return pending, true
}

func (b *Bot) handlePendingCommandInput(c telebot.Context) (bool, error) {
	key, ok := commandInputKeyForContext(c)
	if !ok || c.Message() == nil {
		return false, nil
	}
	pending, ok := b.takePendingCommandInput(key, time.Now())
	if !ok {
		return false, nil
	}

	rawInput := strings.TrimSpace(c.Message().Text)
	valid := false
	switch pending.kind {
	case commandInputVideoSummary:
		valid = videosummary.ValidateYouTubeURL(rawInput) == nil
	case commandInputYouTubeKaraoke:
		valid = youtubekaraoke.ValidateYouTubeURL(rawInput) == nil
	}
	if !valid {
		return true, b.promptForCommandInput(c, pending.kind)
	}

	originalPayload := c.Message().Payload
	c.Message().Payload = rawInput
	defer func() {
		c.Message().Payload = originalPayload
	}()

	switch pending.kind {
	case commandInputVideoSummary:
		return true, b.handleVideoSummaryCommand(c)
	case commandInputYouTubeKaraoke:
		return true, b.handleYouTubeKaraokeCommand(c)
	default:
		return false, nil
	}
}
