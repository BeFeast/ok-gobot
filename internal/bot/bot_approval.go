package bot

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"

	"ok-gobot/internal/tools"
)

// InitializeApprovalSystem sets up the approval workflow integration
func (b *Bot) InitializeApprovalSystem() {
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

// setApprovalFuncOnLocalCommand sets the approval function on a LocalCommand instance.
// chatID is captured via the per-goroutine chatIDMap to avoid global race conditions.
func (b *Bot) setApprovalFuncOnLocalCommand(localCmd *tools.LocalCommand) {
	localCmd.ApprovalFunc = func(command string) (bool, error) {
		chatID := b.getCurrentChatID()

		if chatID == 0 {
			// No chat context — deny dangerous commands outright, allow safe ones.
			if b.approvalManager.IsDangerous(command) {
				return false, fmt.Errorf("dangerous command rejected: no chat context for approval")
			}
			return true, nil
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

// chatIDMap stores per-goroutine chat IDs keyed by goroutine-associated chat ID.
// This replaces the previous racy global variable.
var (
	chatIDMap   = make(map[int64]int64) // key: goroutine-specific identifier (chatID itself for now)
	chatIDMu    sync.RWMutex
	chatIDByGID = make(map[uint64]int64) // goroutine ID -> chatID
	gidMu       sync.RWMutex
)

// setCurrentChatID stores chatID for the current processing goroutine.
func (b *Bot) setCurrentChatID(chatID int64) {
	gid := getGoroutineID()
	gidMu.Lock()
	if chatID == 0 {
		delete(chatIDByGID, gid)
	} else {
		chatIDByGID[gid] = chatID
	}
	gidMu.Unlock()
}

// getCurrentChatID retrieves the chatID for the current processing goroutine.
func (b *Bot) getCurrentChatID() int64 {
	gid := getGoroutineID()
	gidMu.RLock()
	id := chatIDByGID[gid]
	gidMu.RUnlock()
	return id
}

// getGoroutineID extracts the current goroutine ID from the runtime stack.
func getGoroutineID() uint64 {
	var buf [64]byte
	n := runtimeStack(buf[:], false)
	// Stack starts with "goroutine <id> ["
	var id uint64
	for i := len("goroutine "); i < n; i++ {
		if buf[i] < '0' || buf[i] > '9' {
			break
		}
		id = id*10 + uint64(buf[i]-'0')
	}
	return id
}

// runtimeStack is a variable for testing; defaults to runtime.Stack.
var runtimeStack = runtime.Stack
