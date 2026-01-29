# ok-gobot Architecture

## Overview

ok-gobot is a Telegram bot with AI agent capabilities, designed for fast startup and minimal resource usage. It's a Go reimplementation of Moltbot, focused on Telegram-only and OpenRouter-only operation.

## Directory Structure

```
ok-gobot/
├── cmd/
│   └── moltbot/
│       └── main.go          # Entry point
├── internal/
│   ├── agent/               # AI agent logic
│   │   ├── compactor.go     # Context compaction
│   │   ├── heartbeat.go     # Proactive monitoring
│   │   ├── memory.go        # Daily notes & long-term memory
│   │   ├── personality.go   # SOUL.md, IDENTITY.md loading
│   │   ├── safety.go        # Stop phrases, approval rules
│   │   ├── tokens.go        # Token counting
│   │   └── tool_agent.go    # Tool-calling agent
│   ├── ai/
│   │   └── client.go        # OpenRouter/OpenAI client
│   ├── app/
│   │   └── app.go           # Application orchestrator
│   ├── bot/
│   │   ├── bot.go           # Telegram bot logic
│   │   ├── heartbeat.go     # Bot heartbeat runner
│   │   ├── media.go         # Media handling
│   │   └── stream_editor.go # Streaming message editor
│   ├── browser/
│   │   └── manager.go       # Chrome automation
│   ├── cli/                 # Cobra CLI commands
│   │   ├── root.go
│   │   ├── start.go
│   │   ├── config.go
│   │   └── ...
│   ├── config/
│   │   └── config.go        # YAML configuration
│   ├── cron/
│   │   └── scheduler.go     # Cron job scheduler
│   ├── errorx/
│   │   └── handler.go       # Error handling
│   ├── session/
│   │   └── monitor.go       # Session monitoring
│   ├── storage/
│   │   └── sqlite.go        # SQLite persistence
│   └── tools/               # Agent tools
│       ├── tools.go         # Tool registry
│       ├── browser_tool.go
│       ├── cron.go
│       ├── image_gen.go
│       ├── message.go
│       ├── obsidian.go
│       ├── search.go
│       ├── tts.go
│       └── web_fetch.go
├── docs/                    # Documentation
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

## Component Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                         Telegram API                             │
└─────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────┐
│                           Bot Layer                              │
│  ┌─────────────┐  ┌──────────────┐  ┌────────────────────────┐  │
│  │   bot.go    │  │   media.go   │  │   stream_editor.go     │  │
│  │  (handlers) │  │  (photo/     │  │   (live editing)       │  │
│  │             │  │   voice/doc) │  │                        │  │
│  └─────────────┘  └──────────────┘  └────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────┐
│                         Agent Layer                              │
│  ┌─────────────┐  ┌──────────────┐  ┌────────────────────────┐  │
│  │ tool_agent  │  │  personality │  │       memory           │  │
│  │ (reasoning) │  │  (SOUL.md)   │  │   (daily notes)        │  │
│  └─────────────┘  └──────────────┘  └────────────────────────┘  │
│  ┌─────────────┐  ┌──────────────┐  ┌────────────────────────┐  │
│  │  compactor  │  │    safety    │  │      heartbeat         │  │
│  │ (summarize) │  │ (stop words) │  │   (proactive)          │  │
│  └─────────────┘  └──────────────┘  └────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────┐
│                         Tools Layer                              │
│  ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐ ┌────────────────┐ │
│  │ local  │ │  ssh   │ │ search │ │browser │ │    cron        │ │
│  └────────┘ └────────┘ └────────┘ └────────┘ └────────────────┘ │
│  ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐ ┌────────────────┐ │
│  │  file  │ │obsidian│ │web_fetch│ │image_gen│ │     tts       │ │
│  └────────┘ └────────┘ └────────┘ └────────┘ └────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────┐
│                      Infrastructure Layer                        │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐  │
│  │   AI Client     │  │    Storage      │  │  Cron Scheduler │  │
│  │  (OpenRouter)   │  │   (SQLite)      │  │  (robfig/cron)  │  │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

## Data Flow

### Message Processing

```
1. User sends message to Telegram
2. Bot receives update via long polling
3. Safety check (stop phrases)
4. Message saved to storage
5. Message appended to daily memory
6. ToolCallingAgent processes request:
   a. Build system prompt with tools
   b. Send to AI (streaming or non-streaming)
   c. Parse tool calls from response
   d. Execute tools if requested
   e. Get final response
7. Response sent back to user (with live editing if streaming)
8. Session state saved
```

### Streaming Flow

```
1. Send initial "Thinking..." message
2. Start AI streaming request
3. For each chunk received:
   a. Append to StreamEditor buffer
   b. If rate limit allows, edit Telegram message
4. On completion, final edit with complete content
5. Save to memory and session
```

## Database Schema

```sql
-- Messages (raw log)
CREATE TABLE messages (
    id INTEGER PRIMARY KEY,
    chat_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    user_id INTEGER,
    username TEXT,
    content TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Sessions (conversation state)
CREATE TABLE sessions (
    id INTEGER PRIMARY KEY,
    chat_id INTEGER UNIQUE NOT NULL,
    state TEXT,
    message_count INTEGER DEFAULT 0,
    last_summary TEXT,
    compaction_count INTEGER DEFAULT 0,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Session Messages (full history)
CREATE TABLE session_messages (
    id INTEGER PRIMARY KEY,
    session_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Cron Jobs
CREATE TABLE cron_jobs (
    id INTEGER PRIMARY KEY,
    expression TEXT NOT NULL,
    task TEXT NOT NULL,
    chat_id INTEGER NOT NULL,
    next_run DATETIME,
    enabled INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

## Configuration

Configuration is stored in `~/.ok-gobot/config.yaml`:

```yaml
telegram:
  token: "bot-token"

ai:
  provider: "openrouter"
  api_key: "api-key"
  model: "moonshotai/kimi-k2.5"
  base_url: ""  # Optional, uses default

storage:
  path: "~/.ok-gobot/ok-gobot.db"

log_level: "info"
```

## Key Design Decisions

1. **Single Binary**: No external dependencies at runtime (except optional tools like ffmpeg)

2. **SQLite Storage**: Embedded database for simplicity and portability

3. **Tool Interface**: Simple `Execute(ctx, args...) (string, error)` pattern for all tools

4. **Streaming First**: Streaming responses enabled by default for better UX

5. **Telegram-Only**: Focused scope, no multi-channel complexity

6. **OpenRouter-Compatible**: Works with any OpenAI-compatible API

7. **Memory System**: Daily markdown notes + long-term MEMORY.md file

8. **Safety Rules**: Stop phrases immediately halt all actions
