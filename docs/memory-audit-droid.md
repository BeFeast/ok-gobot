# ok-gobot Architecture Audit: Memory, Context & Agent Loop

**Date:** 2026-03-12
**Auditor:** Droid (Claude Opus 4.6)
**Scope:** Memory pipeline, agent tool loop, session history, system prompt assembly
**Branch reviewed:** `fix/code-review-findings` (commit `2b3344d`)

---

## Executive Summary

I reviewed the same codebase as Shraga and Opus, but **against the current branch state** including the uncommitted fixes. This is a critical distinction: both prior audits were written against the pre-fix baseline. Five of the eight originally identified bugs have already been patched on this branch. However, the fixes themselves introduced a **new critical bug** that neither prior audit could have caught.

**Verdict:** The branch is 70% of the way there. Three bugs remain open, one of which is a regression introduced by the partial fix. The minimum delta to OpenClaw parity is small: fix the poisoned-history guard, render HEARTBEAT.md, and cap SearchChunks.

---

## Part 1: Validation of Prior Audit Findings

### What Both Models Agreed On (Correct)

Both Shraga and Opus independently identified the same core chain reaction:

1. Empty-response fallback lies to user -> saved to history -> poisons future turns
2. MEMORY.md excluded from system prompt -> no long-term context
3. Daily notes loaded but never rendered

They agree on root cause and priority order. Their analysis is sound.

### Where They Diverge

| Topic | Shraga | Opus | My Take |
|-------|--------|------|---------|
| Priority ordering | Bug1 -> Bug2 -> Bug3 | Bug1 -> Bug3 -> Bug5 -> Bug2 | Opus is right to prioritize Bug5 (daily notes) above Bug2 (MEMORY.md). Daily notes contain recent session context; MEMORY.md is supplemental. |
| maxFileChars fix | Per-file limits map | Single increase to 24000 | The branch went with 32000. Single value is simpler and sufficient. Per-file limits add complexity for marginal benefit. |
| Fallback fix approach | Diagnostic message in Russian | Structured fallback with raw tool results | Opus's approach is better — returning raw tool results gives the user actionable information instead of just a diagnostic label. |
| Poisoned history fix | `IsErrorFallback` flag on `AgentResponse` | `IsFallback` flag on `AgentResponse` | Same idea, different names. Both are correct in principle. The branch took a different (broken) approach — see Bug A below. |
| SearchChunks | Not mentioned | Mentioned (Bug 6) | Opus caught this; Shraga missed it. Real issue. |
| Compaction wiring | Not mentioned | Mentioned (Bug 8) | Opus caught this too. Low priority but legitimate. |

---

## Part 2: Current State of the Branch

### Already Fixed (5 of 8 original bugs)

The `git diff HEAD` shows these fixes are already applied:

| Original Bug | Fix Applied | File | Status |
|---|---|---|---|
| Bug 1: "I've completed" fallback | Replaced with 3 `"⚠️ ..."` variants | `tool_agent.go:261-270` | FIXED |
| Bug 2: MEMORY.md not loaded | Added to `filesToLoad` + rendered in `SystemPrompt()` | `loader.go:37,180-184` | FIXED |
| Bug 4: maxFileChars = 8000 | Increased to 32000 | `loader.go:16` | FIXED |
| Bug 5: Daily notes not rendered | Added rendering loop in `SystemPrompt()` | `loader.go:186-197` | FIXED |
| Bug 7: History limit 50 | Increased to 120 | `hub_handler.go:135` | FIXED |

### Still Open (3 bugs + 1 regression)

---

## Part 3: Bugs That Remain (Including New Findings)

### BUG A: Poisoned History Guard Checks Wrong String [CRITICAL] -- NEW REGRESSION

**File:** `internal/bot/hub_handler.go:306`

This is the most important finding in this audit. The branch fixed Bug 1 (changing the fallback from `"I've completed the requested actions."` to `"⚠️ ..."` messages) and separately added a guard against poisoned history. But the guard checks for the **old** string:

