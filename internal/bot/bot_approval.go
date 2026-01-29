package bot

import (
	"context"
	"fmt"
	"log"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/tools"
)

// InitializeApprovalSystem sets up the approval workflow integration
func (b *Bot) InitializeApprovalSystem() {
	// Register callback handlers for approval buttons
	b.setupApprovalCallbacks()

	// Wire up the LocalCommand tool with approval function
	b.wireLocalCommandApproval()
}

// setupApprovalCallbacks registers Telegram callback handlers for approval/deny buttons
func (b *Bot) setupApprovalCallbacks() {
	// The callback data format is "approve|<requestID>" or "deny|<requestID>"
	// We need to handle these in the general callback handler
	// This will be added to Start() method as part of bot initialization
}

// wireLocalCommandApproval modifies the LocalCommand tool to use approval system
func (b *Bot) wireLocalCommandApproval() {
	// Get the local command tool from registry
	localTool, ok := b.toolAgent.GetTools().Get("local")
	if !ok {
		log.Println("Warning: LocalCommand tool not found in registry")
		return
	}

	// Cast to LocalCommand
	localCmd, ok := localTool.(*tools.LocalCommand)
	if !ok {
		log.Println("Warning: local tool is not a LocalCommand")
		return
	}

	// Store reference to bot for approval
	b.setApprovalFuncOnLocalCommand(localCmd)
}

// setApprovalFuncOnLocalCommand sets the approval function on a LocalCommand instance
func (b *Bot) setApprovalFuncOnLocalCommand(localCmd *tools.LocalCommand) {
	// We need to extract chatID from somewhere - this is a challenge
	// The best approach is to use a context value
	// For now, we'll create a simple synchronous channel-based approach

	localCmd.ApprovalFunc = func(command string) (bool, error) {
		// Get chatID from the bot's current context
		// This is hacky but works for single-chat scenarios
		chatID := b.getCurrentChatID()

		if chatID == 0 {
			// No chat context, auto-approve safe commands
			return !b.approvalManager.IsDangerous(command), nil
		}

		// Check if dangerous
		if !b.approvalManager.IsDangerous(command) {
			return true, nil
		}

		// Request approval
		resultCh, _ := b.approvalManager.RequestApproval(chatID, command)

		// Wait for approval
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

// ProcessRequestWithApprovalContext wraps ProcessRequest with chat context for approvals
func (b *Bot) ProcessRequestWithApprovalContext(ctx context.Context, chatID int64, userMessage string, session string) (*agent.AgentResponse, error) {
	// Set chat context for approval system
	b.setCurrentChatID(chatID)
	defer b.setCurrentChatID(0)

	// Execute through toolAgent
	return b.toolAgent.ProcessRequest(ctx, userMessage, session)
}
