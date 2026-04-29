# ok-gobot Features

## AI & LLM

### Native OpenAI Tool Calling
Uses the structured `tools` API parameter instead of parsing JSON from text responses. Supports parallel tool calls and iterative multi-step workflows (up to 10 rounds). Falls back to text-based parsing for models without tool support.

**Files:** `internal/ai/types.go`, `internal/ai/client.go`, `internal/agent/tool_agent.go`

### Model Failover
Automatic fallback chain when the primary model fails. Retryable errors: 429, 500-504, context_length_exceeded, network errors (EOF, connection reset, TLS failures). Models go on 60-second cooldown after failure. Thread-safe.

**Config:** `ai.fallback_models: ["anthropic/claude-3.5-sonnet", "openai/gpt-4o-mini"]`
**Files:** `internal/ai/failover.go`

### Multi-Model Routing
Route messages to different models based on task type. Five task types: `vision`, `summarize`, `reasoning`, `coding`, `default`. Detection via explicit `[task:type]` tags in the message body. Resolution order: exact task match in routes config, then "default" route entry, then global default model.

**Config:**
```yaml
model_routes:
  vision: "google/gemini-2.5-pro"
  coding: "claude-sonnet-4-5-20250929"
  summarize: "claude-haiku-3-5-20241022"
```
**Files:** `internal/ai/router.go`

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
    soul_path: "~/ok-gobot-soul"
  - name: "coder"
    soul_path: "~/ok-gobot-soul-coder"
    model: "anthropic/claude-3.5-sonnet"
    allowed_tools: ["local", "file", "grep", "patch"]
    capabilities:
      shell: true
      network: false
      file_write_scope: "full"
```
**Files:** `internal/agent/registry.go`, `internal/bot/agent_command.go`, `internal/bot/agent_handler.go`

### Streaming Responses
SSE streaming with rate-limited Telegram message editing (1 edit/second). Automatic fallback to non-streaming on failure.

**Files:** `internal/ai/client.go`, `internal/bot/stream_editor.go`

### Context Compaction
AI-powered summarization when conversation approaches 80% of model context limit. Model-aware token limits (GPT-4o 128K, Claude 200K, Gemini 1M, Kimi 131K).

**Files:** `internal/agent/compactor.go`, `internal/agent/tokens.go`

### Reflection Loop
Automatic failure tracking per tool. When a tool's failure count exceeds the threshold (default 3), the reflector proposes fixes based on error patterns:
- "not found" -- verify registration/dependencies
- "timeout" -- increase timeout or reduce scope
- "permission denied" -- check runtime permissions
- "parse/unmarshal/invalid" -- review input schema
- "connect/dial/network" -- check connectivity

Failure records are persisted to daily memory notes. Non-blocking background processing.

**Files:** `internal/agent/reflection.go`

---

## Roles & Jobs

### Prebuilt Roles
Three bundled role manifests ship as starting points for scheduled autonomous workflows:

| Role | Default Schedule | Description |
|------|-----------------|-------------|
| `researcher` | 09:00 UTC daily | Searches the web and compiles a daily research digest |
| `monitor` | Every 30 minutes | Checks service/URL health and reports status |
| `release-watch` | 10:00 UTC every Monday | Tracks software releases for configured projects |

Roles are enabled by setting `roles_path` in config and placing markdown manifests in that directory. No roles run by default.

**Files:** `internal/role/bundled.go`, `internal/role/manifest.go`, `internal/role/prebuilt/`

### Declarative Role Manifests
Operators define custom roles as markdown files with YAML frontmatter:

```markdown
---
worker: standard
tools: [web_fetch, search]
schedule: "0 0 9 * * *"
approval: auto
report_template: "{{.Result}}"
---
# My Researcher

