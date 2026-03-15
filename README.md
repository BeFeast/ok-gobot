# ok-gobot

A fast, single-binary Telegram bot with AI agent capabilities. Ground-up Go rewrite of [OpenClaw](https://github.com/openclaw/openclaw) with opinionated defaults for personal use.

Competitive landscape: [docs/COMPETITORS.md](docs/COMPETITORS.md).

## Why Go?

The rewrite was not only about startup time or memory usage. The hard behavioral requirement is non-blocking responsiveness: if a user sends a second message while the bot is in a long-running tool call, the new message must interrupt the active run and get a response immediately instead of being silently buffered.

| Metric | TypeScript (OpenClaw) | Go (ok-gobot) |
|--------|----------------------|---------------|
| Startup | 5,000ms | 15ms |
| Binary | 197MB (node_modules) | 18MB |
| Memory | ~100MB | ~10MB |

## Quick Start

```bash
# 1. Build
git clone https://github.com/BeFeast/ok-gobot.git
cd ok-gobot
make build        # or: go build -o ok-gobot ./cmd/ok-gobot

# 2. Initialize config
ok-gobot config init

# 3. Authenticate with your AI provider
ok-gobot auth anthropic login     # Anthropic OAuth (Claude MAX)
# or: ok-gobot auth chatgpt login # ChatGPT Plus OAuth
# or: ok-gobot config set ai.provider openai
#     ok-gobot config set ai.api_key <YOUR_KEY>

# 4. Set Telegram bot token
ok-gobot config set telegram.token <TOKEN_FROM_BOTFATHER>

# 5. Verify setup
ok-gobot doctor

# 6. Run
ok-gobot start
```

**Requirements:** Go 1.23+, C compiler (for SQLite CGO).

## AI Providers

| Provider | Auth | Config |
|----------|------|--------|
| Anthropic | OAuth (Claude MAX) | `ok-gobot auth anthropic login` |
| ChatGPT | OAuth (Plus/Team) | `ok-gobot auth chatgpt login` |
| OpenAI | API key | `ai.provider: openai` |
| Gemini | API key via custom | `ai.provider: custom` + `ai.base_url` |
| Droid | CLI agent transport | `ai.provider: droid` |

See [INSTALL.md](docs/INSTALL.md) for detailed provider setup.

## Features

### AI & LLM
- **Multi-provider** -- Anthropic, ChatGPT, OpenAI, Gemini, Droid, any OpenAI-compatible endpoint
- **Native tool calling** -- structured `tools` API, not text parsing
- **Model failover** -- automatic fallback chain with cooldown (`ai.fallback_models`)
- **Per-session model override** -- `/model claude-sonnet-4-5` per chat
- **Multi-agent system** -- multiple personalities, models, tool sets per agent (`/agent`)
- **Context compaction** -- AI-powered summarization when approaching token limits
- **Streaming responses** -- live message editing with rate limiting
- **CLI agent transport** -- use Factory Droid, Claude Code, Codex, Gemini CLI, or OpenCode as backends

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
| `image_gen` | DALL-E 3 image generation |
| `tts` | Text-to-speech (OpenAI + Edge TTS) |
| `memory_search` | Semantic search over indexed markdown memory |
| `memory_get` | Read markdown memory source by section path |
| `message` | Send messages to other chats |
| `cron` | Scheduled tasks |

### Security & Control
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
- **Media handling** -- photos, voice, stickers, documents with media group batching
- **Group migration** -- automatic session migration on group->supergroup conversion
- **Debug logging** -- level-aware logging (`debug`/`info`/`warn`/`error`) with hot-reload

### Infrastructure
- **HTTP REST API** -- health, status, send, webhook endpoints (port 8080)
- **WebSocket control protocol** -- real-time session control, streaming, approvals (port 8787)
- **Mission control web UI** -- chat plus `Team`, `Schedule`, and `Run Log` views over configured roles, cron jobs, failures, and daily delivered value
- **Config hot-reload** -- fsnotify watcher + `/reload` command
- **Daemon management** -- launchd (macOS) / systemd (Linux) via `ok-gobot daemon`
- **Doctor diagnostics** -- `ok-gobot doctor` validates config and dependencies

## Telegram Commands

All commands are auto-registered with BotFather for slash autocomplete.

| Command | Description |
|---------|-------------|
| `/start` | Greeting |
| `/help` | List commands |
| `/status` | Rich status: version, model, tokens, context, uptime |
| `/clear` | Clear conversation history |
| `/new` | Full session reset (history + model + agent) |
| `/note <text>` | Quick-capture note to today's memory file |
| `/stop` | Cancel active AI request |
| `/memory` | Show today's memory |
| `/tools` | List available tools |
| `/model [name\|list\|clear]` | View/change AI model |
| `/agent [name\|list]` | View/switch agent |
| `/whoami` | Show user ID, username, chat ID |
| `/commands` | List all registered commands |
| `/usage [off\|tokens\|full]` | Token usage footer mode |
| `/context` | Show context window usage % |
| `/compact` | Force context compaction |
| `/think [off\|low\|medium\|high]` | Set thinking level |
| `/verbose` | Toggle verbose mode |
| `/queue [collect\|steer\|interrupt]` | Queue mode for concurrent messages |
| `/tts [voice]` | Set TTS voice |
| `/estop [on\|off\|status]` | Emergency-stop dangerous tool families (admin) |
| `/activate` | Group: respond to all messages |
| `/standby` | Group: respond only to mentions |
| `/auth [add\|remove\|list\|pair]` | Manage authorization (admin) |
| `/pair <code>` | Pair with bot using code |
| `/reload` | Hot-reload config (admin) |
| `/restart` | Restart bot process (admin) |

## Configuration

Config file: `~/.ok-gobot/config.yaml` (see [config.example.yaml](config.example.yaml))
Canonical key reference: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#8-configuration-reference-canonical)

```yaml
telegram:
  token: "BOT_TOKEN"

ai:
  provider: "anthropic"   # anthropic | chatgpt | openai | droid | custom
  api_key: "oauth:<auto>" # set by: ok-gobot auth anthropic login
  model: "claude-sonnet-4-5-20250929"
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
ok-gobot auth chatgpt login       # ChatGPT OAuth login (Plus/Team)
ok-gobot status                   # Show status
ok-gobot estop on|off|status      # Toggle emergency stop for dangerous tools
ok-gobot doctor                   # Check config and dependencies
ok-gobot daemon install|start|stop|status|logs|uninstall
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
│   ├── agent/            # Personality, memory, safety, compactor, registry
│   ├── ai/               # AI client, failover, types
│   ├── api/              # HTTP API server
│   ├── app/              # Application orchestrator
│   ├── bootstrap/        # First-run onboarding
│   ├── bot/              # Telegram bot, commands, media, queue, status, usage
│   ├── browser/          # Chrome automation
│   ├── cli/              # Cobra CLI (start, config, doctor, daemon, auth)
│   ├── config/           # YAML config, watcher
│   ├── configschema/     # Schema generation
│   ├── control/          # WebSocket control server, hub, TUI protocol
│   ├── cron/             # Job scheduler
│   ├── errorx/           # Error handling
│   ├── logger/           # Level-aware debug logging
│   ├── memory/           # Markdown-backed memory index (embeddings, store)
│   ├── memorymcp/        # Memory MCP server
│   ├── migrate/          # Database migrations
│   ├── redact/           # Log redaction
│   ├── runtime/          # Chat/jobs mailbox runtime, session scheduling
│   ├── sanitize/         # Input sanitization
│   ├── session/          # Context monitoring
│   ├── storage/          # SQLite persistence
│   ├── tools/            # All agent tools
│   └── tui/              # Terminal UI client
├── web/                  # Web UI (HTML/JS)
├── docs/                 # Documentation
└── Makefile
```

## Documentation

- [Competitive Landscape](docs/COMPETITORS.md) -- OpenFang, ZeroClaw, OpenClaw, and ok-gobot comparison
- [Roadmap](docs/ROADMAP.md) -- Implementation backlog derived from the competitor analysis
- [Installation Guide](docs/INSTALL.md) -- Setup, configuration, providers, deployment
- [API Reference](docs/API.md) -- HTTP REST API and WebSocket control protocol
- [Architecture](docs/ARCHITECTURE.md) -- Chat/jobs architecture contract, legacy-runtime freeze, and canonical config reference
- [Features](docs/FEATURES.md) -- Detailed feature descriptions
- [Tools Reference](docs/TOOLS.md) -- All tools with usage examples
- [Memory](docs/MEMORY.md) -- Semantic memory system
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
