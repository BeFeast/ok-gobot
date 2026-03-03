# ok-gobot Architecture

## Overview

ok-gobot is a Telegram bot with AI agent capabilities. Telegram-only, OpenRouter-only (any OpenAI-compatible API). Single binary, ~18MB, ~15ms startup.

## Directory Structure

```
ok-gobot/
├── cmd/ok-gobot/             # Entry point
├── internal/
│   ├── agent/                # AI agent logic
│   │   ├── compactor.go      # Context compaction
│   │   ├── heartbeat.go      # Proactive monitoring
│   │   ├── memory.go         # Daily notes & long-term memory
│   │   ├── personality.go    # SOUL.md, IDENTITY.md loading
│   │   ├── registry.go       # Multi-agent registry
│   │   ├── safety.go         # Stop phrases, approval rules
│   │   ├── tokens.go         # Token counting
│   │   └── tool_agent.go     # Native tool-calling agent
│   ├── ai/
│   │   ├── client.go         # OpenRouter/OpenAI client
│   │   ├── failover.go       # Model failover with cooldown
│   │   └── types.go          # OpenAI tool calling types
│   ├── api/
│   │   ├── server.go         # HTTP API server
│   │   └── middleware.go     # Auth, CORS, logging
│   ├── app/
│   │   └── app.go            # Application orchestrator
│   ├── bot/
│   │   ├── bot.go            # Telegram bot, core handlers, BotFather registration
│   │   ├── agent_command.go  # /agent command
│   │   ├── agent_handler.go  # Multi-agent request routing + token tracking
│   │   ├── approval.go       # Exec approval (dangerous commands)
│   │   ├── auth.go           # DM authorization manager
│   │   ├── commands.go       # Extended commands (/whoami, /stop, /new, etc.)
│   │   ├── config_reload.go  # /reload command
│   │   ├── debounce.go       # Message debouncing
│   │   ├── fragment_buffer.go # Telegram message fragment reassembly
│   │   ├── groups.go         # Group activation modes
│   │   ├── media.go          # Legacy photo/voice/document handling
│   │   ├── media_handler.go  # Photo/voice/sticker/document + media groups
│   │   ├── migration.go      # Group-to-supergroup migration
│   │   ├── queue.go          # Queue mode manager (collect/steer/interrupt)
│   │   ├── ratelimit.go      # Per-chat rate limiting
│   │   ├── status.go         # Rich /status command
│   │   ├── stream_editor.go  # Streaming message editor
│   │   ├── typing.go         # Typing indicators
│   │   └── usage.go          # Token usage tracking + footer
│   ├── browser/
│   │   └── manager.go        # Chrome automation (ChromeDP)
│   ├── cli/
│   │   ├── root.go           # Cobra root command
│   │   ├── start.go          # Bot startup
│   │   ├── config.go         # Config management
│   │   ├── doctor.go         # Diagnostics
│   │   ├── daemon.go         # Service management
│   │   └── status.go         # Status command
│   ├── config/
│   │   ├── config.go         # YAML config loading
│   │   └── watcher.go        # Config hot-reload (fsnotify)
│   ├── control/
│   │   ├── server.go         # Runtime/bot WS control server
│   │   ├── protocol.go       # Runtime/bot WS protocol
│   │   ├── hub.go            # Runtime/bot WS event hub
│   │   ├── tui_server.go     # Standalone TUI WS control server
│   │   ├── tui_session.go    # In-memory TUI session manager
│   │   ├── tui_hub.go        # TUI WS client broadcast hub
│   │   └── tui_types.go      # TUI WS protocol types
│   ├── cron/
│   │   └── scheduler.go      # Cron job scheduler
│   ├── errorx/
│   │   └── handler.go        # Error handling with levels
│   ├── logger/
│   │   └── logger.go         # Level-aware debug logging
│   ├── memory/
│   │   ├── embeddings.go     # Embedding API client
│   │   ├── manager.go        # Remember/Recall coordinator
│   │   └── store.go          # SQLite vector store
│   ├── redact/
│   │   └── redact.go         # Log redaction
│   ├── sanitize/
│   │   └── sanitize.go       # Input sanitization
│   ├── session/
│   │   └── monitor.go        # Context usage monitoring
│   ├── storage/
│   │   └── sqlite.go         # SQLite persistence
│   ├── tui/
│   │   ├── tui.go            # Bubble Tea entrypoint
│   │   ├── model.go          # Main TUI state machine
│   │   ├── client.go         # WS transport client
│   │   └── styles.go         # TUI presentation
│   └── tools/
│       ├── tools.go          # Tool registry & interface
│       ├── browser_tool.go   # Chrome automation tool
│       ├── cron.go           # Cron tool
│       ├── image_gen.go      # DALL-E tool
│       ├── memory_tool.go    # Semantic memory tool
│       ├── message.go        # Cross-chat messaging tool
│       ├── obsidian.go       # Obsidian vault tool
│       ├── patch.go          # Unified diff tool
│       ├── readability.go    # Article extraction
│       ├── search.go         # Web search tool
│       ├── search_file.go    # Grep tool
│       ├── tts.go            # TTS tool (multi-provider)
│       ├── tts_edge.go       # Edge TTS provider
│       └── web_fetch.go      # URL fetch with SSRF protection
├── docs/
├── go.mod
├── Makefile
└── README.md
```

