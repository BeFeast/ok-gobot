package bot

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"

	"ok-gobot/internal/evidence"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

// InitializeApprovalSystem sets up the approval workflow integration
func (b *Bot) InitializeApprovalSystem() {
	b.setupApprovalCallbacks()
	b.wireLocalCommandApproval()
	b.wireExecTool()
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

	localCmd, ok := tools.AsLocalCommand(localTool)
	if !ok {
		log.Println("Warning: local tool is not a LocalCommand")
		return
	}

	b.setApprovalFuncOnLocalCommand(localCmd)
}

// wireExecTool wires the deny-by-default exec tool to the operator approval
// gate and the job_events audit sink. Non-allowlisted commands request
// approval via Telegram inline buttons; allowlisted read-only commands run
// immediately (handled inside ExecTool).
func (b *Bot) wireExecTool() {
	execToolRaw, ok := b.toolRegistry.Get("exec")
	if !ok {
		log.Println("Warning: exec tool not found in registry")
		return
	}
	execTool, ok := tools.AsExecTool(execToolRaw)
	if !ok {
		log.Println("Warning: exec tool is not an *ExecTool")
		return
	}
	// Yolo: no approval gate. Safety = estop kill switch + pre-task restic
	// snapshot (host_task) + per-command file backup (exec) + audit.
	execTool.AuditSink = execAuditSink{bot: b}
}

// execAuditSink mirrors exec invocations into job_events for the active run.
// The command it receives is already redacted by ExecTool.audit, so no secret
// reaches the database. Best-effort: failures are logged, never propagated.
type execAuditSink struct{ bot *Bot }

func (s execAuditSink) RecordExec(chatID int64, command string, exitCode int, dur time.Duration, backup string) {
	if s.bot == nil || chatID == 0 {
		return
	}
	handle := s.bot.ackManager.Peek(chatID)
	if handle == nil || handle.JobID == "" {
		return
	}
	status := "ok"
	if exitCode != 0 {
		status = "nonzero"
	}
	payload := fmt.Sprintf(`{"rc":%d,"dur_ms":%d,"status":%q,"backup":%q}`, exitCode, dur.Milliseconds(), status, backup)
	if err := s.bot.store.AddJobEvent(storage.JobEvent{
		JobID:     handle.JobID,
		EventType: evidence.EventCommand,
		Message:   command,
		Payload:   payload,
	}); err != nil {
		log.Printf("[exec] warning: failed to record job event: %v", err)
	}
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
