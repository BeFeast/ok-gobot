package bot

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/control"
)

// ApprovalManager handles dangerous command approvals
type ApprovalManager struct {
	bot              *telebot.Bot
	pendingApprovals map[string]*PendingApproval
	mu               sync.Mutex
	controlHub       *control.Hub // optional: emit approval events over WebSocket
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
	// Destructive file operations
	"rm -rf", "rm -r", "rm -f",
	// Process management
	"kill ", "killall", "pkill",
	// System state
	"shutdown", "reboot", "halt", "poweroff", "init 0", "init 6",
	// Disk/partition
	"dd ", "mkfs", "fdisk", "format ", "mkfs.", "parted", "cfdisk", "sfdisk", "wipefs",
	// Credentials/permissions
	"passwd", "chmod 777", "chown",
	// Networking/firewall
	"iptables", "nftables",
	// Service management
	"systemctl stop", "systemctl disable",
	// Container management
	"docker rm", "docker rmi",
	// Database destructive ops
	"DROP TABLE", "DROP DATABASE", "DELETE FROM", "truncate ",
	// Device writes
	"> /dev/",
	// Privilege escalation
	"sudo ", "su -", "su root", "doas ",
	// Remote code execution / exfiltration
	"curl | sh", "curl |sh", "curl|sh",
	"wget | sh", "wget |sh", "wget|sh",
	"curl | bash", "curl |bash", "curl|bash",
	"wget | bash", "wget |bash", "wget|bash",
	// Shell eval / exec
	"eval ", "exec ",
	// Path-qualified dangerous binaries
	"/bin/rm ", "/usr/bin/rm ",
	"/sbin/shutdown", "/sbin/reboot", "/sbin/halt",
	"/sbin/mkfs", "/sbin/fdisk",
}

// SetControlHub wires the control-server event hub so that approval events are
// pushed to connected WebSocket clients in real time.
func (am *ApprovalManager) SetControlHub(h *control.Hub) {
	am.mu.Lock()
	am.controlHub = h
	am.mu.Unlock()
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

	// Emit approval.request event to WebSocket clients.
	if am.controlHub != nil {
		am.controlHub.Emit(control.EvtApprovalRequest, control.ApprovalRequestPayload{
			ApprovalID: requestID,
			ChatID:     chatID,
			Command:    command,
		})
	}

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

	if approved {
		log.Printf("[approval] approved: requestID=%s chatID=%d command=%q", callbackID, pending.ChatID, pending.Command)
	} else {
		log.Printf("[approval] denied: requestID=%s chatID=%d command=%q", callbackID, pending.ChatID, pending.Command)
	}

	// Remove from pending map
	delete(am.pendingApprovals, callbackID)

	// Emit approval.resolved event to WebSocket clients.
	if am.controlHub != nil {
		am.controlHub.Emit(control.EvtApprovalResolved, control.ApprovalResolvedPayload{
			ApprovalID: callbackID,
			Approved:   approved,
		})
	}

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

	log.Printf("[approval] timeout: requestID=%s chatID=%d command=%q — auto-denied", requestID, pending.ChatID, pending.Command)

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
