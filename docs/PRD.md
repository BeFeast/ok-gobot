# ok-gobot Immediate-Response Runtime Plan

## Summary
- This replaces the prior plan. The new primary driver is the updated PRD in [docs/PRD-IMMEDIATE-RESPONSE.md](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/docs/PRD-IMMEDIATE-RESPONSE.md).
- The core problem is not “missing more architecture”; it is that inbound transport, run execution, and user feedback are still too tightly coupled.
- Phase 1 should therefore focus on a **concurrency-first runtime hub** inside the existing Go binary:
  - immediate acknowledgment on every inbound message
  - parallelism across sessions
  - sequential processing within a session
  - shared event stream for Telegram and TUI
  - explicit sub-agent spawning as child sessions
- Keep the product opinionated:
  - Telegram + TUI only
  - one binary
  - SQLite
  - no WhatsApp
  - no browser dashboard
  - no Docker sandbox in this phase

## Product Outcome
- Any inbound message gets visible feedback in under 1 second.
- Telegram gets placeholder + live edit updates.
- TUI gets native token streaming and tool event cards over loopback WS.
- Sessions do not block each other.
- Sub-agents are first-class runs with separate session keys and explicit model/thinking/tool scope.

## Important Public Changes

### Config
Add:
```yaml
control:
  enabled: true
  bind: "127.0.0.1"
  port: 8787
  token: ""

runtime:
  mode: "hub"              # hub | legacy (temporary rollout flag)
  ack_timeout_ms: 1000
  telegram_edit_interval_ms: 1500
  telegram_edit_token_batch: 20
  max_active_sessions: 0   # 0 = unlimited
  session_queue_limit: 100

session:
  main_key: "main"
  dm_scope: "main"         # main | per_user
  history_limit: 200

subagents:
  enabled: true
  default_model: ""
  default_thinking: "low"
```

### CLI
Add:
- `ok-gobot tui`
- `ok-gobot task <description> [--model ...] [--thinking ...] [--tools ...] [--workspace ...]`
- `ok-gobot sessions`
- `ok-gobot abort [--session <key>]`

### Telegram Commands
Add or redefine:
- `/abort`
- `/task <description> [--model ...] [--thinking ...]`
- `/session [main|list|<key>]`
- `/deliver [on|off]` for TUI-linked/shared session behavior if exposed cross-surface

### HTTP REST API
Add simple HTTP endpoints on the same control bind/port (e.g. `127.0.0.1:8787`):
- `GET /api/sessions` — list all sessions with status, queue depth, model override, updated_at
- `GET /api/sessions/:key` — single session detail
- Auth: `control.token` as Bearer header if configured
- Designed for external tools, scripts, and monitoring (no WS client required)

See issue #48.

### Control Protocol
Add a loopback WebSocket control protocol:
- Requests:
  - `status.get`
  - `sessions.list`
  - `session.select`
  - `chat.send`
  - `run.abort`
  - `agent.set`
  - `model.set`
  - `subagent.spawn`
  - `approval.respond`
- Events:
  - `session.accepted`
  - `session.queued`
  - `run.started`
  - `run.delta`
  - `tool.started`
  - `tool.finished`
  - `run.completed`
  - `run.failed`
  - `approval.request`
  - `approval.resolved`

## Runtime Architecture

### 1. Replace direct bot-to-agent execution with a runtime hub
Create `internal/runtime` as the only execution owner.

Core types:
```go
type RunRequest struct {
    SessionKey        string
    ParentSessionKey  string
    Surface           string // telegram | tui | cron | api | subagent
    AgentID           string
    Input             string
    Deliver           bool
    Route             *DeliveryRoute
    ModelOverride     string
    ThinkLevel        string
    ToolAllowlist     []string
    WorkspaceRoot     string
    AckHandle         AckHandle
}

type DeliveryRoute struct {
    Channel          string // telegram
    ChatID           int64
    ThreadID         int64
    ReplyToMessageID int
    UserID           int64
    Username         string
    UpdatedAt        time.Time
}

type RuntimeEvent struct {
    SessionKey string
    RunID      string
    Type       string
    Payload    any
    At         time.Time
}
```

Responsibilities:
- accept inbound requests immediately
- issue ack requests immediately
- enqueue by `session_key`
- run exactly one active execution per session
- allow parallel active executions across different session keys
- emit structured events to transports and TUI
- own cancellation, approvals, and sub-agent creation

### 2. Per-session mailbox model
Implement:
- `map[string]*SessionWorker`
- each worker has:
  - buffered inbound queue
  - one active run
  - event subscribers
  - cancel func for active run
- each incoming request:
  - resolve session key
  - acknowledge immediately
  - if idle: start run
  - if busy: enqueue and emit queued status immediately

