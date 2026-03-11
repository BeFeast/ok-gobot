# Memory Audit Report — ok-gobot Architecture Fixes

**Date:** 2026-03-12  
**Auditor:** Subagent Audit Run  
**Repository:** ok-gobot (Go-based AI personal assistant bot)

---

## Executive Summary

This audit identified **5 critical bugs** causing the reported symptoms:
1. Bot saying "I've completed the requested actions" when doing nothing
2. Forgetting tasks mid-conversation ("What should I do?")
3. Broken memory — no recall of previous sessions

All identified bugs have been fixed with concrete Go code changes. The fixes are prioritized by impact and include test coverage.

---

## Bugs Found and Fixed

### Bug 1: False "Completed" Fallback Message (HIGH IMPACT)

**Location:** `internal/agent/tool_agent.go:263`  
**Symptom:** Bot says "I've completed the requested actions" when it actually did nothing.

**Root Cause:**
The `finalResponse` variable is initialized empty and only set when the model returns a non-tool-call response with content. If:
- The model returns empty content after tool execution
- The max iterations (10) are exhausted with only tool calls
- Any error path leaves `finalResponse` unset

Then the fallback triggers: `if finalResponse == "" { finalResponse = "I've completed the requested actions." }`

**Fix Applied:**
```go
// Added completion tracking
var completed bool

// In the final response handling:
finalResponse = strings.TrimSpace(StripThinkTags(message.Content))
completed = true
break

// In the fallback:
if finalResponse == "" {
    switch {
    case len(toolResults) > 0 && !completed:
        finalResponse = "⚠️ I executed tools but didn't receive a final analysis response from the model. Here are the raw tool results:\n\n" + strings.Join(toolResults, "\n\n")
    case len(toolResults) > 0:
        finalResponse = "⚠️ I executed tools, but the model returned an empty final message."
    default:
        finalResponse = "⚠️ I couldn't generate a response (empty model output). Please retry."
    }
}
```

**Test Added:** `TestToolCallingAgent_DoesNotUseFalseCompletedFallback` — verifies that the endless tool call loop doesn't produce the false completion message.

---

### Bug 2: MEMORY.md Not Loaded (CRITICAL — Causes Amnesia)

**Location:** `internal/bootstrap/loader.go:29`  
**Symptom:** Bot doesn't remember anything from previous sessions.

**Root Cause:**
`filesToLoad` slice (the files actually loaded into the system prompt) was missing `MEMORY.md`:
```go
var filesToLoad = []string{
    "SOUL.md",
    "IDENTITY.md",
    "USER.md",
    "AGENTS.md",
    "TOOLS.md",
    "HEARTBEAT.md",  // MEMORY.md was missing!
}
```

The file was in `managedFiles` (for scaffolding) but not in `filesToLoad` (for runtime loading). The model only saw MEMORY.md if it proactively called `memory_search`, which it often didn't.

**Fix Applied:**
```go
var filesToLoad = []string{
    "SOUL.md",
    "IDENTITY.md",
    "USER.md",
    "AGENTS.md",
    "TOOLS.md",
    "MEMORY.md",    // ← ADDED
    "HEARTBEAT.md",
}
```

Additionally, updated `SystemPrompt()` to include MEMORY.md and daily notes:
```go
if memory, ok := l.Files["MEMORY.md"]; ok {
    prompt.WriteString("## LONG-TERM MEMORY\n\n")
    prompt.WriteString(memory)
    prompt.WriteString("\n\n")
}

// Also include today + yesterday daily notes
today := l.currentTime().Format("2006-01-02")
yesterday := l.currentTime().AddDate(0, 0, -1).Format("2006-01-02")
for _, date := range []string{today, yesterday} {
    key := "memory/" + date + ".md"
    if note, ok := l.Files[key]; ok {
        prompt.WriteString("## DAILY MEMORY: ")
        prompt.WriteString(date)
        prompt.WriteString("\n\n")
        prompt.WriteString(note)
        prompt.WriteString("\n\n")
    }
}
```

**Tests Updated:** `TestNewPersonality_LoadsMemoryMD`, `TestGetSystemPrompt_IncludesMemoryAndUsesBootstrapOrder`

---

