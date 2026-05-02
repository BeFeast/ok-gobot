# ok-gobot

A Go-based Telegram agent runtime and operator control-plane prototype.

ok-gobot is intended to become a feature-compatible replacement for the personal
[OpenClaw](https://github.com/openclaw/openclaw) workflows used by this project,
with opinionated scope reductions such as no WhatsApp or Slack surface. It is not
yet feature-compatible. Current support focuses on Telegram command handling,
durable jobs, roles, tools, storage, and selected native workflows.

## Current Reality

The acceptance bar for this project is OpenClaw workflow parity: a user sends a
Telegram command, ok-gobot performs the workflow itself, and the user receives the
expected result or artifact. Infrastructure-only progress does not satisfy that
bar.

Known gaps:

- OpenClaw-style executable skills are not generally supported.
- `skills` are currently markdown-first knowledge packages, not workflow plugins.
- Memory parity with OpenClaw/QMD is not proven by a live smoke deployment.
- AI chat requires provider configuration and is not part of the lightweight
  smoke instance by default.

Native workflow parity currently exists only for explicitly implemented flows,
such as `/video_summary <youtube_url>`.

Competitive landscape: [docs/COMPETITORS.md](docs/COMPETITORS.md).

## Why Go?

The rewrite was not only about startup time or memory usage. The hard behavioral requirement is non-blocking responsiveness: if a user sends a second message while the bot is in a long-running tool call, the new message must interrupt the active run and get a response immediately instead of being silently buffered.

| Metric | TypeScript (OpenClaw) | Go (ok-gobot) |
|--------|----------------------|---------------|
| Startup | 5,000ms | 15ms |
| Binary | 197MB (node_modules) | 18MB |
| Memory | ~100MB | ~10MB |

## Quick Start

Install a release artifact from [GitHub Releases](https://github.com/BeFeast/ok-gobot/releases) when possible. Each release archive has a matching `.sha256` file and is built by the tagged release workflow.

```bash
version=v0.3.0
artifact=ok-gobot_${version}_linux_amd64.tar.gz

curl -LO "https://github.com/BeFeast/ok-gobot/releases/download/${version}/${artifact}"
curl -LO "https://github.com/BeFeast/ok-gobot/releases/download/${version}/${artifact}.sha256"
shasum -a 256 -c "${artifact}.sha256"
tar -xzf "${artifact}"
sudo install -m 0755 ok-gobot /usr/local/bin/ok-gobot

ok-gobot version
```

Use the `darwin` artifact for macOS. See [INSTALL.md](docs/INSTALL.md) for source builds and detailed setup.

```bash
# 1. Build
git clone https://github.com/BeFeast/ok-gobot.git
cd ok-gobot
make build        # or: go build -o ok-gobot ./cmd/ok-gobot
export PATH="$PWD/bin:$PATH"

# 2. Initialize config
ok-gobot config init

# 3. Authenticate with your AI provider
ok-gobot config set ai.provider openrouter
ok-gobot config set ai.api_key <OPENROUTER_KEY>
# or: ok-gobot auth anthropic login
# or: ok-gobot config set ai.provider openai
#     ok-gobot config set ai.api_key <OPENAI_KEY>

# 4. Set Telegram bot token
ok-gobot config set telegram.token <TOKEN_FROM_BOTFATHER>

# 5. Verify setup
ok-gobot doctor

# 6. Run
ok-gobot start
```

**Requirements:** Go 1.24+, C compiler (for SQLite CGO).

## AI Providers

| Provider | Auth | Config |
|----------|------|--------|
| OpenRouter | API key | `ai.provider: openrouter` (default) |
| Anthropic | OAuth (Claude MAX) or API key | `ok-gobot auth anthropic login` or `ai.provider: anthropic` |
| ChatGPT Codex | ChatGPT session JWT | `ai.provider: chatgpt` + `ai.api_key` |
| OpenAI | API key | `ai.provider: openai` |
| Custom OpenAI-compatible | API key + base URL | `ai.provider: custom` + `ai.base_url` |
| Hermes models | Ollama / OpenAI-compatible | `ai.provider: custom` + a Hermes model ID |
| Gemini | API key via Google's OpenAI-compatible endpoint | `ai.provider: custom` + `ai.base_url` |
| Droid | CLI agent transport | `ai.provider: droid` |

Multi-model routing: tag messages with `[task:vision]`, `[task:coding]`, `[task:summarize]`, or `[task:reasoning]` to route to different models.

See [INSTALL.md](docs/INSTALL.md) for detailed provider setup.

## Features

### AI & LLM
- **Multi-provider** -- OpenRouter, Anthropic, ChatGPT Codex, OpenAI, Droid, and custom OpenAI-compatible endpoints including Gemini, Ollama/vLLM, and Hermes models
- **Native tool calling** -- structured `tools` API, not text parsing
- **Model failover** -- automatic fallback chain with cooldown (`ai.fallback_models`)
- **Multi-model routing** -- task-type tags route to different models (`internal/ai/router.go`)
- **Per-session model override** -- `/model claude-sonnet-4-5` per chat
- **Multi-agent system** -- multiple personalities, models, tool sets per agent (`/agent`)
- **Context compaction** -- AI-powered summarization when approaching token limits
- **Streaming responses** -- live message editing with rate limiting
- **CLI agent transport** -- use Factory Droid, Claude Code, Codex, Gemini CLI, or OpenCode as backends

### Roles & Jobs
- **Prebuilt role templates** -- `researcher`, `monitor`, `release-watch`, and `homelab-runbook` ship as embedded markdown manifests
- **Custom roles** -- define new roles declaratively (markdown + YAML frontmatter, no Go code)
- **Scheduled execution** -- roles copied into `roles_path` can run via cron and deliver bounded reports to the admin chat
- **Durable jobs** -- role, cron, background, and retry history tracked in SQLite with standardized reports and artifacts
- **Rules-first chat routing** -- incoming turns classified as reply, clarify, or background job

> **Note:** Scheduled autonomy is experimental. Tool-call and duration limits exist, but centralized token/cost budget enforcement and the full policy gateway are still roadmap work. Set conservative tool allowlists for scheduled roles.

### Skills & Evolution
- **Markdown-first skills** -- installable knowledge bases with safety audit (`ok-gobot skills install`)
- **Skill versioning** -- version history with rollback
- **Utility scoring** -- skills tracked by usefulness; skill router selects relevant skills per query
- **Self-evolution** -- A-Evolve inspired prompt improvement (observe/analyze/evolve/gate/promote)
- **Automatic reflection** -- tool failure analysis and fix suggestions after repeated errors

### Tools
| Tool | Description |
|------|-------------|
| `local` | Execute shell commands (with approval for dangerous ops) |
| `ssh` | Remote command execution |
| `file` | Read/write files in allowed directory |
| `patch` | Apply unified diffs |
| `grep` | Recursive regex file search |
| `obsidian` | Obsidian vault notes |
| `search` | Web search (Brave, Exa) |
| `web_fetch` | Fetch URLs with readability extraction |
| `browser` | Chrome automation (ChromeDP) |
| `browser_task` | Composite browser tasks as sub-agent runs |
| `frontend_verify` | CDP screenshot + LLM visual comparison |
| `image_gen` | DALL-E 3 image generation |
| `tts` | Text-to-speech (OpenAI + Edge TTS) |
| `memory_search` | Semantic search over indexed markdown memory |
| `memory_get` | Read markdown memory source by section path |
| `message` | Send messages to other chats |
| `cron` | Scheduled tasks |
| `recommend_roles` | Suggest roles for a task |
| `denial` | Policy decision logging |

### Security & Control
- **Per-agent capability policy** -- declarative restrictions (shell, network, filesystem, cron, spawn) without source changes
- **Emergency stop** -- `/estop on` instantly disables dangerous tool families
- **Exec approval** -- dangerous commands require inline keyboard confirmation
- **DM authorization** -- open, allowlist, or pairing code modes (`/auth`, `/pair`)
- **Group activation** -- active or standby with mention detection (`/activate`, `/standby`)
- **Rate limiting** -- per-chat debouncing and request throttling
- **SSRF protection** -- blocks private IPs and redirect chains in web_fetch
- **Symlink escape prevention** -- path resolution blocks symlinks escaping workspace
- **CORS restriction** -- loopback-only origins for API and control server
- **Log redaction** -- masks API keys and tokens in logs
- **XSS protection** -- DOMPurify sanitization in web UI

### Message Processing
- **Token tracking** -- per-chat prompt/completion token accumulation with optional usage footer
- **Fragment buffering** -- reassembles Telegram-split long messages (>4000 chars)
- **Queue modes** -- interrupt (default), plus collect or steer for concurrent messages during active AI runs
- **Voice/STT** -- Whisper API transcription for Telegram voice messages
- **Media handling** -- photos, voice, stickers, documents with media group batching
- **Group migration** -- automatic session migration on group->supergroup conversion
- **Session fork** -- fork sessions to explore alternatives without losing the original
- **Debug logging** -- level-aware logging (`debug`/`info`/`warn`/`error`) with hot-reload

### Infrastructure
- **Mission Control API** -- profiles, schedules, runs, stats, estop, and provider/model endpoints for dashboards
- **HTTP REST API** -- health, status, send, webhook, jobs, and Mission Control endpoints (port 8080)
- **WebSocket control protocol** -- real-time session control, streaming, approvals (port 8787)
- **Config hot-reload** -- fsnotify watcher + `/reload` command
- **Daemon management** -- launchd (macOS) / systemd (Linux) via `ok-gobot daemon`
- **Doctor diagnostics** -- `ok-gobot doctor` validates config and dependencies
- **Auto-worktree management** -- isolated git worktrees per background task

## Telegram Commands

Core commands are registered with BotFather for slash autocomplete. `/commands` lists the full runtime command set.

| Command | Description |
|---------|-------------|
| `/start` | Greeting |
| `/help` | List commands |
| `/status` | Rich status: version, model, tokens, context, uptime |
| `/clear` | Clear conversation history |
| `/new` | Full session reset (history + model + agent) |
| `/note <text>` | Quick-capture note to today's memory file |
| `/stop` | Cancel active AI request |
| `/abort` | Abort active AI request |
| `/memory` | Show today's memory |
| `/tools` | List available tools |
| `/model [name|list|clear]` | View/change AI model |
| `/agent [name|list]` | View/switch agent |
| `/whoami` | Show user ID, username, chat ID |
| `/commands` | List all registered commands |
| `/usage [off|tokens|full]` | Token usage footer mode |
| `/context` | Show context window usage % |
| `/compact` | Force context compaction |
| `/think [off|low|medium|high|adaptive]` | Set thinking level |
| `/verbose` | Toggle verbose mode |
| `/queue [collect|steer|interrupt]` | Queue mode for concurrent messages |
| `/tts [voice]` | Set TTS voice |
| `/task <description>` | Spawn a sub-agent task |
| `/btw` | Side query during active task |
| `/roles` | List available roles |
| `/role <name>` | Show role details |
| `/role_run <name> [input]` | Run a role as a durable job (admin) |
| `/jobs` | List recent durable jobs |
| `/job <id>` | Show job details |
| `/job_cancel <id>` | Cancel a durable job (admin) |
| `/estop [on|off|status]` | Emergency-stop dangerous tool families (admin) |
| `/activate` | Group: respond to all messages |
| `/standby` | Group: respond only to mentions |
| `/auth [add|remove|list|pair]` | Manage authorization (admin) |
| `/pair <code>` | Pair with bot using code |
| `/reload` | Hot-reload config (admin) |
| `/restart` | Restart bot process (admin) |

## Configuration

Config file: `~/.ok-gobot/config.yaml` (see [config.example.yaml](config.example.yaml))
Canonical key reference: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#11-configuration-reference-canonical)

```yaml
telegram:
  token: "BOT_TOKEN"

ai:
  provider: "openrouter"  # openrouter | openai | anthropic | chatgpt | droid | custom
  api_key: "..."          # or oauth:<auto> after: ok-gobot auth anthropic login
  model: "moonshotai/kimi-k2.5"
  fallback_models:
    - "claude-haiku-3-5-20241022"

auth:
  mode: "open"           # open | allowlist | pairing
  admin_id: 123456789

groups:
  default_mode: "standby"  # active | standby

tts:
  provider: "edge"       # edge (free) | openai
  default_voice: "ru-RU-DmitryNeural"

memory:
  enabled: false
  embeddings_model: "text-embedding-3-small"
  # Optional: index Obsidian/shared markdown roots alongside MEMORY.md.
  # Sources surface as "extra:<name>/..." in memory_search/memory_get.
  # See docs/MEMORY.md for full options.
  extra_paths: []

control:
  enabled: false         # disabled by default for security
  port: 8787

api:
  enabled: false
  port: 8080
  api_key: "secret"

log_level: "info"          # debug | info | warn | error
```

Environment variables: prefix `OKGOBOT_` (e.g. `OKGOBOT_TELEGRAM_TOKEN`).

## CLI Commands

```bash
ok-gobot start                    # Start bot
ok-gobot config init              # Create default config
ok-gobot config show              # Show config
ok-gobot config set <key> <val>   # Set config value
ok-gobot config models            # List available models
ok-gobot auth anthropic login     # Anthropic OAuth login (Claude MAX)
ok-gobot status                   # Show status
ok-gobot estop on|off|status      # Toggle emergency stop for dangerous tools
ok-gobot doctor                   # Check config and dependencies
ok-gobot daemon install|start|stop|status|logs|uninstall
ok-gobot providers                # List configured AI providers
ok-gobot models list              # List available models per provider
ok-gobot models refresh           # Refresh provider model cache
ok-gobot skills list|install|remove|audit|history|rollback  # Manage skills
ok-gobot jobs list|inspect|cancel|retry|tail|export          # Manage job history
ok-gobot roles list|show|run|enable|disable                  # Manage roles
ok-gobot memory status|index      # Inspect/index markdown memory
ok-gobot sessions list|fork       # Manage sessions
ok-gobot work <task>              # Create a worker worktree for a task
ok-gobot worktrees list|cleanup|rm  # Manage worker worktrees
ok-gobot batch                    # Parallel task fan-out
ok-gobot babysit                  # PR auto-maintenance
ok-gobot evolution status|history|rollback|metrics
ok-gobot export training-data     # Export training data
ok-gobot migrate --from <db>      # One-shot OpenClaw migration
ok-gobot onboard --path <dir>     # Scaffold workspace and config
ok-gobot voice                    # Voice command processing
ok-gobot browser setup|status     # Prepare Chrome automation profile
ok-gobot web                      # Launch web UI
ok-gobot tui                      # Terminal UI
ok-gobot version
```

## Agent System

ok-gobot loads personality files from a configurable directory (default `~/ok-gobot-soul/`):

| File | Purpose |
|------|---------|
| `IDENTITY.md` | Agent name and emoji |
| `SOUL.md` | Personality and values |
| `USER.md` | User context |
| `AGENTS.md` | Agent protocol |
| `TOOLS.md` | Tool configuration (SSH hosts, API keys) |
| `MEMORY.md` | Long-term memory (private sessions only) |
| `memory/YYYY-MM-DD.md` | Daily notes |

## Project Structure

```
ok-gobot/
├── cmd/ok-gobot/         # Entry point
├── internal/
│   ├── agent/            # Personality, memory, safety, compactor, registry, reflection, hooks
│   ├── ai/               # AI clients, failover, router, catalog, types
│   ├── api/              # HTTP API server
│   ├── app/              # Application orchestrator
│   ├── bootstrap/        # Skills install, audit, versioning
│   ├── bot/              # Telegram bot, commands, media, queue, status, usage, routing
│   ├── browser/          # Chrome automation
│   ├── cli/              # Cobra CLI (start, config, doctor, daemon, auth, skills, jobs, etc.)
│   ├── config/           # YAML config, watcher
│   ├── configschema/     # Schema generation
│   ├── control/          # WebSocket control server, hub, TUI protocol, mission API
│   ├── cron/             # Job scheduler, role execution, report delivery
│   ├── errorx/           # Error handling
│   ├── evolution/        # Self-evolution engine (A-Evolve)
│   ├── logger/           # Level-aware debug logging
│   ├── memory/           # Markdown-backed memory index (embeddings, store)
│   ├── memorymcp/        # Memory MCP server
│   ├── migrate/          # Database migrations
│   ├── redact/           # Log redaction
│   ├── role/             # Role manifest parser, bundled roles, loader
│   ├── runtime/          # Chat/jobs mailbox runtime, session scheduling, chat router
│   ├── sanitize/         # Input sanitization
│   ├── session/          # Context monitoring
│   ├── storage/          # SQLite persistence
│   ├── tools/            # All agent tools
│   └── tui/              # Terminal UI client
├── web/                  # Web UI (HTML/JS)
├── docs/                 # Documentation
└── Makefile
```

## Security

See [SECURITY.md](SECURITY.md) for the security policy, threat model, and hardening checklist.

## Documentation

- [Competitive Landscape](docs/COMPETITORS.md) -- OpenFang, ZeroClaw, OpenClaw, and ok-gobot comparison
- [Roadmap](docs/ROADMAP.md) -- Shipped features and future implementation backlog
- [Installation Guide](docs/INSTALL.md) -- Setup, configuration, providers, deployment
- [API Reference](docs/API.md) -- HTTP REST API and WebSocket control protocol
- [Architecture](docs/ARCHITECTURE.md) -- Chat/jobs architecture contract, legacy-runtime freeze, and canonical config reference
- [Features](docs/FEATURES.md) -- Detailed feature descriptions (roles, skills, evolution, tools, security)
- [Mission Control](docs/MISSION-CONTROL.md) -- Mission Control v1 concept overview
- [Tools Reference](docs/TOOLS.md) -- All tools with usage examples
- [Memory](docs/MEMORY.md) -- Semantic memory system
- [Security Policy](SECURITY.md) -- Vulnerability reporting
- [Security Fixes](docs/SECURITY-FIXES.md) -- Security hardening changelog
- [TTS](docs/TTS_USAGE.md) / [TTS (RU)](docs/TTS_USAGE_RU.md) -- Text-to-speech setup
- [Changelog](docs/CHANGELOG.md)

## Development

```bash
make deps     # Install dependencies
make build    # Build binary
make test     # Run tests
make dev      # Development mode
```

## License

MIT
