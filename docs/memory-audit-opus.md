# ok-gobot Architecture Audit: Memory, Context & Agent Loop

**Date:** 2026-03-12  
**Auditor:** Claude Opus (subagent)  
**Scope:** Memory pipeline, agent tool loop, session history, system prompt assembly  

---

## Executive Summary

ok-gobot has **5 confirmed bugs** and **3 additional issues** that together explain all three user-reported symptoms:
1. "I've completed the requested actions" when nothing happened
2. Forgetting tasks mid-conversation
3. No memory across sessions

The **single most impactful fix** is Bug 1 (the empty-response fallback) because it's the root cause of the poisoned-history chain reaction. But all bugs interact — fixing only one won't make the bot production-quality.

**Priority order:** Bug 1 → Bug 3 → Bug 5 → Bug 2 → Bug 4 → Bug 6 → Bug 7 → Bug 8

---

## Bug 1: Hardcoded Empty-Response Fallback [CRITICAL] ✅ VALIDATED

**File:** `internal/agent/tool_agent.go:262-263`

```go
if finalResponse == "" {
    finalResponse = "I've completed the requested actions."
}
```

### Root Cause Analysis

`finalResponse` becomes empty in **three scenarios**:

1. **maxIterations exhausted (10 loops of tool calls):** The model keeps calling tools but never produces a text-only response. The loop exits without setting `finalResponse`. This happens when:
   - Tool calls return errors and the model retries the same tool
   - The model calls non-existent tools (the error "tool not found: X" is returned as a tool result, model retries)
   - Long chains of dependent tool calls that genuinely need >10 iterations

2. **Model returns empty content:** `message.Content` is `""` after `StripThinkTags()`. Some models return think-only responses (`<think>...</think>`) with no visible content, which `StripThinkTags` strips to empty.

3. **All tool calls succeed but model's final response is empty:** Edge case where the model's last non-tool-call response has `Content: ""`.

### Why This Is Critical

The fallback text is **semantically false** — it claims actions were completed when they may have failed or never started. This lie gets saved to history (see Bug 3), poisoning all future turns.

### Fix

```go
// Replace lines 261-264 in internal/agent/tool_agent.go:

// OLD:
if finalResponse == "" {
    finalResponse = "I've completed the requested actions."
}

// NEW:
if finalResponse == "" {
    if len(usedTools) > 0 {
        // Tools were called but the model couldn't produce a summary.
        // Include the raw tool results so the user knows what happened.
        summary := strings.Join(toolResults, "\n\n")
        if len(summary) > 2000 {
            summary = summary[:2000] + "\n... (truncated)"
        }
        finalResponse = fmt.Sprintf(
            "⚠️ I called %d tool(s) (%s) but couldn't generate a summary. Raw results:\n\n%s",
            len(usedTools),
            strings.Join(usedTools, ", "),
            summary,
        )
    } else {
        // No tools called, no response — the model simply failed to respond.
        finalResponse = "⚠️ I wasn't able to generate a response. Could you rephrase your request?"
    }
}
```

Additionally, add iteration-exhaustion detection **before** the fallback:

```go
// Add RIGHT AFTER the for loop (after line 260), before the if finalResponse == "" block:

if finalResponse == "" && len(usedTools) > 0 {
    logger.Warnf("ToolAgent: exhausted %d iterations with %d tool calls, no final text response",
        maxIterations, len(usedTools))
}
```

---

## Bug 2: MEMORY.md Excluded from System Prompt [HIGH] ✅ VALIDATED

**File:** `internal/bootstrap/loader.go:27-34`

```go
var filesToLoad = []string{
    "SOUL.md",
    "IDENTITY.md",
    "USER.md",
    "AGENTS.md",
    "TOOLS.md",
    "HEARTBEAT.md",
}
// MEMORY.md is in managedFiles but NOT in filesToLoad
```

### Confirmed Behavior

- `MEMORY.md` is listed in `managedFiles` (line 19-25) — so `scaffold` creates it
- But it's NOT in `filesToLoad` — so `Loader.loadFiles()` never reads it
- The test (`loader_test.go:43-44`) **explicitly asserts** MEMORY.md is NOT loaded
- The model can only access MEMORY.md via `memory_search` tool (embedding-based semantic search) or `memory_get` tool (file read)

### Why This Matters

The system prompt instructs the model to use `memory_search` before answering questions about prior work. But:
- The model often **skips** the memory_search call for "simple" questions
- Semantic search is lossy — it returns chunks, not the full context
- The model has no awareness of what's IN memory without first searching

