# ok-gobot: Missing Features vs Moltbot

Comparison date: 2026-01-29

> ok-gobot scope: Telegram-only, OpenRouter-only (OpenAI-compatible API).
> Features from moltbot that are multi-channel or provider-specific are excluded.

---

## Priority 1: Critical (Core Functionality)

### 1.1 Native OpenAI Tool Calling API

**Current state:** ok-gobot parses JSON tool calls from plain text AI responses (`parseToolCall` in `tool_agent.go`). This is unreliable — models may output malformed JSON, miss calls, or hallucinate tool names.

**Target:** Use OpenAI-compatible `tools` parameter in API requests and parse `tool_calls` from the response. OpenRouter supports this natively.

**Files to change:**
- `internal/ai/client.go` — add `tools` field to request, parse `tool_calls` from response
- `internal/agent/tool_agent.go` — remove text-based JSON parsing, use structured tool calls
- `internal/tools/tools.go` — add `ToOpenAITool()` method generating JSON Schema

**Sub-tasks:**
1. Define OpenAI tool/function schema structs in `internal/ai/types.go`
2. Add `ToOpenAITool()` to Tool interface returning JSON Schema
3. Implement schema generation for each existing tool (local, ssh, file, obsidian, message, search, web_fetch, browser, cron, image_gen, tts)
4. Add `tools` field to `ChatCompletionRequest` in `client.go`
5. Parse `tool_calls` from `ChatCompletionResponse` and `ChatCompletionChunk` (streaming)
6. Refactor `ToolCallingAgent.Process()` to use native tool calls instead of text parsing
7. Support multi-tool calls (parallel tool execution)
8. Keep text-based fallback for models that don't support tool calling
9. Write tests for schema generation and tool call parsing

---

### 1.2 Model Failover & Fallback

**Current state:** Single model configured. If it fails, the request fails. Only fallback is streaming→non-streaming.

**Target:** Chain of fallback models. If primary model returns error (rate limit, downtime, context too long), automatically retry with next model.

**Files to change:**
- `internal/config/config.go` — add `ai.fallback_models` list
- `internal/ai/client.go` — add retry logic with fallback chain