This is the central concurrency primitive. No transport may execute model/tool logic directly.

### 3. Immediate acknowledgment contract
Every surface must call:
```go
AckHandle.AcceptQueued(status string) error
AckHandle.AcceptActive(status string) error
AckHandle.Update(status string) error
AckHandle.Close() error
```

Surface-specific behavior:
- Telegram idle:
  - send `⏳`
  - optionally send typing indicator immediately
- Telegram busy:
  - send `⏳ queued — previous run in progress`
- TUI:
  - append immediate local pending entry
  - render `accepted` or `queued` state from WS event
- Cron/API:
  - no human UI required, but event must still be emitted

## Session Model

### Canonical keys
Use:
- `agent:<agentId>:main`
- `agent:<agentId>:telegram:dm:<userId>` only when `dm_scope=per_user`
- `agent:<agentId>:telegram:group:<chatId>`
- `agent:<agentId>:telegram:group:<chatId>:thread:<topicId>`
- `agent:<agentId>:subagent:<runSlug>`

Defaults:
- direct Telegram messages collapse to `main`
- groups and topics are isolated
- sub-agents are always isolated child sessions

### Persistence
Do not keep `chat_id` as the primary identity. Add new tables:
- `sessions_v2`
- `session_messages_v2`
- `session_routes`
- `run_queue_state`
- `subagent_runs`

Suggested shapes:
```sql
sessions_v2(
  session_key TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  parent_session_key TEXT NOT NULL DEFAULT '',
  model_override TEXT NOT NULL DEFAULT '',
  think_level TEXT NOT NULL DEFAULT '',
  usage_mode TEXT NOT NULL DEFAULT 'off',
  verbose INTEGER NOT NULL DEFAULT 0,
  queue_depth INTEGER NOT NULL DEFAULT 0,
  updated_at DATETIME NOT NULL
);

session_routes(
  session_key TEXT PRIMARY KEY,
  channel TEXT NOT NULL,
  chat_id INTEGER NOT NULL,
  thread_id INTEGER NOT NULL DEFAULT 0,
  reply_to_message_id INTEGER NOT NULL DEFAULT 0,
  user_id INTEGER NOT NULL DEFAULT 0,
  username TEXT NOT NULL DEFAULT '',
  updated_at DATETIME NOT NULL
);

session_messages_v2(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_key TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL
);

subagent_runs(
  run_id TEXT PRIMARY KEY,
  parent_session_key TEXT NOT NULL,
  child_session_key TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '',
  thinking TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  created_at DATETIME NOT NULL,
  completed_at DATETIME
);
```

Migration:
- keep old tables intact
- create v2 tables
- resolve old `chat_id` sessions into canonical keys lazily on first use
- store compatibility alias rows if needed during rollout

## Memory Model

### Philosophy

ok-gobot adopts a **retrieval-first, markdown-as-truth** memory architecture. The key design decisions:

**Markdown is the source of truth.** `MEMORY.md` and `memory/*.md` are the canonical memory store. The SQLite vector index is a search index built *on top of* these files, not a parallel store with independent records. Any fact worth keeping lives in a markdown file; the vector store is only a way to find it fast.

**Retrieval-first, not eager injection.** The model does not receive the full `MEMORY.md` in its system prompt. Instead, the bootstrap stays compact: `SOUL.md`, `USER.md`, `AGENTS.md`, `IDENTITY.md`, `TOOLS.md`. Memory is retrieved on demand via `memory_search` / `memory_get` tool calls. This keeps context windows predictable and scales as the memory corpus grows.

**Daily auto-journal is preserved and automatic.** Writing `memory/YYYY-MM-DD.md` on every inbound/outbound turn is a core ok-gobot behavior. It requires no explicit model decision and produces a reliable audit trail. This is not replaced by the new architecture; it remains the primary append mechanism.

**Unified vector store.** The current separation between the markdown-file memory system and the SQLite semantic memory store (which indexes independent text records) is a liability — two parallel systems with no shared index. The new architecture unifies them: the vector backend indexes chunks from `MEMORY.md` and `memory/*.md` directly, and the separate record-based table is removed.

**Pre-compaction memory flush.** When context approaches token limit, the runtime performs a silent agent turn that instructs the model to write any important facts into the current daily note before compaction occurs. This prevents loss of session-derived insights at context boundaries.

### Memory Tool Surface

```go
// memory_search: semantic search over the unified markdown index
// Returns: []MemorySnippet{File, LineRange, Text, Score}

// memory_get: read specific lines from a memory file
// Returns: raw text of the requested range
```

The model is instructed in the system prompt to call `memory_search` before answering questions about past decisions, preferences, people, dates, or tasks — then optionally call `memory_get` to read surrounding context.