## Component Diagram

```
┌──────────────────────────────────────────────────────────────┐
│                       Telegram API                           │
└──────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                        Bot Layer                             │
│  ┌──────────┐ ┌──────────┐ ┌────────┐ ┌─────────┐ ┌──────┐ │
│  │ handlers │ │  media   │ │ stream │ │ groups  │ │ auth │ │
│  │ commands │ │ photo/   │ │ editor │ │ active/ │ │ pair │ │
│  │ /model   │ │ voice/   │ │        │ │ standby │ │      │ │
│  │ /agent   │ │ docs     │ │        │ │         │ │      │ │
│  └──────────┘ └──────────┘ └────────┘ └─────────┘ └──────┘ │
│  ┌──────────┐ ┌──────────┐ ┌────────┐ ┌─────────┐ ┌──────┐ │
│  │ typing   │ │ approval │ │debounce│ │ratelimit│ │reload│ │
│  └──────────┘ └──────────┘ └────────┘ └─────────┘ └──────┘ │
│  ┌──────────┐ ┌──────────┐ ┌────────┐ ┌─────────┐ ┌──────┐ │
│  │ fragment │ │  queue   │ │ usage  │ │migration│ │status│ │
│  │ buffer   │ │ manager  │ │tracker │ │ handler │ │      │ │
│  └──────────┘ └──────────┘ └────────┘ └─────────┘ └──────┘ │
└──────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                       Agent Layer                            │
│  ┌──────────┐ ┌──────────┐ ┌────────┐ ┌─────────┐ ┌──────┐ │
│  │tool_agent│ │personality│ │ memory │ │registry │ │safety│ │
│  │ native   │ │ SOUL.md  │ │ daily  │ │ multi-  │ │ stop │ │
│  │ tool API │ │          │ │ notes  │ │ agent   │ │words │ │
│  └──────────┘ └──────────┘ └────────┘ └─────────┘ └──────┘ │
│  ┌──────────┐ ┌──────────┐                                  │
│  │compactor │ │heartbeat │                                  │
│  └──────────┘ └──────────┘                                  │
└──────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                       Tools Layer                            │
│  ┌──────┐ ┌─────┐ ┌──────┐ ┌───────┐ ┌──────┐ ┌─────────┐  │
│  │local │ │ ssh │ │search│ │browser│ │ cron │ │ message │  │
│  └──────┘ └─────┘ └──────┘ └───────┘ └──────┘ └─────────┘  │
│  ┌──────┐ ┌─────┐ ┌──────┐ ┌───────┐ ┌──────┐ ┌─────────┐  │
│  │ file │ │patch│ │ grep │ │  tts  │ │image │ │ memory  │  │
│  └──────┘ └─────┘ └──────┘ └───────┘ └──────┘ └─────────┘  │
│  ┌──────┐ ┌─────────┐                                       │
│  │obsid.│ │web_fetch│                                       │
│  └──────┘ └─────────┘                                       │
└──────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                    Infrastructure Layer                       │
│  ┌──────────┐ ┌──────────┐ ┌────────┐ ┌──────┐ ┌──────────┐ │
│  │AI Client │ │ Storage  │ │  Cron  │ │Config│ │ HTTP API │ │
│  │+Failover │ │ (SQLite) │ │Scheduler│ │Watch │ │          │ │
│  └──────────┘ └──────────┘ └────────┘ └──────┘ └──────────┘ │
│  ┌──────────┐ ┌──────────┐ ┌────────┐ ┌──────┐               │
│  │ Redact   │ │ Sanitize │ │Embedded│ │Logger│               │
│  │          │ │          │ │Memory  │ │      │               │
│  └──────────┘ └──────────┘ └────────┘ └──────┘               │
└──────────────────────────────────────────────────────────────┘
```

