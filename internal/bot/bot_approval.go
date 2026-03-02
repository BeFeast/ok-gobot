package bot

import (
	"fmt"
	"log"
	"time"

	"ok-gobot/internal/tools"
)
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

// wireLocalCommandApproval modifies the LocalCommand tool to use approval system.
// It looks up the "local" tool from the base tool registry (shared by all runs).
func (b *Bot) wireLocalCommandApproval() {
	localTool, ok := b.toolRegistry.Get("local")
	if !ok {
		log.Println("Warning: LocalCommand tool not found in registry")
		return
	}

	localCmd, ok := localTool.(*tools.LocalCommand)
	if !ok {
		log.Println("Warning: local tool is not a LocalCommand")
		return
	}

	b.setApprovalFuncOnLocalCommand(localCmd)
}

// setApprovalFuncOnLocalCommand sets the approval function on a LocalCommand instance
func (b *Bot) setApprovalFuncOnLocalCommand(localCmd *tools.LocalCommand) {
	localCmd.ApprovalFunc = func(command string) (bool, error) {
		chatID := b.getCurrentChatID()

		if chatID == 0 {
			return !b.approvalManager.IsDangerous(command), nil
		}

		if !b.approvalManager.IsDangerous(command) {
			return true, nil
		}

		resultCh, _ := b.approvalManager.RequestApproval(chatID, command)

		select {
		case approved := <-resultCh:
			return approved, nil
		case <-time.After(65 * time.Second):
			return false, fmt.Errorf("approval request timed out")
		}
	}
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