### Fix

Add MEMORY.md to `filesToLoad` **and** render it in `SystemPrompt()`:

```go
// internal/bootstrap/loader.go — change filesToLoad:
var filesToLoad = []string{
    "SOUL.md",
    "IDENTITY.md",
    "USER.md",
    "AGENTS.md",
    "TOOLS.md",
    "HEARTBEAT.md",
    "MEMORY.md",
}

// internal/bootstrap/loader.go — add to SystemPrompt() after AGENTS block (~line 184):
if memory, ok := l.Files["MEMORY.md"]; ok {
    prompt.WriteString("## LONG-TERM MEMORY\n\n")
    prompt.WriteString(memory)
    prompt.WriteString("\n\n")
}
```

**Update the test** (`loader_test.go:43-47`) to expect MEMORY.md to be present:

```go
// Remove these assertions:
// if loader.HasFile("MEMORY.md") {
//     t.Fatalf("MEMORY.md should not be loaded into bootstrap files")
// }
// Replace with:
if !loader.HasFile("MEMORY.md") {
    t.Fatalf("MEMORY.md should be loaded into bootstrap files")
}
if got := loader.SystemPrompt(); !strings.Contains(got, "Memory line") {
    t.Fatalf("SystemPrompt() should contain MEMORY.md content")
}
```

⚠️ **Caveat:** With `maxFileChars = 8000`, a large MEMORY.md will be truncated. See Bug 4.

---

## Bug 3: Poisoned History [CRITICAL] ✅ VALIDATED

**File:** `internal/bot/hub_handler.go:310`

```go
if err := b.store.SaveSessionMessagePairV2(string(sessionKey), content, result.Message); err != nil {
```

### The Chain Reaction

1. User asks: "Search for flights to London"
2. Agent loop exhausts 10 iterations (tool errors, retries)
3. `finalResponse` is empty → fallback: "I've completed the requested actions."
4. `hub_handler.go` saves pair: `("Search for flights", "I've completed the requested actions.")`
5. Next turn, `GetSessionMessagesV2` loads this into history
6. Model sees: User asked for flights → Assistant completed it → thinks flight search is done
7. User asks "what did you find?" → Model says "I already completed that" or asks "what should I do?"

### Fix

Filter fallback messages before persisting to v2 transcript:

```go
// internal/bot/hub_handler.go — replace the SaveSessionMessagePairV2 block (~line 308-311):

// Only persist to v2 transcript if the response is genuine (not a fallback).
// Fallback messages pollute history and cause the model to "forget" tasks.
if !strings.HasPrefix(result.Message, "⚠️") || result.ToolUsed {
    if err := b.store.SaveSessionMessagePairV2(string(sessionKey), content, result.Message); err != nil {
        log.Printf("[bot] failed to persist v2 transcript: %v", err)
    }
}
```

**Better approach** — add a flag to `AgentResponse`:

```go
// internal/agent/tool_agent.go — add field to AgentResponse:
type AgentResponse struct {
    Message          string
    ToolUsed         bool
    ToolName         string
    ToolResult       string
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
    IsFallback       bool // true when the response is a synthetic fallback, not model-generated
}

// Set it in the fallback block:
if finalResponse == "" {
    // ... (the new fallback code from Bug 1 fix)
    return &AgentResponse{
        Message:    finalResponse,
        ToolUsed:   len(usedTools) > 0,
        ToolName:   strings.Join(usedTools, ", "),
        ToolResult: strings.Join(toolResults, "\n\n"),
        IsFallback: true,
        // ... token counts
    }, nil
}

// In hub_handler.go:
if !result.IsFallback {
    if err := b.store.SaveSessionMessagePairV2(string(sessionKey), content, result.Message); err != nil {
        log.Printf("[bot] failed to persist v2 transcript: %v", err)
    }
}
```

---

## Bug 4: maxFileChars = 8000 Truncates Bootstrap Files [MEDIUM] ✅ VALIDATED

**File:** `internal/bootstrap/loader.go:16`

```go
const maxFileChars = 8000
```

### Impact

8000 chars ≈ 2000 tokens. For files like AGENTS.md, TOOLS.md, or MEMORY.md that grow over time, the truncation `truncateWithPreservation` keeps the first 4000 and last 4000 chars — which **loses the middle** of the file where important content lives.

### Fix

```go
// internal/bootstrap/loader.go:
const (
    maxFileChars = 24000  // ~6000 tokens; generous for bootstrap context files
)
```

If context budget is a concern, make it configurable:

