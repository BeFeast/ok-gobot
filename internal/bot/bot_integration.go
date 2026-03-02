package bot

import (
	"gopkg.in/telebot.v4"
)

// RegisterApprovalHandlers should be called in Start() to set up callback handlers
func (b *Bot) RegisterApprovalHandlers() {
	if b.approvalManager == nil {
		return
	}

	// Handle callback queries for approval buttons
	b.api.Handle(telebot.OnCallback, func(c telebot.Context) error {
		callback := c.Callback()
		if callback == nil {
			return nil
		}

		requestID := callback.Data
		switch callback.Unique {
		case "approve":
			if err := b.approvalManager.HandleCallback(requestID, true); err != nil {
				return c.Respond(&telebot.CallbackResponse{Text: "Request expired"})
			}
			c.Edit("✅ Command approved and executing...")
			return c.Respond()
		case "deny":
			if err := b.approvalManager.HandleCallback(requestID, false); err != nil {
				return c.Respond(&telebot.CallbackResponse{Text: "Request expired"})
			}
			c.Edit("❌ Command denied")
			return c.Respond()
		}
		return nil
	})
}