You are a research agent. Search for news about Go releases...
```

| Field | Type | Description |
|-------|------|-------------|
| `worker` | string | Cost tier: `premium`, `standard`, `cheap`, `local` |
| `tools` | list | Allowed tools. Empty = all tools permitted |
| `schedule` | string | 6-field cron expression (seconds included). Empty = no schedule |
| `approval` | string | `auto` (default), `always`, or `never` |
| `report_template` | string | Go `text/template` for report formatting |

**Files:** `internal/role/manifest.go`

### Rules-First Chat Routing
Incoming chat turns are classified before execution:
- **reply** -- lightweight turns stay on the inline AI reply path
- **clarification** -- underspecified work requests get a short follow-up question first
- **background job** -- obvious heavy work is launched as an explicit isolated task instead of blocking the main chat session

Forced job prefixes: `job:`, `task:`, `background:` at the start of a message.

**Files:** `internal/runtime/chat_router.go`, `internal/bot/chat_routing.go`

### Sub-Agent Spawning
Background jobs run as isolated sub-agents with their own session keys (`agent:<agentId>:subagent:<runSlug>`). Parent-child session tracking delivers results back to the originating chat. Cost tier support selects the model class per job.

**Files:** `internal/runtime/subagent.go`, `internal/runtime/runtime.go`

### Mission Control API
HTTP endpoints for monitoring the runtime:
- `GET /api/mission/roles` -- list configured roles with metadata
- `GET /api/mission/schedules` -- list cron jobs with next-run times
- `GET /api/mission/runs` -- recent job run history
- `GET /api/mission/stats` -- aggregate statistics

Loopback-only access with CORS.

**Files:** `internal/control/mission.go`

**Important:** Scheduled roles are functional but do not yet enforce hard token/cost budgets. Budget enforcement is planned for Mission Control v1 (see [ROADMAP.md](ROADMAP.md)).

---

## Skills

### Markdown-First Skills
Skills are markdown-based extensions stored in the workspace `skills/` directory. Each skill has a `SKILL.md` file describing its capabilities.

### CLI Management
```bash
ok-gobot skills list       # Show installed skills with utility scores
ok-gobot skills install <path-or-git-url>  # Install with safety audit
ok-gobot skills remove <name>              # Remove skill
ok-gobot skills audit <path>               # Run safety audit only
ok-gobot skills history                    # Version history
ok-gobot skills rollback                   # Revert to previous version
```

### Safety Audit
Static analysis rejects unsafe skill packages:
- Symlinks pointing outside the skill root
- Script files (`.sh`, `.py`, `.js`, `.exe`, etc.)
- Executable file permission bits
- Pipe-to-shell patterns (`curl | bash`)
- Markdown links escaping the skill root (`../` prefixes)

Errors block installation; warnings are reported but allowed.

### Utility Score Tracking
Each skill tracks uses, successes, and failures in SQLite. The system prompt includes skill descriptions sorted by utility score descending, prioritizing proven-useful skills.

**Files:** `internal/bootstrap/skills.go`, `internal/bootstrap/loader.go`, `internal/cli/skills.go`, `internal/agent/personality.go`

---

## Self-Evolution

### A-Evolve Cycle
ok-gobot implements a self-evolution loop inspired by the A-Evolve paper:

1. **Solve** -- execute tasks and collect metrics (success rate, tokens, duration, retries, tool calls)
2. **Observe** -- analyze failure patterns (high failure rate, excessive tokens, slow completions, tool overuse)
3. **Evolve** -- generate candidate mutations with human-readable notes
4. **Gate** -- benchmark suite scoring; new version must score strictly higher than current
5. **Reload** -- promote the new version and reload configuration

Safety guards:
- Human approval required for large prompt diffs (>20% change)
- Daily cycle limit (max 1 per 24 hours)
- Production failure tracking with automatic rollback after 3 failures
- Versioned snapshots stored in `evolution_dir/v<N>/` with manifest and diff

**CLI:**
```bash
ok-gobot evolution status     # Show config and latest version
ok-gobot evolution history    # List all versions with scores
ok-gobot evolution rollback <version>  # Roll back to a specific version
ok-gobot evolution metrics    # Show task metrics
```

**Files:** `internal/evolution/engine.go`, `internal/cli/evolution.go`

---

## Tools

### Shell & Files
- **local** -- Execute shell commands. Dangerous commands (rm -rf, kill, shutdown, sudo, curl|sh, etc.) require inline keyboard approval.
- **ssh** -- Remote execution. Hosts configured in `~/ok-gobot-soul/TOOLS.md`. Uses `StrictHostKeyChecking=accept-new` (trust-on-first-use).
- **file** -- Read/write with path traversal and symlink escape protection.
- **patch** -- Apply unified diffs to files.
- **grep** -- Recursive regex search, skips binary files and `.git`/`node_modules`. Max 50 results.
- **obsidian** -- Obsidian vault CRUD with frontmatter timestamps. Symlink-safe path resolution.

### Web
- **search** -- Brave Search or Exa API. Returns 5 results with title/URL/snippet.
- **web_fetch** -- Fetch URLs with Mozilla Readability extraction (go-shiori/go-readability). Falls back to basic HTML stripping. SSRF protection blocks private IPs and validates redirect targets. 12KB content limit.
- **browser** -- Chrome automation via ChromeDP: navigate, click, fill, screenshot, wait, extract text. Blocks `file://`, localhost, and `.internal`/`.local` URLs.