### Unified Indexer Contract

The `internal/memory` package owns:
- a filesystem watcher on `MEMORY.md` and `memory/*.md`
- a markdown chunker (header-aware, ~512-token chunks with overlap)
- embedding generation via the same provider used elsewhere in ok-gobot
- cosine similarity search against a single SQLite vector table
- incremental re-index on file change (only changed chunks are re-embedded)

Index is rebuilt from scratch on first startup if not present; otherwise kept warm via watcher events.

### Pre-Compaction Flush Contract

```go
type MemoryFlushTrigger struct {
    ContextUsedFraction float64 // e.g. 0.80
    SessionKey          string
    DailyNoteFile       string  // e.g. memory/2026-03-01.md
}
```

When `ContextUsedFraction >= threshold` (configurable, default `0.80`), the runtime injects a silent system-side turn before the next user turn. The silent turn instructs the model to summarize and write key facts to the daily note. The flush is performed at most once per session per day to avoid loops.

## Telegram Refactor

### Inbound path
Refactor `internal/bot` so Telegram only:
- authenticates sender
- resolves group/standby/mention behavior
- normalizes media/text into an inbound envelope
- resolves delivery route
- submits to runtime hub
- never calls `ToolCallingAgent.ProcessRequest` directly

### Outbound path
Telegram adapter subscribes to runtime events and renders them:
- `session.accepted` -> send placeholder if not yet sent
- `session.queued` -> send queued placeholder
- `run.delta` -> edit the placeholder/final message
- `tool.started` / `tool.finished` -> append compact status lines into active edited message
- `run.completed` -> finalize edited message
- `run.failed` -> finalize with error text
- `approval.request` -> attach inline keyboard
- `approval.resolved` -> continue run or mark denied

### Telegram streaming policy
Implement:
- edit every `20` tokens or `1500ms`, whichever comes first
- maintain one editable placeholder per active run
- compact tool status display to last `N` items to avoid Telegram message explosion
- preserve reply tags/reactions only at finalization stage

## TUI

### Scope
TUI is phase-1 core, not follow-up.

### Implementation
Add `internal/tui` using Bubble Tea.

Features required:
- live chat log
- native token streaming
- tool event cards inline
- session picker
- agent picker
- model override
- `/abort`
- sub-agent spawn dialog
- approval prompt dialog
- current queue depth / run state indicator

### Transport
TUI connects only to the local control server over WS.
It does not read SQLite directly.
It does not call bot internals directly.

### Default behavior
- session default: `agent:<currentAgent>:main`
- delivery default: off
- when delivery is on, final response is also delivered to the last Telegram route for that session

## Sub-agent Spawning

### Product behavior
Sub-agent spawning is included in phase 1 because the PRD makes it explicit.

### Implementation
Treat a sub-agent as another `RunRequest` with:
- new child session key
- explicit parent session key
- explicit model/thinking/tool scope/workspace
- result routed back to parent session on completion

Types:
```go
type SubagentSpawnRequest struct {
    ParentSessionKey string
    Task             string
    Model            string
    Thinking         string
    ToolAllowlist    []string
    WorkspaceRoot    string
    DeliverBack      bool
}
```

### Surfaces
- TUI:
  - spawn dialog with fields for task, model, thinking, tools, workspace
- Telegram:
  - `/task <description> --model sonnet --thinking high`
  - start simple flag parsing; do not add arbitrary shell-like syntax beyond these explicit flags

### Visibility
- child run appears as its own session in TUI
- parent session receives completion summary or failure notification
- Telegram parent session gets only completion notification, not full streaming child updates unless explicitly requested later

## Safety and Approvals

### Phase 1 scope
Include:
- working approval manager wiring
- transport-independent approval broker in runtime
- allowlist-aware default deny when no operator surface is attached

Do not include:
- Docker sandbox
- external node execution
- browser-based approval UI

### Approval flow
- tool requests approval
- runtime emits `approval.request`
- Telegram or TUI responds
- run pauses until answer or timeout
- timeout defaults to deny

## Package Boundaries
- `internal/runtime`: run manager, workers, event bus, approvals, sub-agents
- `internal/session`: key builder, route resolver, session persistence
- `internal/control`: WS control server and protocol
- `internal/tui`: terminal client
- `internal/bot`: Telegram transport adapter
- `internal/tools`: tool implementations, now built per runtime request context
- `internal/app`: bootstraps runtime, bot, control server, API, cron
- `internal/memory`: unified markdown memory system — filesystem watcher, markdown chunker, embedding pipeline, SQLite vector store, `memory_search` / `memory_get` handlers, pre-compaction flush trigger; replaces the prior split between `internal/memory/manager.go` (semantic record store) and `internal/memory/store.go` (SQLite embedding backend)

