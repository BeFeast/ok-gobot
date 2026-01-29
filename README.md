# ok-gobot

A fast, single-binary Telegram bot with AI agent capabilities. Go reimplementation of [Moltbot](https://github.com/BeFeast/clawdbot), focused on Telegram and OpenRouter.

## Why Go?

| Metric | TypeScript (Moltbot) | Go (ok-gobot) |
|--------|---------------------|---------------|
| Startup | 5,000ms | 15ms |
| Binary | 197MB (node_modules) | 18MB |
| Memory | ~100MB | ~10MB |

## Quick Start

```bash
# 1. Build
git clone https://github.com/BeFeast/ok-gobot.git
cd ok-gobot
make build        # or: go build -o ok-gobot ./cmd/moltbot

# 2. Initialize config
ok-gobot config init

# 3. Set tokens
ok-gobot config set telegram.token <TOKEN_FROM_BOTFATHER>
ok-gobot config set ai.api_key <YOUR_OPENROUTER_KEY>

# 4. Verify setup
ok-gobot doctor

# 5. Run
ok-gobot start
```

**Requirements:** Go 1.23+, C compiler (for SQLite CGO).

## Features

### AI & LLM
- **Native OpenAI tool calling** — structured `tools` API, not text parsing
- **Model failover** — automatic fallback chain with cooldown (`ai.fallback_models`)
- **Per-session model override** — `/model claude-3.5-sonnet` per chat
- **Multi-agent system** — multiple personalities, models, tool sets per agent (`/agent`)
- **Context compaction** — AI-powered summarization when approaching token limits
- **Streaming responses** — live message editing with rate limiting

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
| `memory` | Semantic memory with embeddings |
| `message` | Send messages to other chats |
| `cron` | Scheduled tasks |

### Security & Control
- **Exec approval** — dangerous commands require inline keyboard confirmation
- **DM authorization** — open, allowlist, or pairing code modes (`/auth`, `/pair`)
- **Group activation** — active or standby with mention detection (`/activate`, `/standby`)
- **Rate limiting** — per-chat debouncing and request throttling
- **SSRF protection** — blocks private IPs in web_fetch
- **Log redaction** — masks API keys and tokens in logs
- **Message sanitization** — shell, markdown, and control char escaping

### Infrastructure
- **HTTP API** — health, status, send, webhook endpoints
- **Config hot-reload** — fsnotify watcher + `/reload` command
- **Daemon management** — launchd (macOS) / systemd (Linux) via `ok-gobot daemon`
- **Doctor diagnostics** — `ok-gobot doctor` validates config and dependencies

## Telegram Commands

| Command | Description |
|---------|-------------|
| `/start` | Greeting |
| `/help` | List commands |
| `/status` | Bot status, model, config |
| `/clear` | Clear conversation history |
| `/memory` | Show today's memory |
| `/tools` | List available tools |
| `/model [name\|list\|clear]` | View/change AI model |
| `/agent [name\|list]` | View/switch agent |
| `/activate` | Group: respond to all messages |
| `/standby` | Group: respond only to mentions |
| `/auth [add\|remove\|list\|pair]` | Manage authorization (admin) |
| `/pair <code>` | Pair with bot using code |
| `/reload` | Hot-reload config (admin) |

## Configuration

Config file: `~/.ok-gobot/config.yaml` (see [config.example.yaml](config.example.yaml))

```yaml
telegram:
  token: "BOT_TOKEN"

ai:
  provider: "openrouter"
  api_key: "sk-or-..."
  model: "moonshotai/kimi-k2.5"
  fallback_models:
    - "anthropic/claude-3.5-sonnet"
    - "openai/gpt-4o-mini"

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

api:
  enabled: false
  port: 8080
  api_key: "secret"
```

Environment variables: prefix `OKGOBOT_` (e.g. `OKGOBOT_TELEGRAM_TOKEN`).

## CLI Commands

```bash
ok-gobot start                    # Start bot
ok-gobot config init              # Create default config
ok-gobot config show              # Show config
ok-gobot config set <key> <val>   # Set config value
ok-gobot config models            # List available models
ok-gobot status                   # Show status
ok-gobot doctor                   # Check config and dependencies
ok-gobot daemon install|start|stop|status|logs|uninstall
ok-gobot version
```

## Agent System

ok-gobot loads personality files from a configurable directory (default `~/clawd/`):

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
├── cmd/moltbot/          # Entry point
├── internal/
│   ├── agent/            # Personality, memory, safety, compactor, registry
│   ├── ai/               # AI client, failover, types
│   ├── api/              # HTTP API server
│   ├── app/              # Application orchestrator
│   ├── bot/              # Telegram bot, typing, groups, auth, approval, debounce
│   ├── browser/          # Chrome automation
│   ├── cli/              # Cobra CLI (start, config, doctor, daemon)
│   ├── config/           # YAML config, watcher
│   ├── cron/             # Job scheduler
│   ├── errorx/           # Error handling
│   ├── memory/           # Semantic memory (embeddings, store)
│   ├── redact/           # Log redaction
│   ├── sanitize/         # Input sanitization
│   ├── session/          # Context monitoring
│   ├── storage/          # SQLite persistence
│   └── tools/            # All agent tools
├── docs/                 # Documentation
└── Makefile
```

## Documentation

- [API Reference](API.md) — HTTP API endpoints
- [Features](docs/FEATURES.md) — Detailed feature descriptions
- [Architecture](docs/ARCHITECTURE.md) — System design and data flow
- [Tools Reference](docs/TOOLS.md) — All tools with usage examples
- [Changelog](docs/CHANGELOG.md)
- [Daemon](docs/DAEMON.md) — Service management
- [Memory](docs/MEMORY.md) — Semantic memory system
- [TTS](docs/TTS_USAGE.md) — Text-to-speech setup
- [TTS (RU)](docs/TTS_USAGE_RU.md)

## Development

```bash
make deps     # Install dependencies
make build    # Build binary
make test     # Run tests
make dev      # Development mode
```

## License

MIT
