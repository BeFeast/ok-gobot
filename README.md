# Moltbot (Go Edition)

A lightweight, fast reimplementation of Moltbot in Go - focused on Telegram integration with AI capabilities.

## 🚀 Features

- **Fast Startup**: ~50ms vs 5s (TypeScript version)
- **Single Binary**: One 15MB executable, no dependencies
- **Telegram Bot**: Full Telegram Bot API support
- **AI Integration**: OpenAI GPT support (expandable)
- **SQLite Storage**: Local persistence
- **Simple Config**: YAML-based configuration

## 📦 Installation

### From Source

```bash
git clone <repository>
cd moltbot-go
make build

# Or install to $GOPATH/bin
make install
```

### Requirements

- Go 1.23+
- C compiler (for SQLite)
- macOS or Linux

## ⚡ Quick Start

1. **Initialize configuration:**
```bash
./bin/moltbot config init
```

2. **Get a Telegram bot token:**
- Message [@BotFather](https://t.me/botfather) on Telegram
- Create a new bot
- Copy the token

3. **Configure the bot:**
```bash
./bin/moltbot config set telegram.token <your-token>
```

4. **(Optional) Add OpenAI:**
```bash
./bin/moltbot config set openai.api_key <your-key>
./bin/moltbot config set openai.model gpt-4
```

5. **Start the bot:**
```bash
./bin/moltbot start
```

## 🛠️ Commands

```bash
moltbot --help                    # Show help
moltbot version                   # Show version
moltbot config init              # Create config file
moltbot config show              # Show current config
moltbot config set <key> <val>   # Set config value
moltbot start                    # Start the bot
moltbot start --daemon           # Run as daemon
```

## ⚙️ Configuration

Configuration is stored in `~/.moltbot/config.yaml`:

```yaml
telegram:
  token: "your-bot-token"

openai:
  api_key: "your-openai-key"
  model: "gpt-4"

storage:
  path: "~/.moltbot/moltbot.db"

log_level: "info"
```

Environment variables (prefix with `MOLTBOT_`):
- `MOLTBOT_TELEGRAM_TOKEN`
- `MOLTBOT_OPENAI_API_KEY`

## 🏗️ Development

```bash
# Install dependencies
make deps

# Build
make build

# Run tests
make test

# Development mode (auto-restart on changes)
make dev

# Build optimized binary
make build-small
```

## 📝 Project Structure

```
moltbot-go/
├── cmd/moltbot/          # Entry point
├── internal/
│   ├── app/              # Application orchestration
│   ├── ai/               # AI client interface
│   ├── bot/              # Telegram bot logic
│   ├── cli/              # CLI commands
│   ├── config/           # Configuration management
│   └── storage/          # SQLite storage
├── go.mod
├── Makefile
└── README.md
```

## 🦞 Why Go?

- **Startup Time**: 50ms vs 5 seconds (TypeScript)
- **Memory**: 10MB vs 100MB+ (Node.js)
- **Binary Size**: 15MB vs 197MB (node_modules)
- **Deployment**: Single static binary
- **Performance**: Native compilation, no JIT warmup

## 🔮 Roadmap

- [x] Telegram bot core
- [x] CLI with Cobra
- [x] SQLite storage
- [x] Configuration management
- [ ] OpenAI integration (complete)
- [ ] TUI with Bubble Tea
- [ ] Webhook mode
- [ ] Multi-provider AI support
- [ ] Plugin system

## 📄 License

MIT