```go
// hub_handler.go:306 — CURRENT CODE ON BRANCH
shouldPersist := persistMessage != "I've completed the requested actions."
```

Since `tool_agent.go` no longer produces this string, `shouldPersist` is **always true**. Every fallback message (all three `"⚠️ ..."` variants) will be saved to history. The poisoned history problem is **not fixed**.

**Chain reaction (still active):**
1. Model hits maxIterations -> `finalResponse = ""` -> fallback: `"⚠️ I executed tools but didn't receive a final analysis..."`
2. hub_handler checks: `"⚠️ I executed tools..." != "I've completed the requested actions."` -> **true** -> persists
3. Next turn, model sees `"⚠️ I executed tools..."` in history -> gets confused about conversation state
4. User asks follow-up -> model may ask "what should I do?" because history shows a failure state

**Fix — use prefix matching for the new fallback format:**

```go
// internal/bot/hub_handler.go — replace line 306:

// OLD (broken — checks for string that no longer exists):
shouldPersist := persistMessage != "I've completed the requested actions."

// NEW (matches all synthetic fallback variants):
shouldPersist := !strings.HasPrefix(persistMessage, "⚠️")
```

This is a minimal fix. The structurally correct approach (both Shraga and Opus suggested it) is an `IsFallback` field on `AgentResponse`. Here's the full version:

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
    IsFallback       bool
}

// In ProcessRequestWithContent, where fallbacks are generated (lines 261-270):
if finalResponse == "" {
    isFallback := true  // track this
    switch {
    case len(toolResults) > 0 && !completed:
        finalResponse = "⚠️ I executed tools but didn't receive a final analysis response from the model. Here are the raw tool results:\n\n" + strings.Join(toolResults, "\n\n")
    case len(toolResults) > 0:
        finalResponse = "⚠️ I executed tools, but the model returned an empty final message."
    default:
        finalResponse = "⚠️ I couldn't generate a response (empty model output). Please retry."
    }
    return &AgentResponse{
        Message:          finalResponse,
        ToolUsed:         len(usedTools) > 0,
        ToolName:         strings.Join(usedTools, ", "),
        ToolResult:       strings.Join(toolResults, "\n\n"),
        PromptTokens:     lastPromptTokens,
        CompletionTokens: totalCompletionTokens,
        TotalTokens:      lastTotalTokens,
        IsFallback:       isFallback,
    }, nil
}

// internal/bot/hub_handler.go — replace the shouldPersist block:
shouldPersist := !result.IsFallback
if shouldPersist {
    if err := b.store.SaveSession(chatID, result.Message); err != nil {
        log.Printf("[bot] failed to save session: %v", err)
    }
    if err := b.store.SaveSessionMessagePairV2(string(sessionKey), content, result.Message); err != nil {
        log.Printf("[bot] failed to persist v2 transcript: %v", err)
    }
} else {
    log.Printf("[bot] skipping transcript persistence for fallback response")
}
```

**Recommendation:** Use the `IsFallback` field approach. The prefix-match is fragile (what if a legitimate response starts with the warning emoji?). The `IsFallback` field is explicit, type-safe, and future-proof.

---

### BUG B: HEARTBEAT.md Loaded but Never Rendered [MEDIUM]

**File:** `internal/bootstrap/loader.go`

`HEARTBEAT.md` is in both `managedFiles` (line 28) and `filesToLoad` (line 38). The file is read from disk and stored in `l.Files["HEARTBEAT.md"]`. But `SystemPrompt()` has **no section** that renders it.

This is a silent data loss: the file is read, memory is allocated, but the content never reaches the model.

Neither Shraga nor Opus explicitly called this out as a separate bug. Opus's fix for Bug 5 included a HEARTBEAT rendering block, but the actual branch fix did not implement it.

**Impact:** HEARTBEAT.md contains the heartbeat/polling checklist. Without it in the prompt, the model has no instructions for how to handle scheduled heartbeat checks. The model might still work because AGENTS.md may contain some heartbeat guidance, but the dedicated checklist is lost.

**Fix:**

```go
// internal/bootstrap/loader.go — add to SystemPrompt() AFTER the AGENTS block,
// BEFORE the MEMORY block (around line 178):

