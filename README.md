# ok-gobot

A fast Go reimplementation of Moltbot with full AI agent capabilities - Telegram bot with personality, memory, and proactive assistance.

## 🚀 Features

- **Fast Startup**: ~15ms vs 5s (TypeScript version)
- **Single Binary**: One 18MB executable, no dependencies
- **Telegram Bot**: Full Telegram Bot API support
- **AI Agent**: Personality system, memory, proactive assistance
- **Multi-Provider**: OpenRouter, OpenAI, or any OpenAI-compatible API
- **SQLite Storage**: Local persistence with daily memory

## 📦 Installation

### From Source

```bash
git clone https://github.com/BeFeast/ok-gobot.git
cd ok-gobot
make build

# Or install to ~/.local/bin
make install
```

### Requirements

- Go 1.23+
- C compiler (for SQLite)
- macOS or Linux

## ⚡ Quick Start

1. **Initialize configuration:**
```bash
ok-gobot config init
```

2. **Get a Telegram bot token:**
- Message [@BotFather](https://t.me/botfather) on Telegram
- Create a new bot
- Copy the token

3. **Configure the bot:**
```bash
ok-gobot config set telegram.token <your-token>
ok-gobot config set ai.api_key <your-openrouter-key>
```

4. **Start the bot:**
```bash
ok-gobot start
```

## 🛠️ Commands

```bash
ok-gobot --help                    # Show help
ok-gobot version                   # Show version
ok-gobot config init              # Create config file
ok-gobot config show              # Show current config
ok-gobot config set <key> <val>   # Set config value
ok-gobot config models            # List available AI models
ok-gobot start                    # Start the bot
ok-gobot status                   # Show bot status
```

## ⚙️ Configuration

Configuration is stored in `~/.ok-gobot/config.yaml`:

```yaml
telegram:
  token: "your-bot-token"

ai:
  provider: "openrouter"
  api_key: "your-api-key"
  model: "moonshotai/kimi-k2.5"

storage:
  path: "~/.ok-gobot/ok-gobot.db"

log_level: "info"
```

Environment variables (prefix with `OKGOBOT_`):
- `OKGOBOT_TELEGRAM_TOKEN`
- `OKGOBOT_AI_API_KEY`

## 🧠 Agent System

ok-gobot implements a full agent architecture:

### Personality System
- Loads from `~/clawd/SOUL.md`, `IDENTITY.md`, `USER.md`
- Responds as **Штрудель** (Shraga) 🕯️
- Maintains personality across sessions

### Memory System
- Daily notes: `~/clawd/memory/YYYY-MM-DD.md`
- Long-term memory: `~/clawd/MEMORY.md`
- Conversation persistence in SQLite

### Safety Features
- Stop phrases ("стоп", "stop") halt all actions
- External action approval for emails/posts
- Group chat participation rules

### Heartbeat System
- Periodic proactive checks
- Email monitoring
- Context usage warnings

## 🏗️ Development

```bash
# Install dependencies
make deps

# Build
make build

# Run tests
make test

# Development mode
make dev
```

## 📝 Project Structure

```
ok-gobot/
├── cmd/moltbot/          # Entry point
├── internal/
│   ├── agent/            # Personality, memory, safety
│   ├── ai/               # AI client (OpenRouter, OpenAI)
│   ├── bot/              # Telegram bot logic
│   ├── cli/              # CLI commands
│   ├── config/           # Configuration management
│   ├── session/          # Context monitoring
│   └── storage/          # SQLite storage
├── go.mod
├── Makefile
└── README.md
```

## 🦞 Why Go?

| Metric | TypeScript (Moltbot) | Go (ok-gobot) |
|--------|---------------------|---------------|
| Startup | 5,000ms | 15ms (300x faster) |
| Binary | 197MB | 18MB (10x smaller) |
| Memory | ~100MB | ~10MB (10x less) |

## 🔮 Roadmap

- [x] Telegram bot core
- [x] CLI with Cobra
- [x] SQLite storage
- [x] OpenRouter/OpenAI integration
- [x] Personality system
- [x] Memory system
- [x] Safety rules
- [ ] Heartbeat automation
- [ ] Tool framework (SSH, Gmail, Obsidian)
- [ ] TUI with Bubble Tea
- [ ] Webhook mode

## 📄 License

MIT
