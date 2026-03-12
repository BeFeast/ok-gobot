# Memory & Context Pipeline

How ok-gobot assembles context for every AI request, manages conversation history,
and persists long-term memory. This is the authoritative reference for the full
request lifecycle from incoming Telegram message to model response.

---

## 1. Overview

Every AI request goes through three stages:

```
Telegram message
    │
    ▼
┌─────────────────────────────────────────────┐
│  1. SYSTEM PROMPT ASSEMBLY                  │
│     Bootstrap files → SystemPrompt()        │
│     SOUL + IDENTITY + USER + TOOLS +        │
│     AGENTS + HEARTBEAT + MEMORY.md +        │
│     daily notes (today + yesterday)         │
├─────────────────────────────────────────────┤
│  2. CONVERSATION HISTORY                    │
│     session_messages_v2 → last 120 msgs     │
│     (or legacy single-string fallback)      │
├─────────────────────────────────────────────┤
│  3. AGENT TOOL LOOP                         │
│     Up to 10 tool call iterations           │
│     → final text response                   │
│     → honest fallback if model fails        │
├─────────────────────────────────────────────┤
│  4. PERSISTENCE                             │
│     Save to v2 transcript (if not fallback) │
│     Append to daily memory file             │
│     Update token counters                   │
└─────────────────────────────────────────────┘
```

---

## 2. System Prompt Assembly

### 2.1 Bootstrap Files

The bootstrap loader (`internal/bootstrap/loader.go`) reads files from the personality
directory (default `~/ok-gobot-soul/`) and assembles them into a single system prompt.

**Files loaded (in order):**

| File | Purpose | Section Header |
|------|---------|---------------|
| `SOUL.md` | Core personality, values, behavioral rules | `## SOUL` |
| `IDENTITY.md` | Name, emoji, tone, language preferences | `## IDENTITY` |
| `USER.md` | User profile, preferences, context | `## USER CONTEXT` |
| `TOOLS.md` | Tool-specific instructions and host configs | `## TOOLS REFERENCE` |
| `AGENTS.md` | Agent protocol, multi-step workflow rules | `## AGENT PROTOCOL` |
| `HEARTBEAT.md` | Periodic check checklist, context warnings | `## HEARTBEAT` |
| `MEMORY.md` | Long-term memory (facts, decisions, history) | `## LONG-TERM MEMORY` |
| `memory/YYYY-MM-DD.md` (today) | Today's conversation log | `## DAILY MEMORY: YYYY-MM-DD` |
| `memory/YYYY-MM-DD.md` (yesterday) | Yesterday's conversation log | `## DAILY MEMORY: YYYY-MM-DD` |

All files are optional — missing files are silently skipped.

### 2.2 File Size Limit

Each file is truncated at **32,000 characters** (~8,000 tokens). The truncation
preserves the first 16,000 and last 16,000 characters, marking the gap:

```
[first 16,000 chars]
... [truncated N chars] ...
[last 16,000 chars]
```

This limit is defined by `maxFileChars` in `loader.go`.

### 2.3 Full Prompt Structure

The `BuildPrompt()` function (`internal/bootstrap/prompt.go`) wraps `SystemPrompt()`
with additional runtime sections:

```
[SystemPrompt() output — all bootstrap files]
[Skills summary — if skills/ directory has SKILL.md files]
[Tool definitions — name + description for each registered tool]
[Tool usage guidelines — when mode="full"]
[Memory instructions — "call memory_search before answering about prior work"]
[Reply tags / reactions reference]
[Model aliases]
[Reasoning instructions — when think level != "off"]
[Runtime info — os, arch, date]
```

### 2.4 Prompt Modes

| Mode | What's included |
|------|----------------|
| `full` | Everything above — used for normal requests |
| `minimal` | IDENTITY + SOUL only — used for lightweight operations |
| `none` | Single identity line ("You are Name Emoji.") |

---

## 3. Conversation History

### 3.1 Multi-Turn History (v2)

The primary history source is `session_messages_v2` — a table of
`(session_key, role, content, created_at)` rows.

On each request, `hub_handler.go` loads up to 500 recent messages, then
**trims from the oldest until the total fits within 40% of the model's context
window**. This is token-aware: long messages consume more budget, short messages
leave room for more history.

```go
// Load generous batch, then trim to token budget
v2Msgs := b.store.GetSessionMessagesV2(string(sessionKey), 500)
history = trimHistoryToTokenBudget(history, model)
```

