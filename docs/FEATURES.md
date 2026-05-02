# ok-gobot Features

## AI & LLM

### Multi-Provider Support
Provider backends with automatic failover:

| Provider | Auth | Default Model |
|----------|------|---------------|
| OpenRouter | API key | moonshotai/kimi-k2.5 |
| Anthropic | OAuth (Claude MAX) or API key | claude-sonnet-4-5-20250929 |
| ChatGPT Codex | ChatGPT session JWT | gpt-5.4 |
| OpenAI | API key | gpt-4o |
| Droid | CLI agent transport | glm-5 |
| Custom | API key + base URL | any OpenAI-compatible, including Gemini, Ollama/vLLM, and Hermes models |

**Files:** `internal/ai/anthropic_client.go`, `internal/ai/chatgpt_client.go`, `internal/ai/client.go`, `internal/ai/hermes_client.go`, `internal/ai/droid_client.go`

### Native OpenAI Tool Calling
Uses the structured `tools` API parameter instead of parsing JSON from text responses. Supports parallel tool calls and iterative multi-step workflows (up to 10 rounds). Falls back to text-based parsing for models without tool support. Hermes models get a native tool call parser.

**Files:** `internal/ai/types.go`, `internal/ai/client.go`, `internal/agent/tool_agent.go`

### Model Failover
Automatic fallback chain when the primary model fails. Retryable errors: 429, 500-504, context_length_exceeded. Models go on 60-second cooldown after failure. Thread-safe.

**Config:** `ai.fallback_models: ["claude-haiku-3-5-20241022"]`
**Files:** `internal/ai/failover.go`

### Multi-Model Routing by Task Type
Routes requests to different models based on task type tags in messages:
- `[task:vision]` -- image/vision tasks
- `[task:coding]` -- code generation/review
- `[task:summarize]` -- fast/cheap summarization
- `[task:reasoning]` -- complex reasoning

Falls back to the default model when no tag is present. Tags are stripped before sending to the AI.

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
    model: "claude-sonnet-4-5-20250929"
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

---

## Roles, Jobs, and Skills

### Markdown-First Roles
Roles are single `.md` files with optional YAML frontmatter. They define bounded workflows that can run manually or on a schedule and deliver reports.

**Manifest format:**
```markdown
---
worker: standard
tools: [web_fetch, search]
schedule: "0 0 9 * * *"
approval: auto
---
# My Researcher

You are a research agent. Search for news about Go releases...
```

**Prebuilt roles:**

| Role | Default Schedule | Description |
|------|-----------------|-------------|
| `researcher` | Manual only | Web search + bounded research brief |
| `monitor` | Every 30 minutes once copied to `roles_path` | Service/URL health checks + status report |
| `release-watch` | Weekly once copied to `roles_path` | Software release tracking for configured projects |
| `homelab-runbook` | Manual only | Turn requests into checklist/runbook notes |

Bundled roles are templates. They are listed by CLI and Telegram commands, but only manifests copied into `roles_path` are auto-registered as scheduled cron jobs. No bundled role schedules run by default.

Roles respect capability policy, explicit `tools` allowlists, estop, and per-run tool-call/duration limits. Token/cost budget enforcement and the centralized policy gateway are still roadmap work, so scheduled autonomy should be treated as experimental.

**Files:** `internal/role/manifest.go`, `internal/role/bundled.go`, `internal/role/loader.go`

### Durable Jobs
Scheduled roles create durable jobs tracked by the runtime. Jobs produce standardized reports delivered to the admin chat. Job history is persisted in SQLite.

**CLI:** `ok-gobot jobs list|inspect|cancel|retry|tail|export` -- manage job history, events, and artifacts.

**Telegram:** `/jobs`, `/job <id>`, `/job_cancel <id>`.

**Files:** `internal/cron/scheduler.go`, `internal/cron/roles.go`, `internal/cron/report.go`

### Skills System
Skills are markdown-only knowledge bases installable from local paths or git URLs. A static safety audit runs before installation:

- Rejects symlinks
- Blocks script files (.sh, .py, .rb, .pl, .js, etc.)
- Detects pipe-to-shell patterns (`curl | sh`, `wget | bash`)
- Blocks markdown links escaping the skill root (`../`)

Skills are tracked by utility score. The skill router selects relevant skills per query based on accumulated scores.

**CLI:** `ok-gobot skills list|install|remove|audit|history|rollback`

**Files:** `internal/bootstrap/skills.go`, `internal/bootstrap/skills_test.go`

### Skill Versioning
Skills support version history with rollback. Each modification creates a versioned snapshot that can be restored.

---

## Intelligence and Feedback Loops

### Rules-First Chat Routing
Incoming chat turns are classified before execution:
- **reply** -- lightweight turns stay on the inline AI reply path
- **clarification** -- underspecified work requests get a short follow-up question first
- **background job** -- obvious heavy work (investigate, debug, fix, implement, etc.) is launched as an isolated task

