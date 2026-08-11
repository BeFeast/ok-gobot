package bot

import (
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/videosummary"
)

// handleNativeTextCommand routes unambiguous plain-text inputs to native
// workflows before they enter the model-driven agent path.
func (b *Bot) handleNativeTextCommand(c telebot.Context) (bool, error) {
	if c == nil || c.Message() == nil {
		return false, nil
	}

	rawURL, ok := bareYouTubeURL(c.Message().Text)
	if !ok {
		return false, nil
	}

	originalPayload := c.Message().Payload
	c.Message().Payload = rawURL
	defer func() {
		c.Message().Payload = originalPayload
	}()

	return true, b.handleVideoSummaryCommand(c)
}

func bareYouTubeURL(input string) (string, bool) {
	raw := strings.TrimSpace(input)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return "", false
	}
	if videosummary.ValidateYouTubeURL(raw) != nil {
		return "", false
	}
	return raw, true
}