```go
// Add to Loader struct:
type Loader struct {
    BasePath     string
    Files        map[string]string
    Skills       []SkillEntry
    MaxFileChars int  // 0 = use default
    now          func() time.Time
}

// In loadFiles():
maxChars := l.MaxFileChars
if maxChars <= 0 {
    maxChars = 24000
}
l.Files[filename] = truncateWithPreservation(string(content), maxChars)
```

---

## Bug 5: Daily Memory Files Loaded but Never Rendered in System Prompt [HIGH] 🆕 NEW

**File:** `internal/bootstrap/loader.go:122-129` + `internal/bootstrap/prompt.go`

### Evidence

The loader **correctly reads** today's and yesterday's daily memory files:

```go
// loader.go:122-129
now := l.currentTime()
today := now.Format("2006-01-02")
yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
for _, date := range []string{today, yesterday} {
    path := filepath.Join(l.BasePath, "memory", date+".md")
    content, err := os.ReadFile(path)
    if err == nil {
        l.Files["memory/"+date+".md"] = truncateWithPreservation(string(content), maxFileChars)
    }
}
```

But `SystemPrompt()` only renders: SOUL, IDENTITY, USER, TOOLS, AGENTS. **Daily memory files are loaded into `l.Files` but never emitted.**

Similarly, `HEARTBEAT.md` is in `filesToLoad` and loaded into `l.Files`, but `SystemPrompt()` never renders it.

### Impact

The model has **no awareness of recent daily events** — it can't see what happened today or yesterday unless it calls `memory_search` or `memory_get`. This directly causes the "broken memory" symptom.

### Fix

```go
// internal/bootstrap/loader.go — add to SystemPrompt() after MEMORY section:

// Render daily memory notes (today + yesterday).
now := l.currentTime()
for _, date := range []string{
    now.AddDate(0, 0, -1).Format("2006-01-02"),
    now.Format("2006-01-02"),
} {
    key := "memory/" + date + ".md"
    if dailyContent, ok := l.Files[key]; ok {
        prompt.WriteString(fmt.Sprintf("## Daily Notes: %s\n\n", date))
        prompt.WriteString(dailyContent)
        prompt.WriteString("\n\n")
    }
}

// Render HEARTBEAT.md
if heartbeat, ok := l.Files["HEARTBEAT.md"]; ok {
    prompt.WriteString("## HEARTBEAT\n\n")
    prompt.WriteString(heartbeat)
    prompt.WriteString("\n\n")
}
```