if heartbeat, ok := l.Files["HEARTBEAT.md"]; ok {
    prompt.WriteString("## HEARTBEAT\n\n")
    prompt.WriteString(heartbeat)
    prompt.WriteString("\n\n")
}
```

Update the test in `loader_test.go` accordingly — add a HEARTBEAT.md test file and verify it appears in the expected prompt output:

```go
// In TestLoaderBuildsSystemPromptFromFiles, add:
writeTestFile(t, filepath.Join(basePath, "HEARTBEAT.md"), "Heartbeat line")

// Update expected string to include:
// ... after "## AGENT PROTOCOL\n\nAgents line\n\n"
// add: "## HEARTBEAT\n\nHeartbeat line\n\n"
// ... before "## LONG-TERM MEMORY\n\nMemory line\n\n"
```

---

### BUG C: SearchChunks Loads Entire Table Into Memory [MEDIUM]

**File:** `internal/memory/store.go:394-397`

```go
rows, err := s.db.QueryContext(ctx, `
    SELECT id, source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at
    FROM memory_chunks
    ORDER BY indexed_at DESC
`)
```

This fetches ALL rows, decodes ALL embeddings (binary -> float32 slice per row), and computes cosine similarity in a Go loop. With N chunks each having a 1536-dimension embedding (6KB per row), 1000 chunks means ~6MB of embedding data loaded and decoded per search call.

Shraga missed this entirely. Opus caught it (Bug 6).

**Fix (short-term) — add LIMIT to pre-filter candidates:**

```go
// internal/memory/store.go — replace the SearchChunks query:

