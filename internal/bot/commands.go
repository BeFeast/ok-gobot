package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

// registerExtraHandlers registers all additional command handlers
func (b *Bot) registerExtraHandlers() {
	b.api.Handle("/abort", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleAbortCommand(c)
	}))

	b.api.Handle("/whoami", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleWhoamiCommand(c)
	}))

	b.api.Handle("/commands", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleCommandsCommand(c)
	}))

	b.api.Handle("/new", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleNewCommand(c)
	}))

	b.api.Handle("/note", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleNoteCommand(c)
	}))

	b.api.Handle("/stop", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleStopCommand(c)
	}))

	b.api.Handle("/usage", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleUsageCommand(c)
	}))

	b.api.Handle("/context", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleContextCommand(c)
	}))

	b.api.Handle("/compact", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleCompactCommand(c)
	}))

	b.api.Handle("/think", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleThinkCommand(c)
	}))

	b.api.Handle("/verbose", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleVerboseCommand(c)
	}))

	b.api.Handle("/queue", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleQueueCommand(c)
	}))

	b.api.Handle("/tts", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleTTSCommand(c)
	}))

	b.api.Handle("/restart", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleRestartCommand(c)
	}))

	b.api.Handle("/estop", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleEstopCommand(c)
	}))

	b.api.Handle("/task", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleTaskCommand(c)
	}))

	b.api.Handle("/btw", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleBtwCommand(c)
	}))

	b.api.Handle("/roles", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleRolesCommand(c)
	}))

	b.api.Handle("/role", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleRoleCommand(c)
	}))

	b.api.Handle("/role_run", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleRoleRunCommand(c)
	}))

	b.api.Handle("/jobs", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleTGJobsCommand(c)
	}))

	b.api.Handle("/job", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleTGJobCommand(c)
	}))

	b.api.Handle("/job_cancel", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleTGJobCancelCommand(c)
	}))

	b.api.Handle("/active_memory", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleActiveMemoryCommand(c)
	}))

	b.api.Handle("/memory_curate", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleMemoryCurateCommand(c)
	}))

	b.api.Handle("/qmd", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleQMDCommand(c)
	}))
}