Detection uses lead-phrase scoring, context terms, code block presence, and message length. Forced prefixes (`job:`, `task:`, `background:`) bypass heuristics.

**Files:** `internal/runtime/chat_router.go`, `internal/bot/chat_routing.go`

### Automatic Reflection
Tracks tool execution failures asynchronously. After repeated failures (default threshold: 3), triggers analysis and suggests fixes based on error patterns (not found, timeout, permission, parse error, connection failure).

**Files:** `internal/agent/reflection.go`

### Self-Evolution (A-Evolve Inspired)
Autonomous prompt improvement cycle: Observe -> Analyze -> Evolve -> Gate -> Promote.

**Safety constraints:**
- Max 1 evolution per 24 hours
- Human approval required for >20% prompt diff
- Benchmark gating before promotion
- Auto-rollback after 3 production failures on a new version
- Versioned snapshots with manifests

**CLI:** `ok-gobot evolution` -- check status and history.

**Files:** `internal/evolution/engine.go`

### Agent Lifecycle Hooks
Four hook points for custom behavior: `SessionStart`, `PreToolUse`, `PostToolUse`, `SessionEnd`.

---

## Tools

### Shell & Files
- **local** -- Execute shell commands. Dangerous commands (rm -rf, kill, shutdown, etc.) require inline keyboard approval.
- **ssh** -- Remote execution for hosts configured in `TOOLS.md`.
- **file** -- Read/write within the configured workspace with path traversal protection.
- **patch** -- Apply unified diffs within the configured workspace.
- **grep** -- Recursive regex search, skips binary files and `.git`/`node_modules`. Max 50 results.
- **obsidian** -- Obsidian vault CRUD with frontmatter timestamps when `~/Obsidian` exists.

### Web
- **search** -- Brave Search or Exa API when a search API key is configured. Returns 5 results with title/URL/snippet.
- **web_fetch** -- Fetch URLs with Mozilla Readability extraction (go-shiori/go-readability). Falls back to basic HTML stripping. SSRF protection blocks private IPs. 12KB content limit.
- **browser** -- Chrome automation via ChromeDP: navigate, click, fill, screenshot, wait, extract text.
- **browser_task** -- Composite browser tasks as isolated sub-agent runs.
- **frontend_verify** -- CDP screenshot + LLM visual comparison for UI testing.

### Media
- **image_gen** -- DALL-E 3 when an OpenAI-compatible image API key is configured. Sizes: 1024x1024, 1792x1024, 1024x1792. Quality: standard/hd.
- **tts** -- Two providers when TTS is configured: OpenAI (paid, 6 voices) and Edge TTS (free, Russian/English voices). Auto OGG conversion for Telegram.
- **youtube_karaoke** -- Telegram `/youtube_karaoke <youtube_url>` downloads YouTube subtitles with `yt-dlp`, generates an LRC karaoke artifact natively, stores it in `youtube_karaoke.output_dir`, and sends the file back to Telegram.

### Memory & Scheduling
- **memory_search** -- Semantic vector search over indexed markdown memory.
- **memory_get** -- Read markdown memory source by section path.
- **cron** -- 5-field expressions are accepted by the tool and stored as 6-field cron entries with seconds. Persistent in SQLite. Enable/disable without deletion.
- **message** -- Send to other chats by ID or alias. Allowlist-based security.

### Policy & Routing
- **recommend_roles** -- Suggest appropriate roles for a given task.
- **denial** -- DENY/ALLOW policy decision logging.

---

## Security & Control

### Per-Agent Capability Policy
Fine-grained declarative restrictions per agent profile:

| Capability | Type | Description |
|-----------|------|-------------|
| `shell` | bool | Allow shell tools (local, ssh) |
| `network` | bool | Allow network tools (web_fetch, search, browser) |
| `network_allowlist` | list | Restrict network to specific public hostnames |
| `allow_internal_networks` | bool | Allow loopback/private/link-local targets; blocked by default |
| `cron` | bool | Allow cron scheduling |
| `memory_write` | bool | Allow memory write tools |
| `spawn` | bool | Allow sub-agent/job spawning |
| `filesystem_roots` | list | Restrict filesystem access to specific paths |
| `file_write_scope` | enum | `full` or `read_only` |

Default: fully permissive (backward compatible). Denied actions return structured messages.

**Files:** `internal/config/config.go`, `internal/agent/registry.go`

### Emergency Stop
`/estop on` instantly disables dangerous tool families (local, ssh, browser, cron, message). `/estop off` re-enables. State visible in `/status` and TUI. CLI: `ok-gobot estop on|off|status`.

**Files:** `internal/cli/estop.go`, `internal/tools/tools.go`

### Exec Approval
Dangerous commands (`rm -rf`, `kill`, `shutdown`, `DROP TABLE`, etc.) trigger a Telegram inline keyboard with Approve/Deny buttons. Auto-deny after 60 seconds.

**Files:** `internal/bot/approval.go`, `internal/bot/bot_approval.go`