Messages are dropped in pairs (user + assistant) to avoid orphaned roles
that would cause API validation errors.

These are injected between the system prompt and the current user message:

```
[system]  ← SystemPrompt + tools + guidelines
[user]    ← history message 1 (oldest that fits budget)
[assistant] ← history message 2
...
[user]    ← history message N
[user]    ← current user message
```

### 3.2 Legacy Fallback

If v2 history is empty (first message in a new session), the handler falls back to
the legacy `sessions.session` column — a single string containing the last assistant
response. This is injected as an `[assistant]` message.

### 3.3 Session Keys

Session keys follow the canonical format:

| Chat Type | Session Key Format |
|-----------|-------------------|
| Private DM | `dm:<chatID>` |
| Group/Supergroup | `group:<chatID>` |

### 3.4 Session Reset

The `/new` command clears:
- v2 transcript history
- Legacy session string
- Model override
- Agent override
- Token counters

---

## 4. Agent Tool Loop

### 4.1 How It Works

`ToolCallingAgent.ProcessRequestWithContent()` (`internal/agent/tool_agent.go`)
runs the core request loop:

```
1. Build messages array: [system, ...history, user]
2. Call AI model with tool definitions
3. If model returns tool_calls:
   a. Execute each tool
   b. Append tool call + result to messages
   c. Go to step 2 (next iteration)
4. If model returns text (no tool_calls):
   → That's the final response. Done.
5. If 10 iterations exhausted or model returns empty:
   → Return honest fallback (IsFallback=true)
```

### 4.2 Iteration Limit

Maximum **10 iterations** of tool calls per request. This prevents infinite loops
when the model repeatedly calls tools without producing a final answer.

### 4.3 Streaming

When a streaming-capable AI client is available and a delta callback is wired,
responses are streamed token-by-token through a `LiveStreamEditor` that continuously
edits the Telegram "thinking" placeholder message in real time.

### 4.4 Fallback Handling

When the model fails to produce a final text response, the agent returns an
**honest error** instead of a false completion claim:

| Scenario | Fallback Message |
|----------|-----------------|
| Tools called, max iterations hit | "I executed tools but didn't receive a final analysis response..." + raw tool results |
| Tools called, empty final message | "I executed tools, but the model returned an empty final message." |
| No tools called, empty output | "I couldn't generate a response (empty model output). Please retry." |
| API error after tools executed | "Tool executed but model failed to analyze results..." + raw results |

All fallback responses set `IsFallback: true` on `AgentResponse`.

**Why this matters:** The previous behavior was to return `"I've completed the
requested actions."` which is semantically false — the model claimed success when
it had done nothing. This lie was saved to conversation history, causing the model
to lose track of tasks on subsequent turns (the "poisoned history" problem).

---

## 5. Response Persistence

### 5.1 What Gets Saved

After the agent produces a response, `hub_handler.go` persists several things:

| What | Where | Condition |
|------|-------|-----------|
| User + assistant message pair | `session_messages_v2` | Only if `!result.IsFallback` |
| Last assistant message | `sessions.session` (legacy) | Only if `!result.IsFallback` |
| Timestamped conversation log | `memory/YYYY-MM-DD.md` (daily note) | Always |
| Token usage counters | `sessions_v2` | When tokens > 0 |

### 5.2 The IsFallback Guard

Fallback responses (synthetic error messages) are **never persisted** to the v2
transcript or legacy session store. This prevents the "poisoned history" chain:

```go
if !result.IsFallback {
    b.store.SaveSession(chatID, result.Message)
    b.store.SaveSessionMessagePairV2(sessionKey, content, result.Message)
} else {
    log.Printf("[bot] skipping transcript persistence for synthetic fallback response")
}
```

The daily memory file still gets the fallback entry (for debugging/audit purposes),
but it doesn't affect the conversation history that the model sees.

### 5.3 Silent Replies

If the model returns `SILENT_REPLY` or `HEARTBEAT_OK`, the response is suppressed
entirely — no Telegram message sent, no history saved. The "thinking" placeholder
is deleted.

---

## 6. Memory Architecture

ok-gobot has three layers of memory, each serving a different purpose:

### 6.1 Bootstrap Memory (System Prompt)

Files loaded directly into the system prompt on every request:

| File | Content | Lifetime |
|------|---------|----------|
| `MEMORY.md` | Long-term facts, decisions, user preferences | Persistent, manually curated |
| `memory/today.md` | Today's conversation log | Auto-appended each response |
| `memory/yesterday.md` | Yesterday's conversation log | Rolls over daily |

