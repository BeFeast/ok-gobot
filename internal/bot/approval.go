package bot

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v4"
)

// ApprovalManager handles dangerous command approvals
type ApprovalManager struct {
	bot              *telebot.Bot
	pendingApprovals map[string]*PendingApproval
	mu               sync.Mutex
}

// PendingApproval represents a command awaiting approval
type PendingApproval struct {
	ChatID    int64
	Command   string
	ResultCh  chan bool
	CreatedAt time.Time
}

// NewApprovalManager creates a new approval manager
func NewApprovalManager(bot *telebot.Bot) *ApprovalManager {
	am := &ApprovalManager{
		bot:              bot,
		pendingApprovals: make(map[string]*PendingApproval),
	}

	// Start cleanup goroutine for expired approvals
	go am.cleanupExpiredApprovals()

	return am
}

// dangerousPatterns contains patterns that require user approval
var dangerousPatterns = []string{
	"rm -rf",
	"rm -r",
	"kill ",
	"killall",
	"shutdown",
	"reboot",
	"dd ",
	"mkfs",
	"fdisk",
	"format ",
	"passwd",
	"chmod 777",
	"chown",
	"iptables",
	"systemctl stop",
	"systemctl disable",
	"docker rm",
	"DROP TABLE",
	"DELETE FROM",
	"truncate ",
	"> /dev/",
	"mkfs.",
	"parted",
	"cfdisk",
	"sfdisk",
	"wipefs",
	"rm -f",
	"pkill",
	"halt",
	"poweroff",
	"init 0",
	"init 6",
}

// IsDangerous checks if a command matches dangerous patterns
func (am *ApprovalManager) IsDangerous(command string) bool {
	lower := strings.ToLower(command)

	for _, pattern := range dangerousPatterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			return true
		}
	}

	return false
}

// RequestApproval sends an approval request to the user
// Returns a channel that will receive the approval result and a request ID
func (am *ApprovalManager) RequestApproval(chatID int64, command string) (chan bool, string) {
	am.mu.Lock()
	defer am.mu.Unlock()

	// Generate unique request ID
	requestID := fmt.Sprintf("%d_%d", chatID, time.Now().UnixNano())

	// Create result channel
	resultCh := make(chan bool, 1)

	// Store pending approval
	am.pendingApprovals[requestID] = &PendingApproval{
		ChatID:    chatID,
		Command:   command,
		ResultCh:  resultCh,
		CreatedAt: time.Now(),
	}

	// Create inline keyboard with approval buttons
	keyboard := &telebot.ReplyMarkup{}
	btnApprove := keyboard.Data("✅ Approve", "approve", requestID)
	btnDeny := keyboard.Data("❌ Deny", "deny", requestID)
	keyboard.Inline(keyboard.Row(btnApprove, btnDeny))

	// Send approval request message
	msg := fmt.Sprintf("⚠️ *Dangerous Command Detected*\n\n"+
		"Command: `%s`\n\n"+
		"This command may cause irreversible changes. Do you want to proceed?",
		command)

	chat := &telebot.Chat{ID: chatID}
	am.bot.Send(chat, msg, &telebot.SendOptions{
		ParseMode:   telebot.ModeMarkdown,
		ReplyMarkup: keyboard,
	})

	// Auto-deny after 60 seconds
	go am.autoTimeout(requestID, 60*time.Second)

	return resultCh, requestID
}

// HandleCallback processes approval/denial from inline keyboard
func (am *ApprovalManager) HandleCallback(callbackID string, approved bool) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	pending, exists := am.pendingApprovals[callbackID]
	if !exists {
		return fmt.Errorf("approval request not found or expired")
	}

	// Send result to channel
	select {
	case pending.ResultCh <- approved:
	default:
	}

	// Remove from pending map
	delete(am.pendingApprovals, callbackID)

	return nil
}

// autoTimeout automatically denies a request after the specified duration
func (am *ApprovalManager) autoTimeout(requestID string, timeout time.Duration) {
	time.Sleep(timeout)

	am.mu.Lock()
	defer am.mu.Unlock()

	pending, exists := am.pendingApprovals[requestID]
	if !exists {
		return // Already handled
	}

	// Send timeout denial
	select {
	case pending.ResultCh <- false:
	default:
	}

	// Notify user
	chat := &telebot.Chat{ID: pending.ChatID}
	am.bot.Send(chat, "⏱️ Command approval timed out. Request denied.")

	// Remove from pending map
	delete(am.pendingApprovals, requestID)
}

// cleanupExpiredApprovals periodically removes expired approvals
func (am *ApprovalManager) cleanupExpiredApprovals() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		am.mu.Lock()
		now := time.Now()
		for id, pending := range am.pendingApprovals {
			// Remove approvals older than 2 minutes
			if now.Sub(pending.CreatedAt) > 2*time.Minute {
				delete(am.pendingApprovals, id)
			}
		}
		am.mu.Unlock()
	}
}