rows, err := s.db.QueryContext(ctx, `
    SELECT id, source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at
    FROM memory_chunks
    ORDER BY indexed_at DESC
    LIMIT ?
`, topK*20)
```

This limits to `topK * 20` candidates (e.g. 100 for topK=5), then ranks by similarity. For a personal bot with hundreds of chunks, this is more than sufficient. The recency bias is actually useful — recent memories are more likely to be relevant.

---

### BUG D: /compact Command Not Wired [LOW]

**File:** `internal/bot/commands.go:275`

```go
return c.Send("Compaction not yet implemented. Use /new to start fresh.")
```

The `Compactor` struct exists and works. The command stub exists. They're just not connected. Opus documented this as Bug 8 with a full implementation. I agree with the analysis but disagree with priority — this is a nice-to-have, not a blocker for the reported symptoms.

**Fix:** See Opus's Bug 8 implementation in `memory-audit-opus.md` — it's correct and complete. Note that it requires adding a `ClearSessionMessagesV2` method to the store, and the `GetSessionMessagesV2` call should use the session key format, not the legacy `GetSessionMessages(chatID, ...)` that's currently in the command.

---

## Part 4: Cross-Audit Assessment

### Do the models agree on root cause?

**Yes, strongly.** All three audits (Shraga, Opus, and this one) converge on the same fundamental chain reaction:

```
Empty fallback -> persisted to history -> model confused on next turn
```

This is the single root cause for symptoms 1 ("I've completed") and 2 ("what should I do?"). Symptom 3 (no memory between sessions) is caused by MEMORY.md and daily notes not being in the system prompt.

### Where my audit differs

1. **I'm auditing the branch, not the baseline.** Both prior audits were written before the fixes landed. Five of the eight bugs they found are already fixed. This changes the priority list entirely.

2. **I found the regression.** The partial fix introduced Bug A (hub_handler checking for the old fallback string). This is arguably worse than having no guard at all, because it creates a false sense of safety — the code looks like it handles fallbacks, but it doesn't.

3. **HEARTBEAT.md rendering** was mentioned in passing by Opus but never tracked as a distinct bug. It is one.

### Where I agree with the prior audits

- The `IsFallback` flag approach is the right structural fix for poisoned history
- 32000 chars is sufficient for maxFileChars (both prior audits wanted increases; the branch delivered)
- SearchChunks needs a LIMIT clause
- `/compact` should be wired but is not urgent

---

## Part 5: Final Prioritized Fix List

Only bugs that remain open on the current branch:

| Priority | Bug | Severity | Effort | File |
|----------|-----|----------|--------|------|
| **P0** | **A: Poisoned history guard checks wrong string** | CRITICAL | 15 min | `tool_agent.go` + `hub_handler.go` |
| **P1** | **B: HEARTBEAT.md not rendered** | MEDIUM | 5 min | `loader.go` + `loader_test.go` |
| **P2** | **C: SearchChunks unbounded** | MEDIUM | 5 min | `store.go` |
| **P3** | **D: /compact not wired** | LOW | 30 min | `commands.go` + `sqlite.go` |

**Minimum viable patch: Bug A alone.** This is the only remaining critical issue. Bugs B-D are quality improvements that can land separately.

---

## Part 6: OpenClaw Parity Assessment

### What OpenClaw loads per request

```
SOUL.md, USER.md, AGENTS.md, MEMORY.md, TOOLS.md, IDENTITY.md, HEARTBEAT.md, memory/YYYY-MM-DD.md (today)
```

### What ok-gobot loads per request (after branch fixes)

```
SOUL.md, IDENTITY.md, USER.md, AGENTS.md, TOOLS.md, MEMORY.md, HEARTBEAT.md (loaded but NOT rendered),
memory/today.md (rendered), memory/yesterday.md (rendered)
```

### Delta to parity

| Feature | OpenClaw | ok-gobot (branch) | Gap |
|---------|----------|-------------------|-----|
| SOUL.md in prompt | Yes | Yes | None |
| IDENTITY.md in prompt | Yes | Yes | None |
| USER.md in prompt | Yes | Yes | None |
| AGENTS.md in prompt | Yes | Yes | None |
| TOOLS.md in prompt | Yes | Yes | None |
| MEMORY.md in prompt | Yes | Yes | None |
| HEARTBEAT.md in prompt | Yes | **Loaded but not rendered** | **Bug B** |
| Daily notes in prompt | Today only | Today + yesterday | ok-gobot is ahead |
| Session continuity | Full multi-turn | Full multi-turn (120 msgs) | None |
| History compaction | Implemented | Stub only | Bug D |
| Fallback honesty | Says "I can't" | **Saves fallback to history** | **Bug A** |

### Minimum architectural delta

**Two fixes to reach functional parity:**

1. Fix Bug A (poisoned history guard) — 15 min
2. Fix Bug B (render HEARTBEAT.md) — 5 min

That's it. The branch has already closed the gap on MEMORY.md, daily notes, file size limits, and history depth. ok-gobot's advantage (yesterday's notes, Telegram-only simplicity, 120-message window) is real. The remaining delta is tiny.

### ok-gobot's structural advantages over OpenClaw

1. **Telegram-only** — no multi-channel routing, no adapter pattern, simpler error handling
2. **Yesterday's notes** — OpenClaw only loads today; ok-gobot loads today + yesterday, giving the model better temporal continuity
3. **Larger context budget** — 32000 chars per file vs OpenClaw's more conservative limits
4. **Streaming + live editing** — the `LiveStreamEditor` gives real-time token-by-token feedback in Telegram, which OpenClaw doesn't do

---

## Appendix: Test Verification

The existing test `TestLoaderBuildsSystemPromptFromFiles` validates the current prompt structure but does **not** include HEARTBEAT.md in its expected output. After fixing Bug B, the test must be updated.

The poisoned history bug (Bug A) has no test coverage. A test should be added:

```go
// internal/bot/hub_handler_test.go (or agent integration test)
func TestFallbackResponseNotPersisted(t *testing.T) {
    // Given: an AgentResponse with IsFallback = true
    result := &agent.AgentResponse{
        Message:    "⚠️ I couldn't generate a response (empty model output). Please retry.",
        IsFallback: true,
    }

    // When: determining whether to persist
    shouldPersist := !result.IsFallback

    // Then: should NOT persist
    if shouldPersist {
        t.Fatal("fallback responses should not be persisted to history")
    }
}
```