## Delivery Order

### Phase A — Fix current broken wiring
- approval manager instantiated and connected
- config watcher started from app
- per-session model override honored on normal tool path
- AI failover wrapped into production client path
- cron/message/semantic memory tools wired through real dependencies
- file/patch/grep tools resolve against configured workspace, not cwd
- wire `memory_search` / `memory_get` tools through real vector store (currently stubs — connect to SQLite vector backend with actual embeddings)

### Phase B — Runtime hub and v2 sessions
- add session key model
- add runtime hub with per-session workers
- add event model
- add v2 SQLite tables
- route Telegram through runtime hub without TUI yet
- preserve placeholder + queued acknowledgment behavior
- build markdown memory indexer: filesystem watcher over `MEMORY.md` and `memory/*.md`, header-aware chunking, embeddings stored in SQLite vector table
- unify semantic memory store with markdown index: remove the independent record-based semantic store table, redirect all memory tool calls through the markdown-backed index

### Phase C — Telegram immediate response
- immediate placeholder on inbound
- queued acknowledgment in busy sessions
- live-edited streaming message
- tool status lines in placeholder
- `/abort` through runtime hub
- implement pre-compaction memory flush: detect context token usage ≥ 80% threshold → inject silent system turn → model writes key facts to `memory/YYYY-MM-DD.md` → proceed with compaction

### Phase D — Control server and TUI
- add local WS server
- add TUI client
- stream tokens/tool events
- session picker, model picker, agent picker, approvals

### Phase E — Sub-agents
- spawn child session API
- TUI spawn dialog
- Telegram `/task` command with explicit flags
- completion routing to parent session

### Phase F — Cleanup
- default `runtime.mode=hub`
- remove legacy direct execution path once parity tests pass

## Test Cases and Scenarios
1. Telegram DM while idle gets `⏳` in under 1 second.
2. Telegram DM while same session is busy gets queued acknowledgment in under 1 second.
3. Telegram session A running does not delay Telegram session B acknowledgment.
4. TUI send while Telegram run is active in another session starts immediately and streams tokens.
5. TUI and Telegram share `agent:<agentId>:main` session state by default.
6. Group chats remain isolated from `main`.
7. Topic/thread messages remain isolated from parent group session.
8. Runtime worker guarantees one active run per session key.
9. `/abort` cancels active run in under 2 seconds.
10. Tool events appear in Telegram edited placeholder during long tool chains.
11. Tool events appear as inline cards in TUI in real time.
12. Approval request pauses the run and resumes correctly after Telegram approval.
13. Approval request pauses the run and resumes correctly after TUI approval.
14. Session model override changed in TUI affects next Telegram turn in same session.
15. Telegram `/task --model ... --thinking ...` creates isolated child session and returns completion to parent.
16. TUI sub-agent spawn creates visible child session and completion event.
17. Old SQLite data remains readable after v2 migration.
18. Config reload updates edit cadence and ack settings without restart where supported.
19. `memory_search "past decisions about X"` returns ranked snippets sourced from `MEMORY.md` and `memory/*.md`, not from independent semantic records.
20. Every completed agent turn appends a `User:` / `Assistant:` entry to `memory/YYYY-MM-DD.md` automatically, with no explicit model action required.
21. System prompt does not contain the full `MEMORY.md` text; only `SOUL.md`, `USER.md`, `AGENTS.md`, `IDENTITY.md`, `TOOLS.md` are present in bootstrap context.
22. When context token usage reaches 80%, a silent flush turn executes and the resulting `memory/YYYY-MM-DD.md` contains a summary of session-derived facts before compaction proceeds.
23. After modifying `MEMORY.md` on disk, the memory indexer re-embeds only the changed chunks within 5 seconds; subsequent `memory_search` queries reflect the update.

## Assumptions and Defaults
- The updated PRD is authoritative over the earlier phase split.
- Immediate response is the top priority.
- TUI is a required phase-1 surface.
- Sub-agent spawning is included in phase 1.
- Telegram and TUI are the only interactive surfaces in scope.
- Browser UI, WhatsApp, and sandboxing remain out of scope.
- `dm_scope` stays `main` by default because `ok-gobot` is still single-user opinionated.
- SQLite remains the only persistence layer.
- The system remains one Go binary and one local long-running runtime.
- The SQLite vector extension already present in ok-gobot is the sole vector backend; no external vector database is introduced.
- `MEMORY.md` is never injected into the system prompt in full; the model accesses long-term memory exclusively via `memory_search` / `memory_get` tool calls.
- The independent semantic memory record table (`memory save` / `memory list` / `memory forget` workflow) is deprecated in Phase B and removed once the unified markdown index reaches parity.