**This is the primary memory mechanism.** The model sees this content without
needing to call any tools. It provides continuity within and between sessions.

### 6.2 Conversation History (v2 Transcript)

The last 120 messages from `session_messages_v2`, loaded as chat history.
This provides multi-turn continuity within a single session.

- Persisted per session key in SQLite
- Cleared by `/new` command
- Not affected by bot restarts (persistent)
- Fallback messages excluded (IsFallback guard)

### 6.3 Semantic Memory (Vector Search)

Optional embedding-based memory for precise retrieval:

```
User query → embedding → cosine similarity search → top-K chunks
```

**Components:**
- `memory_search` tool — semantic search over indexed markdown chunks
- `memory_get` tool — retrieve specific chunks by source + header path
- `MemoryStore` — SQLite-backed chunk storage with binary embeddings
- `MemoryManager` — coordinates embedding client and store
- `Indexer` — parses markdown files into chunks, generates embeddings, upserts to store

**When to use which:**

| Need | Mechanism |
|------|-----------|
| "What's the user's name?" | MEMORY.md in system prompt (instant) |
| "What did we discuss today?" | Daily notes in system prompt (instant) |
| "What happened 3 days ago?" | `memory_search` tool (requires tool call) |
| "What was the SSH config decision?" | `memory_search` tool (semantic match) |

### 6.4 Daily Memory Lifecycle

```
1. Bot starts → EnsureTodayNote() creates memory/YYYY-MM-DD.md with header
2. Each response → AppendToToday() adds timestamped entry:
     ## HH:MM
     Assistant (agent_name): <response> [Tool: tool_name]
3. Next request → Loader reads today.md + yesterday.md into system prompt
4. Day rolls over → yesterday becomes "2 days ago" (not loaded), today is fresh
```

### 6.5 Memory File Management

| Operation | How |
|-----------|-----|
| Read MEMORY.md | Automatic (system prompt) |
| Write MEMORY.md | `file` tool or `memory_update` tool |
| Read daily notes | Automatic (system prompt for today/yesterday) |
| Write daily notes | Automatic (AppendToToday after each response) |
| Quick note | `/note <text>` command |
| Search old memories | `memory_search` tool |
| Index markdown files | Automatic on startup or via reindex command |

---

## 7. Context Budget

### 7.1 Token Composition

A typical request's token budget breaks down as:

```
┌─────────────────────────────────────┐
│ System prompt                       │  ~2,000-8,000 tokens
│   SOUL.md          ~500-1,500      │
│   IDENTITY.md      ~200-500       │
│   USER.md          ~200-1,000     │
│   TOOLS.md         ~200-500       │
│   AGENTS.md        ~500-2,000     │
│   HEARTBEAT.md     ~100-300       │
│   MEMORY.md        ~500-4,000     │
│   Daily notes      ~200-2,000     │
├─────────────────────────────────────┤
│ Tool definitions   ~500-2,000      │
│ Prompt guidelines  ~500-1,000      │
├─────────────────────────────────────┤
│ Conversation history (120 msgs)    │  ~2,000-30,000 tokens
├─────────────────────────────────────┤
│ Current user message               │  ~50-2,000 tokens
├─────────────────────────────────────┤
│ Available for completion           │  remainder
└─────────────────────────────────────┘
```

### 7.2 Model Context Limits

Model-aware limits are defined in `internal/agent/tokens.go`:

| Model Family | Context Window |
|-------------|---------------|
| GPT-4o | 128K |
| Claude 3.5/4 | 200K |
| Gemini 1.5/2 | 1M |
| Kimi K2 | 131K |
| Default | 128K |

### 7.3 Context Monitoring

The `/context` command shows current usage as a percentage of the model's limit.
The session monitor (`internal/session/monitor.go`) produces warnings:

- Under 70%: normal operation
- 70-85%: warning, suggest `/compact`
- Over 85%: critical, strongly recommend `/compact`

### 7.4 Compaction

The `Compactor` (`internal/agent/compactor.go`) summarizes conversation history
using an AI call. It preserves facts, decisions, names, and dates while removing
redundancy.

The `/compact` command:
1. Loads all v2 transcript messages for the session
2. Calls the AI to summarize the conversation
3. Clears the old messages from `session_messages_v2`
4. Inserts a single assistant message with the compacted summary
5. Updates the compaction counter