### Bug 3: Fallback Message Saved to History (HIGH IMPACT)

**Location:** `internal/bot/hub_handler.go:309-318`  
**Symptom:** After false "completed" message, next turn starts with poisoned context, confusing the model.

**Root Cause:**
The fallback message "I've completed the requested actions" was being saved to the transcript, poisoning the conversation history. On the next turn, the model sees this as its own previous response and gets confused.

**Fix Applied:**
```go
// Detect and skip persisting the synthetic fallback
persistMessage := strings.TrimSpace(result.Message)
shouldPersist := persistMessage != "I've completed the requested actions."

if shouldPersist {
    if err := b.store.SaveSession(chatID, result.Message); err != nil {
        log.Printf("[bot] failed to save session: %v", err)
    }
    if err := b.store.SaveSessionMessagePairV2(string(sessionKey), content, result.Message); err != nil {
        log.Printf("[bot] failed to persist v2 transcript: %v", err)
    }
} else {
    log.Printf("[bot] skipping transcript persistence for synthetic fallback response")
}
```

---

### Bug 4: maxFileChars = 8000 — Too Small (MEDIUM-HIGH IMPACT)

**Location:** `internal/bootstrap/loader.go:17`  
**Symptom:** Important files like MEMORY.md and AGENTS.md are truncated mid-content, losing critical context.

**Root Cause:**
The 8000 character limit truncates large files. MEMORY.md can easily exceed this, and the truncation happens silently with just a "... [truncated X chars] ..." notice.

**Fix Applied:**
```go
// Keep substantially more context from bootstrap files; 8k was truncating
// MEMORY.md and AGENTS.md in real deployments.
const maxFileChars = 32000
```

This provides 4x more room for bootstrap context before truncation kicks in.

---

### Bug 5: History Limit Too Low (MEDIUM IMPACT)

**Location:** `internal/bot/hub_handler.go:135`  
**Symptom:** Bot forgets tasks mid-conversation — asks "what should I do?" after being told.

**Root Cause:**
Only loading 50 messages of history means that in a conversation with tool calls (which add multiple messages per turn), the model may lose the original user request within just a few turns.

**Fix Applied:**
```go
// Load more history to maintain context across multi-turn tool-heavy conversations
if v2Msgs, err := b.store.GetSessionMessagesV2(string(sessionKey), 120); err == nil && len(v2Msgs) > 0 {
```

**Before:** 50 messages (≈ 10-15 turns with tool calls)  
**After:** 120 messages (≈ 30-40 turns with tool calls)

---

## Summary of Changes

| File | Changes |
|------|---------|
| `internal/bootstrap/loader.go` | Added MEMORY.md to filesToLoad; added daily notes to SystemPrompt(); increased maxFileChars 8000→32000 |
| `internal/agent/tool_agent.go` | Fixed false completion fallback; added completion tracking |
| `internal/bot/hub_handler.go` | Skip persisting synthetic fallback; increased history limit 50→120 |
| `internal/agent/personality_test.go` | Updated tests to expect MEMORY.md loading |
| `internal/agent/tool_agent_test.go` | Added test for false completion fallback |
| `internal/bootstrap/loader_test.go` | Updated tests to expect MEMORY.md in output |

---

## Validation

All critical tests pass:
- `TestNewPersonality_LoadsMemoryMD` ✓
- `TestGetSystemPrompt_IncludesMemoryAndUsesBootstrapOrder` ✓
- `TestBuildSystemPrompt_OrdersSkillsAfterAgentsAndMentionsMemoryTools` ✓
- `TestToolCallingAgent_DoesNotUseFalseCompletedFallback` ✓
- All existing ToolCallingAgent tests ✓
- All bootstrap loader tests ✓

---

## Impact Assessment

| Bug | Impact | User-Facing? | Fixed |
|-----|--------|--------------|-------|
| False "completed" fallback | High | Yes | ✓ |
| MEMORY.md not loaded | Critical | Yes (amnesia) | ✓ |
| Fallback saved to history | High | Yes (poisoned context) | ✓ |
| maxFileChars too low | High | Yes (truncated memory) | ✓ |
| History limit too low | Medium | Yes (forgets tasks) | ✓ |

All reported symptoms should be resolved after these fixes are deployed.
