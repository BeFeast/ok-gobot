package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/videosummary"
	"ok-gobot/internal/youtubekaraoke"
)

const commandInputTTL = 5 * time.Minute

type commandInputKind string

const (
	commandInputVideoSummary   commandInputKind = "video_summary"
	commandInputYouTubeKaraoke commandInputKind = "youtube_karaoke"
	commandInputSkill          commandInputKind = "skill"
)

type commandInputKey struct {
	chatID int64
	userID int64
}

type pendingCommandInput struct {
	kind      commandInputKind
	expiresAt time.Time
	skill     bootstrap.SkillCommand // set when kind == commandInputSkill
}

type commandInputSpec struct {
	prompt      string
	placeholder string
}

func commandInputSpecFor(kind commandInputKind) commandInputSpec {
	return commandInputSpecForPending(pendingCommandInput{kind: kind})
}

func commandInputSpecForPending(pending pendingCommandInput) commandInputSpec {
	switch pending.kind {
	case commandInputSkill:
		return skillCommandInputSpec(pending.skill)
	case commandInputVideoSummary:
		return commandInputSpec{
			prompt:      "Send the video URL to summarize.",
			placeholder: "Paste a video URL",
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
	return b.promptForPendingCommandInput(c, pendingCommandInput{kind: kind})
}

// promptForSkillCommandInput asks what the skill should do when its command
// was sent without arguments (typical for command-menu taps).
func (b *Bot) promptForSkillCommandInput(c telebot.Context, cmd bootstrap.SkillCommand) error {
	return b.promptForPendingCommandInput(c, pendingCommandInput{kind: commandInputSkill, skill: cmd})
}

func (b *Bot) promptForPendingCommandInput(c telebot.Context, pending pendingCommandInput) error {
	key, ok := commandInputKeyForContext(c)
	if !ok || c.Message() == nil {
		return c.Send("Unable to start guided input. Send the command with its argument instead.")
	}

	spec := commandInputSpecForPending(pending)
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
	pending.expiresAt = time.Now().Add(commandInputTTL)
	b.pendingCommandInputs[key] = pending
	b.commandInputMu.Unlock()
	return nil
}

// skillCommandInputSpec builds the guided-input prompt for a skill command,
// e.g. "/media_request: what should the media-request skill do?".
func skillCommandInputSpec(cmd bootstrap.SkillCommand) commandInputSpec {
	hint := abbreviateForAck(cmd.Description, 120)
	prompt := fmt.Sprintf("/%s — %s\n\nWhat should the %s skill do? Send your request.", cmd.Command, hint, cmd.SkillName)
	return commandInputSpec{
		prompt:      prompt,
		placeholder: "Describe the request",
	}
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

func (b *Bot) handlePendingCommandInput(ctx context.Context, c telebot.Context) (bool, error) {
	key, ok := commandInputKeyForContext(c)
	if !ok || c.Message() == nil {
		return false, nil
	}
	pending, ok := b.takePendingCommandInput(key, time.Now())
	if !ok {
		return false, nil
	}

	rawInput := strings.TrimSpace(c.Message().Text)
	if pending.kind == commandInputSkill {
		if rawInput == "" || strings.HasPrefix(rawInput, "/") {
			return true, b.promptForPendingCommandInput(c, pending)
		}
		return true, b.dispatchAgentTurn(ctx, c, skillCommandPrompt(pending.skill, rawInput))
	}
	valid := false
	switch pending.kind {
	case commandInputVideoSummary:
		valid = videosummary.ValidateIngestURL(rawInput) == nil
	case commandInputYouTubeKaraoke:
		valid = youtubekaraoke.ValidateYouTubeURL(rawInput) == nil
	}
	if !valid {
		return true, b.promptForPendingCommandInput(c, pending)
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