## Message Processing Flow

```
1. Telegram sends update via long polling
2. Rate limiter checks per-chat limit (10/min)
3. If rate limited → friendly "wait N seconds" reply
4. Auth check (open / allowlist / pairing)
5. Group activation check (active vs standby + mention detection)
6. Safety check (stop phrases: "стоп", "stop", etc.)
7. Queue mode check — if active run:
   - collect: buffer silently
   - steer: enqueue as steering input
   - interrupt: cancel active run, proceed
8. Fragment buffer — reassemble split messages (>4000 chars, same user, ID gap ≤ 1)
9. Debouncer accumulates messages (1.5s window, configurable per queue mode)
10. Queue manager marks run as active
11. Typing indicator starts (refreshes every 4s)
12. Message saved to storage + daily memory
13. Active agent profile resolved (per-session)
14. Cancellable context created, registered in activeRuns map
15. ToolCallingAgent processes with native tool API:
    a. Build system prompt with personality + tools schema
    b. Send to AI via streaming or non-streaming
    c. If response contains tool_calls → execute tools
    d. Send tool results back, loop (max 10 iterations)
    e. If dangerous command → request approval via inline keyboard
16. Token usage recorded (prompt + completion tokens)
17. Usage footer appended if enabled (tokens/full mode)
18. Response sent to user (live editing if streaming)
19. Typing indicator stopped
20. Queue manager marks run complete, returns buffered messages
21. Session state saved
```

## Database Schema

```sql
CREATE TABLE messages (
    id INTEGER PRIMARY KEY,
    chat_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    user_id INTEGER, username TEXT, content TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
    id INTEGER PRIMARY KEY,
    chat_id INTEGER UNIQUE NOT NULL,
    state TEXT,
    message_count INTEGER DEFAULT 0,
    last_summary TEXT,
    compaction_count INTEGER DEFAULT 0,
    model_override TEXT DEFAULT '',
    group_mode TEXT DEFAULT '',
    active_agent TEXT DEFAULT 'default',
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    total_tokens INTEGER DEFAULT 0,
    context_tokens INTEGER DEFAULT 0,
    usage_mode TEXT DEFAULT '',
    think_level TEXT DEFAULT '',
    verbose INTEGER DEFAULT 0,
    queue_mode TEXT DEFAULT '',
    queue_debounce_ms INTEGER DEFAULT 0,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE session_messages (
    id INTEGER PRIMARY KEY,
    session_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL,
    role TEXT NOT NULL, content TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cron_jobs (
    id INTEGER PRIMARY KEY,
    expression TEXT NOT NULL, task TEXT NOT NULL,
    chat_id INTEGER NOT NULL,
    next_run DATETIME, enabled INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE authorized_users (
    id INTEGER PRIMARY KEY,
    user_id INTEGER UNIQUE, username TEXT,
    authorized_at TIMESTAMP, paired_by TEXT
);

CREATE TABLE memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    content TEXT NOT NULL,
    embedding BLOB NOT NULL,
    category TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

## Key Design Decisions

1. **Single binary** — no runtime dependencies (optional: ffmpeg, whisper, pdftotext, Chrome, edge-tts)
2. **SQLite** — embedded database, zero config, portable
3. **Native tool calling** — uses OpenAI `tools` API, not text parsing
4. **Streaming first** — live message editing for better UX
5. **Telegram-only** — no multi-channel complexity
6. **OpenRouter-compatible** — works with any OpenAI-compatible API
7. **Multi-agent** — switchable personalities without restart
8. **Defense in depth** — SSRF, sanitization, exec approval, auth, rate limiting