// handleWhoamiCommand shows sender info
func (b *Bot) handleWhoamiCommand(c telebot.Context) error {
	sender := c.Sender()
	chat := c.Chat()

	var sb strings.Builder
	sb.WriteString("👤 *Who am I:*\n\n")
	sb.WriteString(fmt.Sprintf("🆔 User ID: `%d`\n", sender.ID))
	if sender.Username != "" {
		sb.WriteString(fmt.Sprintf("👤 Username: @%s\n", sender.Username))
	}
	if sender.FirstName != "" {
		name := sender.FirstName
		if sender.LastName != "" {
			name += " " + sender.LastName
		}
		sb.WriteString(fmt.Sprintf("📛 Name: %s\n", name))
	}
	sb.WriteString(fmt.Sprintf("💬 Chat ID: `%d`\n", chat.ID))
	sb.WriteString(fmt.Sprintf("📋 Chat Type: %s\n", chat.Type))

	if b.authManager.IsAdmin(sender.ID) {
		sb.WriteString("\n🔑 Role: admin")
	} else if b.authManager.CheckAccess(sender.ID, chat.ID) {
		sb.WriteString("\n🔑 Role: authorized")
	} else {
		sb.WriteString("\n🔒 Role: unauthorized")
	}

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleCommandsCommand lists all slash commands
func (b *Bot) handleCommandsCommand(c telebot.Context) error {
	commands := []struct{ cmd, desc string }{
		{"help", "Show available commands"},
		{"commands", "List all slash commands"},
		{"status", "Show current status"},
		{"whoami", "Show your sender info"},
		{"new", "Start a new session"},
		{"note", "Quick-capture note into today's memory"},
		{"clear", "Clear conversation history"},
		{"stop", "Stop the current run"},
		{"abort", "Abort the current run"},
		{"memory", "Show today's memory"},
		{"memory_status", "Show memory index health"},
		{"qmd", "Show QMD sidecar status and fallback"},
		{"tools", "List available tools"},
		{"model", "Show or set AI model"},
		{"agent", "Manage agents"},
		{"usage", "Usage footer control (off/tokens/full)"},
		{"context", "Explain how context is built"},
		{"compact", "Compact session context"},
		{"think", "Set thinking level (off/low/medium/high/adaptive)"},
		{"verbose", "Toggle verbose mode (on/off)"},
		{"active_memory", "Pre-reply memory recall (status/on/off)"},
		{"queue", "Adjust queue settings"},
		{"tts", "Control text-to-speech"},
		{"estop", "Emergency stop for dangerous tools (admin)"},
		{"task", "Spawn a sub-agent task"},
		{"btw", "Ask a side question while task runs"},
		{"roles", "List available roles"},
		{"role", "Show role details"},
		{"role_run", "Run a role as a durable job (admin)"},
		{"jobs", "List recent durable jobs"},
		{"job", "Show job details"},
		{"job_cancel", "Cancel a durable job (admin)"},
		{"memory_curate", "Curate daily notes into auditable durable-memory drafts (admin)"},
		{"activate", "Activate bot in group"},
		{"standby", "Set standby mode in group"},
		{"pair", "Pair with bot using code"},
		{"auth", "Authorization management (admin)"},
		{"reload", "Reload configuration (admin)"},
		{"restart", "Restart the bot (admin)"},
	}

	var sb strings.Builder
	sb.WriteString("📋 *All Commands:*\n\n")
	for _, cmd := range commands {
		sb.WriteString(fmt.Sprintf("/%s — %s\n", cmd.cmd, cmd.desc))
	}

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleNewCommand starts a new session
func (b *Bot) handleNewCommand(c telebot.Context) error {
	chatID := c.Chat().ID

	if err := b.store.ResetSession(chatID); err != nil {
		log.Printf("Failed to reset session: %v", err)
		return c.Send("❌ Failed to start new session")
	}

	return c.Send("✅ New session started. History and counters cleared.")
}

// handleNoteCommand appends a quick note directly to today's memory file.
func (b *Bot) handleNoteCommand(c telebot.Context) error {
	noteText := strings.TrimSpace(c.Message().Payload)
	if noteText == "" {
		return c.Send("❌ Usage: /note <text>")
	}

	if err := b.appendQuickNoteToTelegramMemory(c.Chat(), senderIDFromMessage(c.Message()), noteText); err != nil {
		log.Printf("Failed to append quick note: %v", err)
		return c.Send("❌ Failed to save quick note")
	}

	return c.Send("📝 Quick note saved.")
}

// handleStopCommand stops the current AI run via the runtime hub.
func (b *Bot) handleStopCommand(c telebot.Context) error {
	sessionKey := sessionKeyForChat(c.Chat())

	if b.hub.IsActive(sessionKey) {
		b.hub.Cancel(sessionKey)
		return c.Send("🛑 Stopped current run.")
	}
	return c.Send("ℹ️ No active run to stop.")
}

// handleAbortCommand aborts the current AI run via the runtime hub.
func (b *Bot) handleAbortCommand(c telebot.Context) error {
	sessionKey := sessionKeyForChat(c.Chat())

	if b.hub.IsActive(sessionKey) {
		b.hub.Cancel(sessionKey)
		return c.Send("⛔ Aborted")
	}
	return c.Send("ℹ️ No active run to abort.")
}

// handleUsageCommand controls usage footer display
func (b *Bot) handleUsageCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	args := strings.TrimSpace(c.Message().Payload)

	if args == "" {
		mode, _ := b.store.GetSessionOption(chatID, "usage_mode")
		if mode == "" {
			mode = "off"
		}
		return c.Send(fmt.Sprintf("📊 Usage display: `%s`\n\nOptions: `/usage off` | `/usage tokens` | `/usage full`", mode),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	switch args {
	case "off", "tokens", "full":
		if err := b.store.SetSessionOption(chatID, "usage_mode", args); err != nil {
			return c.Send("❌ Failed to set usage mode")
		}
		return c.Send(fmt.Sprintf("✅ Usage display set to: `%s`", args),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	default:
		return c.Send("❌ Invalid mode. Use: off, tokens, full")
	}
}

// handleContextCommand explains how context is built
func (b *Bot) handleContextCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	usage, _ := b.store.GetTokenUsage(chatID)

	var sb strings.Builder
	sb.WriteString("📚 *Context Structure:*\n\n")

	// System prompt components
	sb.WriteString("*System Prompt Components:*\n")
	prompt := b.personality.GetSystemPrompt()
	sb.WriteString(fmt.Sprintf("• Personality (SOUL.md, IDENTITY.md, etc.): ~%d chars\n", len(prompt)))

	// Tools
	toolCount := len(b.toolRegistry.List())
	sb.WriteString(fmt.Sprintf("• Tools: %d registered\n", toolCount))

	// Memory mode + indexed daily notes
	memoryMode := bootstrap.NormalizeMemoryMode(b.aiConfig.MemoryMode)
	sb.WriteString(fmt.Sprintf("• Memory mode: `%s` — %s\n", memoryMode, memoryModeBlurb(memoryMode)))
	if loader := b.personality.Loader(); loader != nil {
		inline := loader.DailyNoteDatesForMode(memoryMode)
		if len(inline) > 0 {
			sb.WriteString(fmt.Sprintf("• Inlined daily notes: %s\n", strings.Join(inline, ", ")))
		} else {
			sb.WriteString("• Inlined daily notes: none (retrieval-first)\n")
		}
		retrievalOnly := loader.DailyNoteSourcesForMode(memoryMode)
		if len(retrievalOnly) > 0 {
			sb.WriteString(fmt.Sprintf("• Retrieval-only daily notes: %s\n", strings.Join(retrievalOnly, ", ")))
		}
	}
	memoryEnabled := false
	if _, ok := b.toolRegistry.Get("memory_search"); ok {
		memoryEnabled = true
	}
	if memoryEnabled {
		sb.WriteString("• Active memory tools: `memory_search`")
		if _, ok := b.toolRegistry.Get("memory_get"); ok {
			sb.WriteString(", `memory_get`")
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("• Active memory tools: none (memory.enabled=false)\n")
	}
	if b.activeMemory != nil && b.activeMemory.IsConfigured() {
		sb.WriteString("• Active memory context pack: cited snippets with scores and a hard character budget\n")
	} else if b.memoryManager != nil {
		sb.WriteString("• Memory context pack builder: available via `ok-gobot memory pack`\n")
	} else {
		sb.WriteString("• Memory context pack builder: disabled\n")
	}
	sb.WriteString("• Memory recall: scoped by user/chat/session policy\n")

	// Session info
	sb.WriteString(fmt.Sprintf("\n*Session:*\n"))
	sb.WriteString(fmt.Sprintf("• Messages: %d\n", usage.MessageCount))
	sb.WriteString(fmt.Sprintf("• Compactions: %d\n", usage.CompactionCount))

	// Token budget
	contextLimit := agent.ModelLimits(b.aiConfig.Model)
	sb.WriteString(fmt.Sprintf("\n*Token Budget:*\n"))
	if usage.TotalTokens > 0 {
		pct := float64(usage.TotalTokens) / float64(contextLimit) * 100
		sb.WriteString(fmt.Sprintf("• Used: %s / %s (%.0f%%)\n",
			formatTokenCount(usage.TotalTokens), formatTokenCount(contextLimit), pct))
	} else {
		sb.WriteString(fmt.Sprintf("• Context limit: %s\n", formatTokenCount(contextLimit)))
	}

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// memoryModeBlurb returns a short, human-friendly description of a memory mode.
func memoryModeBlurb(mode string) string {
	switch mode {
	case bootstrap.MemoryModeRetrievalFirst:
		return "MEMORY.md inlined; daily notes via memory_search"
	case bootstrap.MemoryModeStartupRecent:
		return "MEMORY.md + today inlined; older notes via memory_search"
	default:
		return "MEMORY.md + today + yesterday inlined"
	}
}

// handleCompactCommand manually compacts session context using the D0/D1/D2
// context tree. Old transcript messages are summarised into D1 nodes that
// reference the original message span; when enough D1 nodes accumulate they
// are rolled up into a D2 node.
func (b *Bot) handleCompactCommand(c telebot.Context) error {
	sessionKey := sessionKeyForChat(c.Chat())
	sk := string(sessionKey)

	msgs, err := b.store.GetSessionMessagesV2(sk, 500)
	if err != nil || len(msgs) < agent.MinD1SpanMessages {
		return c.Send(fmt.Sprintf("ℹ️ Not enough conversation to compact (need at least %d messages).", agent.MinD1SpanMessages))
	}

	_ = c.Send("🌳 Compacting conversation into context tree...")

	// Lifecycle memory flush BEFORE the transcript is replaced. Failure must
	// not block compaction (acceptance criteria: "Flush failure does not
	// block emergency compaction forever; it produces a clear warning").
	if flushRes, flushErr := b.lifecycleFlush.Flush(agent.FlushRecord{
		Kind:         agent.FlushKindPreCompact,
		SessionKey:   sk,
		MessageCount: len(msgs),
		Summary:      fmt.Sprintf("about to compact %d messages from session %s", len(msgs), sk),
	}); flushErr != nil {
		log.Printf("[lifecycle] pre-compact memory flush failed (continuing): %v", flushErr)
		_ = c.Send(fmt.Sprintf("⚠️ Pre-compact memory flush failed: %v (continuing with compaction)", flushErr))
	} else if flushRes.Skipped && flushRes.Reason != "" && flushRes.Reason != "already flushed for this lifecycle state" {
		log.Printf("[lifecycle] pre-compact memory flush skipped: %s", flushRes.Reason)
	}

	tc := agent.NewTreeCompactor(b.ai, b.getEffectiveModel(c.Chat().ID))
	treeResult, err := b.compactToTree(context.Background(), tc, sk, msgs)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ Compaction failed: %v", err))
	}

	// Update compaction counter in legacy session store.
	if len(treeResult.NewNodes) > 0 {
		b.store.SaveSessionSummary(c.Chat().ID, treeResult.NewNodes[0].Summary) //nolint:errcheck
	}

	return c.Send(treeResult.FormatNotification())
}

// compactToTree performs a full tree-compaction pass: creates D1 nodes from
// raw messages, optionally rolls D1s into D2, then replaces the live
// transcript with only the recent tail that was not compacted.
func (b *Bot) compactToTree(ctx context.Context, tc *agent.TreeCompactor, sessionKey string, msgs []storage.SessionMessageV2) (*agent.TreeCompactionResult, error) {
	result := &agent.TreeCompactionResult{}

	// Keep the most recent MinD1SpanMessages messages as live context;
	// compact everything before them.
	cutoff := len(msgs) - agent.MinD1SpanMessages
	if cutoff < agent.MinD1SpanMessages {
		return nil, fmt.Errorf("not enough messages to compact")
	}

	toCompact := msgs[:cutoff]

	// Build SpanMessages from the slice to compact.
	spanMsgs := make([]agent.SpanMessage, 0, len(toCompact))
	for _, m := range toCompact {
		if m.Role == "system" {
			continue
		}
		spanMsgs = append(spanMsgs, agent.SpanMessage{
			ID:      m.ID,
			Role:    m.Role,
			Content: m.Content,
		})
	}
	if len(spanMsgs) < agent.MinD1SpanMessages {
		return nil, fmt.Errorf("not enough non-system messages to compact")
	}

	// Estimate original tokens.
	counter := agent.NewTokenCounter()
	for _, m := range spanMsgs {
		result.OriginalTokens += counter.CountTokens(m.Content) + 4
	}

	// Create a D1 node for the compacted span.
	d1Node, err := tc.CompactToD1(ctx, sessionKey, spanMsgs)
	if err != nil {
		return nil, err
	}

	// Persist the D1 node.
	d1ID, err := b.store.SaveContextNode(storage.ContextNode{
		SessionKey: sessionKey,
		Density:    d1Node.Density,
		Summary:    d1Node.Summary,
		SpanStart:  d1Node.SpanStart,
		SpanEnd:    d1Node.SpanEnd,
		TokenCount: d1Node.TokenCount,
	})
	if err != nil {
		return nil, fmt.Errorf("save D1 node: %w", err)
	}
	d1Node.ID = d1ID
	result.NewNodes = append(result.NewNodes, *d1Node)
	result.SummaryTokens += d1Node.TokenCount

	// Record archived message IDs.
	for _, m := range toCompact {
		result.ArchivedMsgIDs = append(result.ArchivedMsgIDs, m.ID)
	}

	// Delete the compacted messages from the live transcript and replace
	// with a single assistant message carrying the tree summary.
	if err := b.store.ClearSessionMessagesV2(sessionKey); err != nil {
		return nil, fmt.Errorf("clear old messages: %w", err)
	}

	// Re-insert the context tree summary + recent tail.
	allNodes, err := b.store.GetAllContextNodes(sessionKey)
	if err != nil {
		return nil, fmt.Errorf("load context nodes: %w", err)
	}
	tree := buildAgentTree(sessionKey, allNodes)
	treeSummary := tree.FormatForPrompt()
	if treeSummary != "" {
		if err := b.store.SaveSessionMessageV2(sessionKey, "assistant", treeSummary, ""); err != nil {
			return nil, fmt.Errorf("save tree summary: %w", err)
		}
	}

	// Re-insert the recent tail messages that were not compacted.
	tail := msgs[cutoff:]
	for _, m := range tail {
		if err := b.store.SaveSessionMessageV2(sessionKey, m.Role, m.Content, m.RunID); err != nil {
			return nil, fmt.Errorf("re-insert tail message: %w", err)
		}
	}

	// Check if we have enough D1 nodes to produce a D2 roll-up.
	d1Nodes, err := b.store.GetContextNodes(sessionKey, agent.DensityD1)
	if err != nil {
		return nil, fmt.Errorf("load D1 nodes: %w", err)
	}

	// Only create a D2 if we have unparented D1 nodes meeting the threshold.
	var unparentedD1 []agent.ContextNode
	for _, n := range d1Nodes {
		if n.ParentID == 0 {
			unparentedD1 = append(unparentedD1, storageNodeToAgent(n))
		}
	}

	if len(unparentedD1) >= agent.MinD1NodesForD2 {
		d2Node, err := tc.CompactToD2(ctx, sessionKey, unparentedD1)
		if err != nil {
			return nil, fmt.Errorf("D2 compaction: %w", err)
		}

		d2ID, err := b.store.SaveContextNode(storage.ContextNode{
			SessionKey: sessionKey,
			Density:    d2Node.Density,
			Summary:    d2Node.Summary,
			SpanStart:  d2Node.SpanStart,
			SpanEnd:    d2Node.SpanEnd,
			TokenCount: d2Node.TokenCount,
		})
		if err != nil {
			return nil, fmt.Errorf("save D2 node: %w", err)
		}
		d2Node.ID = d2ID
		result.NewNodes = append(result.NewNodes, *d2Node)

		// Link the D1 nodes to the new D2 parent.
		for _, n := range unparentedD1 {
			if err := b.store.SetContextNodeParent(n.ID, d2ID); err != nil {
				return nil, fmt.Errorf("set D1 parent: %w", err)
			}
		}
	}

	result.TokensSaved = result.OriginalTokens - result.SummaryTokens
	return result, nil
}

// buildAgentTree converts storage context nodes into an agent.ContextTree.
func buildAgentTree(sessionKey string, nodes []storage.ContextNode) *agent.ContextTree {
	tree := &agent.ContextTree{SessionKey: sessionKey}
	for _, n := range nodes {
		tree.Nodes = append(tree.Nodes, storageNodeToAgent(n))
	}
	return tree
}

// storageNodeToAgent converts a storage.ContextNode to agent.ContextNode.
func storageNodeToAgent(n storage.ContextNode) agent.ContextNode {
	return agent.ContextNode{
		ID:         n.ID,
		SessionKey: n.SessionKey,
		Density:    n.Density,
		Summary:    n.Summary,
		SpanStart:  n.SpanStart,
		SpanEnd:    n.SpanEnd,
		ParentID:   n.ParentID,
		TokenCount: n.TokenCount,
		CreatedAt:  n.CreatedAt,
	}
}

// handleThinkCommand controls thinking level
func (b *Bot) handleThinkCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	args := strings.TrimSpace(c.Message().Payload)

	if args == "" {
		level, _ := b.store.GetSessionOption(chatID, "think_level")
		if level == "" {
			level = "(default)"
		}
		return c.Send(fmt.Sprintf("🧠 Think level: `%s`\n\nOptions: `/think off` | `/think low` | `/think medium` | `/think high` | `/think adaptive`", level),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	validLevels := map[string]bool{"off": true, "low": true, "medium": true, "high": true, "adaptive": true}
	if !validLevels[args] {
		return c.Send("❌ Invalid level. Use: off, low, medium, high, adaptive")
	}

	if err := b.store.SetSessionOption(chatID, "think_level", args); err != nil {
		return c.Send("❌ Failed to set think level")
	}
	return c.Send(fmt.Sprintf("✅ Think level set to: `%s`", args),
		&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleVerboseCommand toggles verbose mode
func (b *Bot) handleVerboseCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	args := strings.TrimSpace(c.Message().Payload)

	if args == "" {
		verbose, _ := b.store.GetVerbose(chatID)
		state := "off"
		if verbose {
			state = "on"
		}
		return c.Send(fmt.Sprintf("📝 Verbose: `%s`\n\nOptions: `/verbose on` | `/verbose off`", state),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	switch args {
	case "on":
		b.store.SetVerbose(chatID, true)
		return c.Send("✅ Verbose mode: on")
	case "off":
		b.store.SetVerbose(chatID, false)
		return c.Send("✅ Verbose mode: off")
	default:
		return c.Send("❌ Use: on, off")
	}
}

// handleQueueCommand adjusts queue settings
func (b *Bot) handleQueueCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	args := strings.Fields(c.Message().Payload)

	if len(args) == 0 {
		mode, _ := b.store.GetSessionOption(chatID, "queue_mode")
		if mode == "" {
			mode = "interrupt"
		}
		debounceMs := b.debouncer.GetDelay()
		return c.Send(fmt.Sprintf("🪢 Queue: `%s` (debounce %dms)\n\nUsage: `/queue <mode> [debounce_ms]`\nModes: collect, steer, interrupt", mode, debounceMs.Milliseconds()),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	mode := args[0]
	validModes := map[string]bool{"collect": true, "steer": true, "interrupt": true}
	if !validModes[mode] {
		return c.Send("❌ Invalid mode. Use: collect, steer, interrupt")
	}

	if err := b.store.SetSessionOption(chatID, "queue_mode", mode); err != nil {
		return c.Send("❌ Failed to set queue mode")
	}

	// Optional debounce ms
	if len(args) > 1 {
		if ms, err := strconv.Atoi(args[1]); err == nil && ms >= 0 && ms <= 10000 {
			b.debouncer.SetDelay(chatID, ms)
		}
	}

	return c.Send(fmt.Sprintf("✅ Queue mode set to: `%s`", mode),
		&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleTTSCommand controls text-to-speech
func (b *Bot) handleTTSCommand(c telebot.Context) error {
	args := strings.TrimSpace(c.Message().Payload)

	if args == "" || args == "help" {
		return c.Send(`🔊 *TTS Commands:*

/tts on — Enable auto-TTS
/tts off — Disable auto-TTS
/tts status — Show TTS settings`, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	switch args {
	case "on":
		return c.Send("✅ TTS enabled (not yet fully implemented)")
	case "off":
		return c.Send("✅ TTS disabled")
	case "status":
		return c.Send("🔊 TTS: off (default)")
	default:
		return c.Send("❌ Unknown TTS action. Use: on, off, status, help")
	}
}

// handleRestartCommand restarts the bot process
func (b *Bot) handleRestartCommand(c telebot.Context) error {
	if !b.authManager.IsAdmin(c.Sender().ID) {
		return c.Send("🔒 This command is only available to administrators.")
	}

	c.Send("🔄 Restarting...")
	log.Println("Restart requested via /restart command")

	// Exit with code 0 — assumes a process manager will restart
	go func() {
		os.Exit(0)
	}()

	return nil
}

func (b *Bot) handleEstopCommand(c telebot.Context) error {
	args := strings.Fields(strings.ToLower(strings.TrimSpace(c.Message().Payload)))
	action := "status"
	if len(args) > 0 {
		action = args[0]
	}

	switch action {
	case "status":
		enabled, err := b.store.IsEmergencyStopEnabled()
		if err != nil {
			log.Printf("Failed to load estop state: %v", err)
			return c.Send("❌ Failed to load estop state")
		}
		return c.Send(formatEstopStatus(enabled))
	case "on", "off":
		if !b.authManager.IsAdmin(c.Sender().ID) {
			return c.Send("🔒 This command is only available to administrators.")
		}

		enabled := action == "on"
		if err := b.store.SetEmergencyStopEnabled(enabled); err != nil {
			log.Printf("Failed to update estop state: %v", err)
			return c.Send("❌ Failed to update estop state")
		}
		return c.Send(formatEstopStatus(enabled))
	default:
		return c.Send("❌ Usage: /estop on | /estop off | /estop status")
	}
}

func formatEstopStatus(enabled bool) string {
	families := strings.Join(tools.DangerousToolFamilies(), ", ")
	if enabled {
		return fmt.Sprintf("🛑 estop is ON. Disabled tool families: %s", families)
	}
	return fmt.Sprintf("🟢 estop is OFF. Dangerous tool families enabled: %s", families)
}