**Sub-tasks:**
1. Add `fallback_models` config field (list of model IDs)
2. Define retryable error types (429, 503, 500, context_length_exceeded)
3. Implement `CompleteWithFallback()` that tries primary, then fallbacks
4. Add cooldown tracking per model (don't retry recently-failed model)
5. Log which model was actually used in response
6. Apply fallback to both streaming and non-streaming paths
7. Write tests for failover logic

---

### 1.3 Session Model Overrides

**Current state:** One global model for all chats.

**Target:** Per-chat model selection via command like `/model claude-3.5-sonnet`.

**Files to change:**
- `internal/bot/bot.go` — add `/model` command handler
- `internal/storage/sqlite.go` — add `model_override` column to sessions
- `internal/agent/tool_agent.go` — check session override before using default

**Sub-tasks:**
1. Add `model_override` column to sessions table + migration
2. Implement `/model` command (set, clear, show current)
3. Implement `/model list` showing available models
4. Pass session model override to AI client
5. Show active model in `/status` output
6. Write tests

---

### 1.4 Multi-Agent System (Basic)

**Current state:** Single agent personality loaded from `~/clawd/`.

**Target:** Support multiple agent profiles. Each can have different personality, tools, model. Selectable per chat or via command.

**Files to change:**
- `internal/agent/` — new `agent_registry.go`
- `internal/config/config.go` — add agents config section
- `internal/bot/bot.go` — add `/agent` command

**Sub-tasks:**
1. Define Agent config struct (name, soul_path, model, allowed_tools)
2. Add `agents` section to config.yaml
3. Create AgentRegistry that loads multiple personality sets
4. Implement `/agent` command to switch agent per session
5. Store active agent in session metadata
6. Route messages to correct agent based on session
7. Write tests

---

### 1.5 Semantic Memory with Embeddings

**Current state:** File-based memory (MEMORY.md, daily notes). No search capability beyond exact file reads.

**Target:** Vector store for conversation memories. Embed important messages, search by semantic similarity.

**Files to change:**
- New `internal/memory/` package
- `internal/tools/` — new memory_tool.go

**Sub-tasks:**
1. Define Memory interface (Store, Search, Delete)
2. Implement SQLite-vec backend for vector storage
3. Add OpenAI/OpenRouter embeddings client (text-embedding-3-small)
4. Implement automatic memory extraction from conversations
5. Create `memory` tool for agent to save/search memories
6. Integrate memory context into system prompt (top-K relevant)
7. Add `/memory search <query>` command
8. Migrate existing MEMORY.md content into vector store
9. Write tests

---

### 1.6 Exec Approval Workflow

**Current state:** `local` tool executes any command without confirmation. `ShouldAskBeforeAction` exists but isn't enforced at tool level.

**Target:** Dangerous commands require user approval via Telegram inline keyboard before execution.

**Files to change:**
- `internal/tools/tools.go` — add approval mechanism to LocalCommand
- `internal/bot/bot.go` — add inline keyboard callback handler

**Sub-tasks:**
1. Define dangerous command patterns (rm, kill, shutdown, reboot, dd, mkfs, etc.)
2. Implement approval request via Telegram inline keyboard (Approve/Deny)
3. Add callback query handler in bot for approval responses
4. Block tool execution until approval received (with timeout)
5. Add `tools.local.require_approval` config option
6. Log all approved/denied commands
7. Write tests

---

## Priority 2: Important (Quality of Life)

### 2.1 Typing Indicators

**Current state:** No typing action sent while processing.

**Target:** Send "typing..." action while AI is thinking/streaming.

**Sub-tasks:**
1. Send `ChatTyping` action when message received
2. Refresh typing every 5 seconds during long processing
3. Stop typing indicator when response sent

---

### 2.2 Message Debouncing & Rate Limiting

**Current state:** Every message triggers AI immediately. Rapid messages = rapid API calls.

**Target:** Debounce rapid messages (wait 1-2s for more input). Rate limit per user.

**Sub-tasks:**
1. Implement per-chat debounce timer (configurable, default 1.5s)
2. Batch debounced messages into single AI request
3. Add per-user rate limit (max N requests per minute)
4. Return friendly message when rate limited
5. Write tests

---

### 2.3 Enhanced Web Content Extraction

**Current state:** Basic HTML tag stripping in web_fetch.

**Target:** Mozilla Readability-style article extraction for clean content.

**Sub-tasks:**
1. Evaluate Go readability libraries (go-readability)
2. Integrate readability extraction as primary method
3. Fall back to current HTML stripping if readability fails
4. Extract metadata (title, author, date, description)
5. Write tests with real-world HTML samples

---

### 2.4 Multiple TTS Providers

**Current state:** OpenAI TTS only.

**Target:** Add Edge TTS (free, no API key) as alternative.

**Sub-tasks:**
1. Define TTS provider interface
2. Implement Edge TTS provider (via edge-tts CLI or Go library)
3. Add `tts.provider` config option
4. Allow per-request provider selection in tool
5. Write tests

---

### 2.5 Config Hot-Reload

**Current state:** Config loaded once at startup. Changes require restart.

**Target:** Watch config file for changes, reload automatically.

**Sub-tasks:**
1. Add fsnotify watcher on config.yaml
2. Implement safe config reload (validate before applying)
3. Notify components of config change via channel
4. Add `/reload` command for manual reload
5. Write tests

---

### 2.6 Log Redaction

**Current state:** API keys and tokens may appear in logs.

**Target:** Automatically mask sensitive data in log output.

**Sub-tasks:**
1. Define patterns to redact (API keys, tokens, passwords)
2. Implement redaction middleware for logger
3. Apply to all log output paths
4. Write tests with sample sensitive data

---

### 2.7 DM Authorization System

**Current state:** Bot responds to anyone who messages it.

**Target:** Require authorization for new users. Allowlist or pairing code.

**Sub-tasks:**
1. Add `auth.mode` config (open, allowlist, pairing)
2. Implement allowlist with user IDs / usernames
3. Implement pairing code generation and validation
4. Store authorized users in SQLite
5. Add `/auth` admin command to manage users
6. Return "unauthorized" message for unknown users
7. Write tests

---

### 2.8 Webhook / HTTP API

**Current state:** No external API. Only Telegram polling.

**Target:** Simple HTTP API for sending messages, querying status, triggering actions externally.

**Sub-tasks:**
1. Add HTTP server with configurable port
2. Implement `/api/send` endpoint (send message to chat)
3. Implement `/api/status` endpoint
4. Implement `/api/health` endpoint
5. Add API key authentication
6. Add webhook endpoint for cron/external triggers
7. Write tests

---

### 2.9 Apply Patch / Advanced File Editing

**Current state:** File tool only supports full read/write.

**Target:** Support unified diff patches for precise file editing.

**Sub-tasks:**
1. Implement unified diff parser
2. Implement patch application logic
3. Add `patch` operation to file tool
4. Add `search` operation (grep-like) to file tool
5. Write tests

---

### 2.10 Group Chat Activation Modes

**Current state:** Basic `ShouldRespondInGroup` check.

**Target:** Active mode (respond to all) vs Standby mode (respond only to mentions/replies).

**Sub-tasks:**
1. Add `groups.activation_mode` config (active, standby)
2. Detect bot mentions in group messages
3. Detect replies to bot messages
4. Implement standby mode logic
5. Add `/activate` and `/standby` commands for runtime switching
6. Store per-group mode in session
7. Write tests

---

## Priority 3: Nice to Have

### 3.1 SSRF Protection in web_fetch

**Sub-tasks:**
1. Block requests to localhost, 127.0.0.1, ::1, 10.x, 172.16-31.x, 192.168.x
2. Block requests to link-local addresses
3. Resolve DNS before connecting to prevent DNS rebinding
4. Write tests

### 3.2 Diagnostic / Doctor Command

**Sub-tasks:**
1. Check config validity
2. Test AI API connectivity
3. Test Telegram bot token
4. Check optional dependencies (pdftotext, whisper, ffmpeg, chrome)
5. Report results

### 3.3 Daemon Management

**Sub-tasks:**
1. Generate systemd unit file
2. Generate launchd plist
3. Implement `ok-gobot daemon install/start/stop/status`

### 3.4 Browser Profiles

**Sub-tasks:**
1. Support multiple named Chrome profiles
2. Add profile selection to browser tool
3. Store profiles in config

### 3.5 Message Sanitization

**Sub-tasks:**
1. Sanitize user input before passing to tools
2. Prevent command injection in local/ssh tools
3. Escape special characters in Telegram markdown

---

## Implementation Order (Recommended)

| Phase | Tasks | Rationale |
|-------|-------|-----------|
| **Phase 1** | 1.1 (Native tool calling) | Foundation — everything else depends on reliable tool calls |
| **Phase 2** | 2.1 (Typing), 1.2 (Failover) | Quick UX win + reliability |
| **Phase 3** | 1.3 (Model overrides), 1.6 (Exec approval) | User control + safety |
| **Phase 4** | 2.2 (Rate limiting), 2.6 (Log redaction), 3.1 (SSRF) | Hardening |
| **Phase 5** | 1.5 (Semantic memory), 2.3 (Web extraction) | Intelligence |
| **Phase 6** | 1.4 (Multi-agent), 2.7 (Auth), 2.10 (Group modes) | Multi-user readiness |
| **Phase 7** | 2.8 (HTTP API), 2.5 (Hot-reload), 2.9 (Patches) | Extensibility |
| **Phase 8** | 2.4 (TTS), 3.2-3.5 (Polish) | Nice to have |
