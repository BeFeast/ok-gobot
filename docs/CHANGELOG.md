# Changelog

## [Unreleased] - 2026-01-29

### Added

#### Message Processing & UX
- Debug logging system with level-aware output (`internal/logger`)
- Token tracking from API responses with per-chat accumulation
- Rich `/status` command: version, git commit, model, tokens, context %, compactions, uptime
- Usage footer after AI responses (configurable: off/tokens/full)
- Fragment buffering for Telegram-split long messages (>4000 chars)
- Queue modes: collect, steer, interrupt for handling messages during active AI runs
- Media group buffering for batch photo processing
- Group-to-supergroup migration handler (session, model, agent, mode migration)
- BotFather command registration on startup (slash command autocomplete)

#### New Telegram Commands
- `/whoami` — show user/chat info
- `/new` — full session reset
- `/stop` — cancel active AI run
- `/commands` — list all registered commands
- `/usage` — configure usage footer mode
- `/context` — show context window usage
- `/compact` — force context compaction
- `/think` — set thinking level (off/low/medium/high)
- `/verbose` — toggle verbose mode
- `/queue` — set queue mode with optional debounce
- `/tts` — set TTS voice
- `/restart` — restart bot process (admin only)

#### Media Handlers
- Photo handler with download, dimension extraction, and AI pipeline processing
- Voice message handler (transcription pending)
- Sticker handler with emoji extraction
- Document handler with filename/size extraction

#### New Files
- `internal/logger/logger.go` — Level-aware logging
- `internal/bot/commands.go` — Extended command handlers
- `internal/bot/status.go` — Rich status display
- `internal/bot/usage.go` — Token usage tracking and footer
- `internal/bot/fragment_buffer.go` — Fragment reassembly
- `internal/bot/media_handler.go` — Photo/voice/sticker/document handlers
- `internal/bot/migration.go` — Group migration handler
- `internal/bot/queue.go` — Queue mode manager

#### Database
- New session columns: `input_tokens`, `output_tokens`, `total_tokens`, `context_tokens`, `usage_mode`, `think_level`, `verbose`, `queue_mode`, `queue_debounce_ms`
- New storage methods: `UpdateTokenUsage`, `SetContextTokens`, `GetTokenUsage`, `ResetSession`, `GetSessionOption`, `SetSessionOption`, `GetVerbose`, `SetVerbose`

#### AI & LLM
- Native OpenAI tool calling API with parallel tool execution and iterative workflows
- Model failover/fallback chain with 60s cooldown per failed model
- Per-session model override via `/model` command
- Multi-agent system with per-agent personality, model, and tool filtering
- Agent registry with `/agent` command for runtime switching

#### Security & Control
- Exec approval workflow for dangerous commands (inline keyboard approve/deny)
- DM authorization system: open, allowlist, and pairing code modes
- Group chat activation modes: active and standby with mention detection
- Per-chat rate limiting (10 req/min sliding window)
- Message debouncing (1.5s batching window)
- SSRF protection in web_fetch (blocks private IPs, DNS rebinding prevention)
- Log redaction for API keys, Bearer tokens, bot tokens
- Message sanitization (shell args, Telegram markdown, control chars)

#### Tools
- `patch` tool for applying unified diffs
- `grep` tool for recursive regex file search
- `memory` tool for semantic vector memory with embeddings
- Edge TTS provider (free, no API key required)
- Enhanced web content extraction with go-readability

#### Infrastructure
- HTTP API server with health, status, send, webhook endpoints
- Config hot-reload via fsnotify with `/reload` command
- Daemon management (launchd on macOS, systemd on Linux)
- Doctor diagnostic command checking config and dependencies
- Typing indicators during AI processing

#### Packages
- `internal/api/` — HTTP API server and middleware
- `internal/memory/` — Semantic memory (embeddings, store, manager)
- `internal/redact/` — Log redaction
- `internal/sanitize/` — Input sanitization

### Dependencies
- Added `github.com/go-shiori/go-readability` for article extraction
- Added `github.com/fsnotify/fsnotify` for config watching

---

## [0.2.0] - Previous

### Added
- Streaming responses with live Telegram message editing
- Media handling: photos, voice, audio, documents (PDF, TXT, MD, etc.)
- Message tool for cross-chat messaging
- Session management with full conversation history
- Context compaction with AI-powered summarization
- Cron scheduler with persistent SQLite storage
- Web fetch tool with HTML content extraction
- Image generation via DALL-E 3
- Text-to-speech via OpenAI TTS
- Heartbeat system with IMAP email monitoring
- Browser automation tool via ChromeDP

---

## [0.1.0] - Initial

### Added
- Telegram Bot API support via telebot
- OpenRouter/OpenAI compatible AI client
- Personality system (SOUL.md, IDENTITY.md, USER.md)
- Memory system (daily notes, MEMORY.md)
- Safety rules (stop phrases)
- Tool calling agent
- Tools: local, ssh, file, obsidian, browser, search
- SQLite storage
- Cobra CLI with config management