### Media
- **image_gen** -- DALL-E 3. Sizes: 1024x1024, 1792x1024, 1024x1792. Quality: standard/hd.
- **tts** -- Two providers: OpenAI (paid, 6 voices) and Edge TTS (free, Russian/English voices). Provider prefix: `edge:text` or `openai:text`. Auto OGG conversion for Telegram.

### Memory & Scheduling
- **memory_search** -- Semantic vector search. Embeds text via OpenAI embeddings API, stores in SQLite as binary BLOBs, searches with cosine similarity in Go.
- **memory_get** -- Read markdown memory content by source and optional header path.
- **cron** -- 5-field cron expressions. Persistent in SQLite. Enable/disable without deletion.
- **message** -- Send to other chats by ID or alias. Allowlist-based security.

---

## Security & Control

### Capability Policy
Per-agent capability restrictions enforced at tool dispatch:

| Capability | Controls | Default |
|-----------|----------|---------|
| `shell` | local, ssh execution | allowed |
| `network` | web_fetch, search, browser | allowed |
| `network_allowlist` | restrict network to listed hostnames | all allowed |
| `cron` | scheduling | allowed |
| `memory_write` | memory capture | allowed |
| `spawn` | sub-agent/job spawning | allowed |
| `filesystem_roots` | restrict file access to listed paths | unrestricted |
| `file_write_scope` | `full` or `read_only` | full |

Denied tool calls return a structured `ToolDenial` with reason and remediation hint.

**Files:** `internal/tools/policy.go`

### Emergency Stop (estop)
`/estop on` disables dangerous tool families (local, ssh, browser, cron, message) instantly. `/estop off` re-enables. Status visible in `/status`, TUI, and `ok-gobot estop status`.

**Files:** `internal/cli/estop.go`, `internal/tools/tools.go`

### Exec Approval
Dangerous commands (`rm -rf`, `kill`, `shutdown`, `DROP TABLE`, `sudo`, `curl|sh`, etc.) trigger a Telegram inline keyboard with Approve/Deny buttons. Auto-deny after 60 seconds.

**Files:** `internal/bot/approval.go`, `internal/bot/bot_approval.go`

### DM Authorization
Three modes:
- **open** -- anyone can use the bot (default)
- **allowlist** -- only configured user IDs + DB-authorized users
- **pairing** -- requires pairing code from admin (`/auth pair` generates 6-digit code, `/pair <code>` to activate)

Unknown modes are denied (fail-closed). Brute-force protection: 5 failed pairing attempts triggers 15-minute lockout.

**Files:** `internal/bot/auth.go`

### Group Activation Modes
- **active** -- bot responds to all messages
- **standby** -- bot responds only to @mentions, replies to its messages, or messages starting with its name

Per-group, stored in DB. Commands: `/activate`, `/standby`.

**Files:** `internal/bot/groups.go`

### Rate Limiting & Debouncing
- Per-chat rate limiter: 10 requests/minute sliding window
- Message debouncer: 1.5s window batches rapid messages into single AI request

**Files:** `internal/bot/ratelimit.go`, `internal/bot/debounce.go`

### SSRF Protection
web_fetch validates URLs before requests: blocks localhost, private IPs (10.x, 172.16-31.x, 192.168.x, fc00::/7), resolves DNS first to prevent rebinding. Redirect targets are revalidated.

**Files:** `internal/tools/web_fetch.go`

### Log Redaction
Masks sensitive patterns in log output: API keys (sk-...), Bearer tokens, bot tokens, long hex/base64 strings.

**Files:** `internal/redact/redact.go`

### Message Sanitization
- `SanitizeShellArg` -- escapes shell metacharacters
- `SanitizeTelegramMarkdown` -- escapes MarkdownV2 special chars
- `StripControlChars` -- removes non-printable characters