The result is reported to the user:
```
Tokens saved: 12000 → 800 (11200 saved)
```

---

## 8. Request Lifecycle (End to End)

Complete flow for a Telegram DM message:

```
1. Telegram sends update to bot
2. Bot handler receives message
3. Fragment buffer checks for split messages (waits 1.5s)
4. Rate limiter checks (10 req/min per chat)
5. Debouncer batches rapid messages (1.5s window)
6. Auth check (open/allowlist/pairing)
7. Command check (/status, /new, etc. handled directly)
8. Session key resolved: dm:<chatID>
9. Send ⏳ placeholder message (ack)
10. Start typing indicator

--- processViaHub ---

11. Load v2 history (last 120 messages)
12. Submit to RuntimeHub as RunRequest
13. Hub resolves agent profile for session
14. Agent builds system prompt:
    a. Loader reads bootstrap files from disk
    b. Loader reads today + yesterday daily notes
    c. BuildPrompt() assembles full prompt with tools, guidelines
15. Agent builds message array:
    [system] + [history...] + [user message]
16. Tool loop (up to 10 iterations):
    a. Call AI model with messages + tool definitions
    b. If tool_calls → execute tools → append results → loop
    c. If text response → done
    d. If empty/exhausted → honest fallback (IsFallback=true)

--- Response rendering ---

17. Strip SILENT_REPLY / HEARTBEAT_OK sentinels
18. Parse [[reply_to:...]] and [[react:...]] tags
19. Stop live streaming editor
20. Edit ⏳ placeholder with final response (or send new message)
21. Set emoji reactions if requested
22. Append to daily memory file (always)
23. If !IsFallback:
    a. Save to legacy session store
    b. Save user+assistant pair to v2 transcript
24. Update token usage counters
25. Log completion
```

---

## 9. File Reference

| File | Responsibility |
|------|---------------|
| `internal/bootstrap/loader.go` | Load bootstrap files, build SystemPrompt() |
| `internal/bootstrap/prompt.go` | Build full prompt with tools and guidelines |
| `internal/bootstrap/scaffold.go` | Create default bootstrap directory structure |
| `internal/agent/tool_agent.go` | Agent tool loop, fallback handling, AgentResponse |
| `internal/agent/memory.go` | Daily notes read/write, MEMORY.md access |
| `internal/agent/compactor.go` | Context compaction via AI summarization |
| `internal/agent/tokens.go` | Token counting, model context limits |
| `internal/bot/hub_handler.go` | Request orchestration, history loading, persistence |
| `internal/memory/store.go` | SQLite chunk storage, cosine similarity search |
| `internal/memory/manager.go` | Embedding + store coordination |
| `internal/memory/embeddings.go` | OpenAI-compatible embedding API client |
| `internal/memory/indexer.go` | Markdown → chunks → embeddings pipeline |
| `internal/storage/sqlite.go` | Session, messages, v2 transcript persistence |

---

## 10. Comparison with OpenClaw

ok-gobot's memory model is designed to match OpenClaw's approach while staying
simpler (single-channel Telegram, no multi-surface routing).

| Feature | OpenClaw | ok-gobot |
|---------|----------|----------|
| Bootstrap files in prompt | SOUL, USER, AGENTS, MEMORY, TOOLS, IDENTITY, HEARTBEAT, daily today | SOUL, IDENTITY, USER, TOOLS, AGENTS, HEARTBEAT, MEMORY, daily today + yesterday |
| Daily notes | Today only | Today + yesterday |
| Session history | Full multi-turn | Full multi-turn (120 messages) |
| Compaction | Implemented | Logic exists, command not wired |
| Semantic search | Via tool call | Via tool call |
| Channels | Multi-channel (Telegram, Discord, etc.) | Telegram only |
| Honest failures | Clear error messages | Clear error messages (IsFallback guard) |

---

## 11. Known Limitations

1. **SearchChunks is O(n)** — loads all chunks from SQLite and computes cosine
   similarity in Go. Fine for hundreds of chunks; will need a LIMIT clause or
   vector index for thousands.

2. **Daily notes grow unbounded** — each response appends to today's note.
   Long conversations produce large daily files that consume prompt tokens.

3. **No MEMORY.md auto-curation** — MEMORY.md must be manually edited (via `file`
   tool or direct edit). There is no automatic summarization or promotion from
   daily notes to long-term memory.

4. **No automatic compaction trigger** — `/compact` works but must be invoked
   manually. There is no automatic trigger when context approaches the limit.