### DM Authorization
Three modes:
- **open** -- anyone can use the bot (default)
- **allowlist** -- only configured user IDs + DB-authorized users
- **pairing** -- requires pairing code from admin (`/auth pair` generates 6-digit code, `/pair <code>` to activate)

Unknown/misconfigured modes are denied (fail-closed).

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
web_fetch validates URLs before requests: blocks localhost, private IPs (10.x, 172.16-31.x, 192.168.x, fc00::/7), resolves DNS first to prevent rebinding.

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

See [API.md](API.md) for full reference.

### Mission Control API
Monitoring endpoints for the operator dashboard, served by the authenticated HTTP API when `api.enabled` is true:
- `GET /api/mission/roles` -- list registered agent profiles with metadata
- `GET /api/mission/schedules` -- list scheduled jobs with next run time
- `GET /api/mission/runs` -- recent job runs with status and summary
- `GET /api/mission/stats` -- daily token/message/session statistics
- `GET /api/mission/estop` -- current emergency-stop state
- `GET /api/mission/providers` -- active AI provider and model

See [MISSION-CONTROL.md](MISSION-CONTROL.md) for the concept overview.

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
Receives voice messages. Transcription via Whisper API (OpenAI-compatible endpoint).

### Stickers
Extracts emoji from sticker, processes through AI pipeline.

### Documents
Extracts filename and size, processes through AI pipeline with caption.

### Media Group Buffering
Collects photos that arrive as a Telegram media group (same `media_group_id`). Waits 1.5s for more photos before flushing as a batch. Max file size: 10MB.

**Files:** `internal/bot/media_handler.go`

---

## Session Management

### Session Fork/Branch
Fork a session to explore alternatives without losing the original conversation. Useful for comparing different approaches to a problem.

### Side Queries (`/btw`)
Ask questions during active task execution without interrupting the running task.

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
| `/abort` | Abort the currently running AI request |
| `/commands` | List all registered commands |
| `/usage` | Set usage footer mode (off/tokens/full) |
| `/context` | Show context window usage percentage |
| `/compact` | Force context compaction |
| `/think` | Set thinking level (off/low/medium/high/adaptive) |
| `/verbose` | Toggle verbose mode |
| `/queue` | Set queue mode (collect/steer/interrupt) |
| `/tts` | Set TTS voice |
| `/task` | Spawn a sub-agent task |
| `/roles` | List available roles |
| `/role <name>` | Show role details |
| `/role_run <name> [input]` | Run a role as a durable job (admin) |
| `/jobs` | List recent durable jobs |
| `/job <id>` | Show job details |
| `/job_cancel <id>` | Cancel a durable job (admin) |
| `/btw` | Side query during active task |
| `/estop` | Toggle dangerous tool families on/off/status (admin for on/off) |
| `/restart` | Restart the bot process (admin only) |

**Files:** `internal/bot/commands.go`, `internal/bot/role_commands.go`, `internal/bot/status.go`

### Roles CLI

Manage roles from the command line:

```bash
ok-gobot roles list                         # List all roles (disk + bundled)
ok-gobot roles show <name>                  # Show role details and prompt
ok-gobot roles run <name> [--input "..."]   # Run role as a durable job
ok-gobot roles enable <name>                # Enable a scheduled role's cron job
ok-gobot roles disable <name>               # Disable a scheduled role's cron job
```

**Files:** `internal/cli/roles.go`

### BotFather Registration
Core commands are registered with BotFather on startup via `bot.api.SetCommands()`, enabling Telegram slash autocomplete. `/commands` lists the full runtime command set, including role/job handlers that may not be in the BotFather autocomplete list.

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

---

## Prebuilt Roles

ok-gobot ships a small pack of prebuilt role manifests as markdown files with YAML frontmatter. Roles are disabled by default — operators copy them to their `roles_path` directory to activate.

Each role defines allowed tools, a default budget (max tool calls per run), a report template, and an approval mode.

| Role | Purpose | Default tools | Schedule |
|---|---|---|---|
| `release-watch` | Watch repos/releases and report changes | `web_fetch`, `search`, `memory_get`, `memory_search` | Weekly (disabled by default) |
| `monitor` | Check URLs/services and post status changes | `web_fetch` | Every 30 min (disabled by default) |
| `researcher` | Run bounded research and produce a brief | `search`, `web_fetch`, `memory_search` | Manual only |
| `homelab-runbook` | Turn requests into checklist/runbook notes | `obsidian`, `memory_get`, `memory_search` | Manual only |

### Usage

1. Copy bundled role templates from `internal/role/prebuilt/` into your roles directory.

2. Set `roles_path` in `config.yaml`:
   ```yaml
   roles_path: ~/my-roles
   ```

3. Roles with a `schedule` field are auto-registered as cron jobs on startup.
   Manual-only roles (no schedule) are available via `/role_run <name>` or `ok-gobot roles run <name>`.

4. Customise tools, budget, schedule, or prompt in your copy — the bundled
   originals are never modified.

**Files:** `internal/role/prebuilt/`, `internal/role/bundled.go`, `internal/role/loader.go`