**Files:** `internal/sanitize/sanitize.go`

---

## Infrastructure

### HTTP API
REST API with API key authentication (`X-API-Key` header or Bearer token). Endpoints:
- `GET /api/health` -- no auth
- `GET /api/status` -- bot status
- `POST /api/send` -- send message to chat
- `POST /api/webhook` -- forward event to configured chat

CORS restricted to loopback origins.

See [API.md](API.md) for full reference.

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

### Git Worktree Management
`ok-gobot work <task>` creates a git worktree with a dedicated worker agent for the task. Worktrees provide isolated working copies for parallel development.

```bash
ok-gobot work <task>            # Spawn worktree + worker
ok-gobot worktrees list         # Show tracked worktrees
ok-gobot worktrees cleanup      # Remove merged/stale worktrees
ok-gobot worktrees rm <id>      # Delete specific worktree
```

**Files:** `internal/cli/worktrees.go`

---

## Message Processing

### Debug Logging
Level-aware logging system with `debug`, `info`, `warn`, `error` levels. Set via `config set log_level debug`. Automatically updates on config hot-reload.

**Files:** `internal/logger/logger.go`

### Token Tracking
Per-chat accumulation of prompt/completion tokens from API responses. Displayed in `/status` and optional usage footer after each response.

**Files:** `internal/bot/usage.go`, `internal/bot/agent_handler.go`

### Fragment Buffering
Reassembles long messages that Telegram splits into multiple fragments. Detects continuation by same user, message ID gap <= 1, time gap <= 1.5s. Buffers up to 12 parts / 50K chars.

**Files:** `internal/bot/fragment_buffer.go`

### Queue Modes
Controls how incoming messages are handled during an active AI run:
- **interrupt** (default) -- cancel current run, process new message fresh
- **collect** -- buffer silently, process after run completes
- **steer** -- feed new messages as steering input to the active run

Commands: `/queue collect|steer|interrupt [debounce_ms]`

**Files:** `internal/bot/queue.go`

### Usage Footer
Optional token usage display appended to AI responses. Modes: `off` (default), `tokens`, `full`.

Commands: `/usage off|tokens|full`

**Files:** `internal/bot/usage.go`

---

## Media Handling

### Photos
Downloads from Telegram, extracts dimensions and size, processes through AI pipeline with caption. Supports media groups (multiple photos sent together) via timer-based buffering.

### Voice Messages
Receives voice messages with duration info. Transcription not yet implemented.

### Stickers
Extracts emoji from sticker, processes through AI pipeline.

### Documents
Extracts filename and size, processes through AI pipeline with caption.

### Media Group Buffering
Collects photos that arrive as a Telegram media group (same `media_group_id`). Waits 1.5s for more photos before flushing as a batch. Max file size: 10MB.

**Files:** `internal/bot/media_handler.go`

---

## Telegram Commands

### Extended Commands
Beyond the core commands (`/start`, `/help`, `/status`, `/clear`, `/model`, `/agent`), the bot registers:

| Command | Description |
|---------|-------------|
| `/whoami` | Show your user ID, username, and chat ID |
| `/new` | Reset session (clear history + model override + agent) |
| `/note <text>` | Append a quick note directly to today's memory file |
| `/stop` | Cancel the currently running AI request |
| `/commands` | List all registered commands |
| `/usage` | Set usage footer mode (off/tokens/full) |
| `/context` | Show context window usage percentage |
| `/compact` | Force context compaction |
| `/think` | Set thinking level (off/low/medium/high) |
| `/verbose` | Toggle verbose mode |
| `/queue` | Set queue mode (collect/steer/interrupt) |
| `/tts` | Set TTS voice |
| `/estop` | Toggle dangerous tool families on/off/status (admin for on/off) |
| `/restart` | Restart the bot process (admin only) |

**Files:** `internal/bot/commands.go`, `internal/bot/status.go`

### BotFather Registration
All commands are automatically registered with BotFather on startup via `bot.api.SetCommands()`, enabling Telegram's slash command autocomplete.

---

## Group Migration

Handles Telegram group-to-supergroup migration events. When a group becomes a supergroup, the chat ID changes. The bot automatically migrates session data, model overrides, active agents, and group mode to the new chat ID.

**Files:** `internal/bot/migration.go`

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
