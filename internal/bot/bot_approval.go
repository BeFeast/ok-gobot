package bot

// InitializeApprovalSystem sets up the approval workflow integration
func (b *Bot) InitializeApprovalSystem() {
	// Register callback handlers for approval buttons
	b.setupApprovalCallbacks()
}

// setupApprovalCallbacks registers Telegram callback handlers for approval/deny buttons
func (b *Bot) setupApprovalCallbacks() {
	// The callback data format is "approve|<requestID>" or "deny|<requestID>"
	// We need to handle these in the general callback handler
	// This will be added to Start() method as part of bot initialization
}

// currentChatID stores the active chat ID for approval context
var currentChatID int64

// setCurrentChatID sets the chat ID for the current request context
func (b *Bot) setCurrentChatID(chatID int64) {
	currentChatID = chatID
}

// getCurrentChatID gets the current chat ID
func (b *Bot) getCurrentChatID() int64 {
	return currentChatID
}