Note: need to add `"fmt"` to imports in loader.go if not already there (it's already imported).

---

## Bug 6: SearchChunks Loads ALL Chunks Into Memory [MEDIUM] 🆕 NEW

**File:** `internal/memory/store.go:394-397`

```go
rows, err := s.db.QueryContext(ctx, `
    SELECT id, source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at
    FROM memory_chunks
    ORDER BY indexed_at DESC
`)
```

### Problem

This query fetches **every single chunk** from the database, decodes all embeddings in Go, and computes cosine similarity in a loop. With hundreds of memory chunks, this means:
- Loading megabytes of embedding data per search
- O(n) cosine similarity computation
- Memory usage grows linearly with history

### Fix (Short-term)

Add a reasonable LIMIT and pre-filter by recency:

```go
// internal/memory/store.go — replace the SearchChunks query:
rows, err := s.db.QueryContext(ctx, `
    SELECT id, source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at
    FROM memory_chunks
    ORDER BY indexed_at DESC
    LIMIT ?
`, topK*20) // fetch 20x candidates for similarity ranking
```

### Fix (Long-term)

Use SQLite's `sqlite-vss` extension or a KNN approach. But the short-term fix is sufficient for hundreds of chunks.

---

## Bug 7: History Limit Hardcoded to 50 Messages [LOW] 🆕 NEW

**File:** `internal/bot/hub_handler.go:135`

```go
if v2Msgs, err := b.store.GetSessionMessagesV2(string(sessionKey), 50); err == nil && len(v2Msgs) > 0 {
```

### Problem

50 messages = 25 turn pairs. For a personal assistant with long conversations, this means:
- History gets silently truncated to the most recent 50 messages
- Older context within the same session is lost
- No compaction is applied — just hard truncation

Combined with the fact that compaction (`/compact` command) returns "not yet implemented" (`commands.go:275`), there's no way to manage context growth.

### Fix

This is more of a design issue than a bug. Short-term:

```go
// Make the limit configurable and increase the default:
const defaultHistoryLimit = 100

// In hub_handler.go:
if v2Msgs, err := b.store.GetSessionMessagesV2(string(sessionKey), defaultHistoryLimit); err == nil && len(v2Msgs) > 0 {
```

Long-term: Implement actual compaction in the `/compact` command using the existing `Compactor` struct.

---

## Bug 8: Compaction Not Implemented [LOW] 🆕 NEW

**File:** `internal/bot/commands.go:275`

```go
return c.Send("🧹 Compaction not yet implemented. Use /new to start fresh.")
```

The `Compactor` struct exists (`internal/agent/compactor.go`) and works, but it's never wired into the `/compact` command. This means the only way to manage context is `/new` which loses ALL history.

### Fix

Wire the compactor into the command:

```go
// internal/bot/commands.go — replace handleCompactCommand:
func (b *Bot) handleCompactCommand(c telebot.Context) error {
    chatID := c.Chat().ID
    sessionKey := sessionKeyForChat(c.Chat())

    messages, err := b.store.GetSessionMessagesV2(string(sessionKey), 200)
    if err != nil || len(messages) < 4 {
        return c.Send("ℹ️ Not enough conversation to compact.")
    }

    // Convert to ai.Message for compactor
    aiMsgs := make([]ai.Message, len(messages))
    for i, m := range messages {
        aiMsgs[i] = ai.Message{Role: m.Role, Content: m.Content}
    }

    compactor := agent.NewCompactor(b.defaultAIClient, b.defaultModel)
    result, err := compactor.Compact(c.Context(), aiMsgs)
    if err != nil {
        return c.Send(fmt.Sprintf("❌ Compaction failed: %v", err))
    }

    // Clear old messages and insert summary
    if err := b.store.ClearSessionMessagesV2(string(sessionKey)); err != nil {
        return c.Send(fmt.Sprintf("❌ Failed to clear old messages: %v", err))
    }

    // Save the compacted summary as a system message
    if err := b.store.SaveSessionMessageV2(string(sessionKey), "assistant", 
        "[Compacted conversation summary]\n\n"+result.Summary, ""); err != nil {
        return c.Send(fmt.Sprintf("❌ Failed to save summary: %v", err))
    }

    return c.Send(result.FormatNotification())
}
```

Note: This requires adding a `ClearSessionMessagesV2` method to the store (simple `DELETE FROM session_messages_v2 WHERE session_key = ?`).

---

## Summary Table

| # | Bug | Severity | File | Status |
|---|-----|----------|------|--------|
| 1 | Empty-response fallback lies to user | CRITICAL | tool_agent.go:262 | Validated |
| 2 | MEMORY.md excluded from system prompt | HIGH | loader.go:27 | Validated |
| 3 | Poisoned history from fallback messages | CRITICAL | hub_handler.go:310 | Validated |
| 4 | maxFileChars = 8000 truncates files | MEDIUM | loader.go:16 | Validated |
| 5 | Daily memory loaded but never rendered | HIGH | loader.go + prompt.go | **NEW** |
| 6 | SearchChunks loads ALL rows | MEDIUM | store.go:394 | **NEW** |
| 7 | History limit hardcoded at 50 | LOW | hub_handler.go:135 | **NEW** |
| 8 | Compaction not implemented | LOW | commands.go:275 | **NEW** |

---

## The Single Most Impactful Fix

**Bug 1 + Bug 3 together.** They form a feedback loop:

1. Bug 1 generates a false "completed" message
2. Bug 3 saves it to history
3. Next turn, model sees the lie and gets confused
4. This causes symptoms 1 AND 2 (false completion + forgetting tasks)

Fix Bug 1 first (honest fallback), then Bug 3 (don't persist fallbacks). Together these eliminate the root cause of 2 out of 3 reported symptoms.

For symptom 3 (broken memory), fix Bugs 2 and 5 — include MEMORY.md and daily notes in the system prompt.

---

## Implementation Order

1. **Bug 1** — Change fallback to honest error message (5 min fix)
2. **Bug 3** — Add `IsFallback` flag, don't persist fallbacks (10 min fix)
3. **Bug 5** — Render daily memory in SystemPrompt (5 min fix)
4. **Bug 2** — Add MEMORY.md to filesToLoad + SystemPrompt (5 min fix)
5. **Bug 4** — Increase maxFileChars to 24000 (1 min fix)
6. **Bug 6** — Add LIMIT to SearchChunks query (2 min fix)
7. **Bug 7** — Increase history limit (1 min fix)
8. **Bug 8** — Wire compactor to /compact (30 min fix)

**Total estimated time: ~1 hour for all fixes.**
