# ok-gobot Features

## AI & LLM

### Native OpenAI Tool Calling
Uses the structured `tools` API parameter instead of parsing JSON from text responses. Supports parallel tool calls and iterative multi-step workflows (up to 10 rounds). Falls back to text-based parsing for models without tool support.

**Files:** `internal/ai/types.go`, `internal/ai/client.go`, `internal/agent/tool_agent.go`

### Model Failover
Automatic fallback chain when the primary model fails. Retryable errors: 429, 500-504, context_length_exceeded. Models go on 60-second cooldown after failure. Thread-safe.

**Config:** `ai.fallback_models: ["anthropic/claude-3.5-sonnet", "openai/gpt-4o-mini"]`
**Files:** `internal/ai/failover.go`

### Per-Session Model Override
Each chat can use a different model. The `/model` command sets, clears, or lists models. Stored in SQLite.

**Commands:** `/model`, `/model list`, `/model <name>`, `/model clear`
**Files:** `internal/bot/bot.go`, `internal/storage/sqlite.go`

### Multi-Agent System
Multiple agent profiles with separate personality files, models, and tool restrictions. Switchable per chat via `/agent` command. If no agents configured, single default agent is used.

**Config:**
```yaml
agents:
  - name: "default"
    soul_path: "~/clawd"
  - name: "coder"
    soul_path: "~/clawd-coder"
    model: "anthropic/claude-3.5-sonnet"
    allowed_tools: ["local", "file", "grep", "patch"]
```
**Files:** `internal/agent/registry.go`, `internal/bot/agent_command.go`, `internal/bot/agent_handler.go`

### Streaming Responses
SSE streaming with rate-limited Telegram message editing (1 edit/second). Automatic fallback to non-streaming on failure.

**Files:** `internal/ai/client.go`, `internal/bot/stream_editor.go`

### Context Compaction
AI-powered summarization when conversation approaches 80% of model context limit. Model-aware token limits (GPT-4o 128K, Claude 200K, Gemini 1M, Kimi 131K).

**Files:** `internal/agent/compactor.go`, `internal/agent/tokens.go`

---

## Tools

### Shell & Files
- **local** — Execute shell commands. Dangerous commands (rm -rf, kill, shutdown, etc.) require inline keyboard approval.
- **ssh** — Remote execution. Hosts configured in `~/clawd/TOOLS.md`.
- **file** — Read/write with path traversal protection.
- **patch** — Apply unified diffs to files.
- **grep** — Recursive regex search, skips binary files and `.git`/`node_modules`. Max 50 results.
- **obsidian** — Obsidian vault CRUD with frontmatter timestamps.

### Web
- **search** — Brave Search or Exa API. Returns 5 results with title/URL/snippet.
- **web_fetch** — Fetch URLs with Mozilla Readability extraction (go-shiori/go-readability). Falls back to basic HTML stripping. SSRF protection blocks private IPs. 12KB content limit.
- **browser** — Chrome automation via ChromeDP: navigate, click, fill, screenshot, wait, extract text.

### Media
- **image_gen** — DALL-E 3. Sizes: 1024x1024, 1792x1024, 1024x1792. Quality: standard/hd.
- **tts** — Two providers: OpenAI (paid, 6 voices) and Edge TTS (free, Russian/English voices). Provider prefix: `edge:text` or `openai:text`. Auto OGG conversion for Telegram.

### Memory & Scheduling
- **memory** — Semantic vector memory. Embeds text via OpenAI embeddings API, stores in SQLite as binary BLOBs, searches with cosine similarity in Go. Commands: save, search, list, forget.
- **cron** — 5-field cron expressions. Persistent in SQLite. Enable/disable without deletion.
- **message** — Send to other chats by ID or alias. Allowlist-based security.

---

## Security & Control

### Exec Approval
Dangerous commands (`rm -rf`, `kill`, `shutdown`, `DROP TABLE`, etc.) trigger a Telegram inline keyboard with Approve/Deny buttons. Auto-deny after 60 seconds.

**Files:** `internal/bot/approval.go`, `internal/bot/bot_approval.go`

### DM Authorization
Three modes:
- **open** — anyone can use the bot (default)
- **allowlist** — only configured user IDs + DB-authorized users
- **pairing** — requires pairing code from admin (`/auth pair` generates 6-digit code, `/pair <code>` to activate)

**Files:** `internal/bot/auth.go`

### Group Activation Modes
- **active** — bot responds to all messages
- **standby** — bot responds only to @mentions, replies to its messages, or messages starting with its name

Per-group, stored in DB. Commands: `/activate`, `/standby`.

**Files:** `internal/bot/groups.go`

### Rate Limiting & Debouncing
- Per-chat rate limiter: 10 requests/minute sliding window
- Message debouncer: 1.5s window batches rapid messages into single AI request

**Files:** `internal/bot/ratelimit.go`, `internal/bot/debounce.go`

### SSRF Protection
web_fetch validates URLs before requests: blocks localhost, private IPs (10.x, 172.16-31.x, 192.168.x, fc00::/7), resolves DNS first to prevent rebinding.

**Files:** `internal/tools/web_fetch.go`

### Log Redaction
Masks sensitive patterns in log output: API keys (sk-...), Bearer tokens, bot tokens, long hex/base64 strings.

**Files:** `internal/redact/redact.go`

### Message Sanitization
- `SanitizeShellArg` — escapes shell metacharacters
- `SanitizeTelegramMarkdown` — escapes MarkdownV2 special chars
- `StripControlChars` — removes non-printable characters

**Files:** `internal/sanitize/sanitize.go`

---

## Infrastructure

### HTTP API
REST API with API key authentication (`X-API-Key` header or Bearer token). Endpoints:
- `GET /api/health` — no auth
- `GET /api/status` — bot status
- `POST /api/send` — send message to chat
- `POST /api/webhook` — forward event to configured chat

See [API.md](../API.md) for full reference.

### Config Hot-Reload
Watches `config.yaml` with fsnotify. 500ms debounce, validates before applying. Manual reload via `/reload` (admin only).

**Files:** `internal/config/watcher.go`

### Daemon Management
Install as system service: launchd plist on macOS, systemd user unit on Linux. Auto-restart on failure.

```bash
ok-gobot daemon install
ok-gobot daemon start|stop|status|logs|uninstall
```

### Doctor Diagnostics
Validates config, Telegram token, AI API key, API reachability, storage path. Checks optional deps: pdftotext, whisper, ffmpeg, Chrome.

```bash
ok-gobot doctor
```

---

## Media Handling

- **Photos** — download from Telegram, send local images
- **Voice messages** — OGG format, optional Whisper transcription
- **Audio files** — MP3/OGG with optional transcription
- **Documents** — PDF (pdftotext), TXT, MD, JSON, YAML, XML, CSV

**Files:** `internal/bot/media.go`

---

## Personality & Memory

### File-Based Personality
Loads from configurable directory: IDENTITY.md, SOUL.md, USER.md, AGENTS.md, TOOLS.md, MEMORY.md.

### File-Based Memory
Daily notes in `memory/YYYY-MM-DD.md`. Long-term memory in MEMORY.md (loaded only in private sessions).

### Semantic Memory
Vector embeddings stored in SQLite. Cosine similarity search in Go. OpenAI-compatible embeddings API.

### Heartbeat System
Periodic background checks: context usage warnings, email monitoring (IMAP). Custom checker registration.

**Files:** `internal/agent/heartbeat.go`
