# ok-gobot Features

This document provides an overview of all features implemented in ok-gobot.

## Table of Contents

1. [Streaming Responses](#streaming-responses)
2. [Media Handling](#media-handling)
3. [Message Tool](#message-tool)
4. [Session Management](#session-management)
5. [Context Compaction](#context-compaction)
6. [Cron Scheduler](#cron-scheduler)
7. [Web Fetch](#web-fetch)
8. [Image Generation](#image-generation)
9. [Text-to-Speech](#text-to-speech)
10. [Heartbeat System](#heartbeat-system)

---

## Streaming Responses

Real-time streaming of AI responses with live message editing in Telegram.

**Files:**
- `internal/ai/client.go` - `CompleteStream()` method
- `internal/bot/stream_editor.go` - Rate-limited message editor
- `internal/bot/bot.go` - Streaming integration

**Features:**
- SSE (Server-Sent Events) parsing for OpenRouter/OpenAI streaming API
- Rate-limited Telegram message editing (1 edit/second)
- Automatic fallback to non-streaming mode on failure
- Graceful handling of `[DONE]` termination

**Usage:**
Streaming is enabled by default when the AI client supports it. The bot sends an initial "Thinking..." message and updates it as content streams in.

---

## Media Handling

Full support for receiving and sending media files via Telegram.

**File:** `internal/bot/media.go`

**Supported Input:**
- **Photos** - Downloaded and saved to temp directory
- **Voice messages** - OGG format, optional whisper transcription
- **Audio files** - MP3/OGG, optional whisper transcription
- **Documents** - PDF (text extraction), TXT, MD, JSON, YAML, XML, CSV

**Supported Output:**
- `SendPhoto(chat, path, caption)`
- `SendDocument(chat, path, caption)`
- `SendVoice(chat, path)`

**Dependencies:**
- `whisper` CLI (optional) - for audio transcription
- `pdftotext` (optional) - for PDF text extraction

---

## Message Tool

Send messages to other chats/users from within agent context.

**File:** `internal/tools/message.go`

**Features:**
- Send messages to allowed chats by ID or alias
- Allowlist-based security
- Numeric chat ID or alias resolution

**Tool Schema:**
```
message <to> <text>
```

**Example:**
```
message admin "Task completed successfully"
message 123456789 "Hello from the bot"
```

---

## Session Management

Persistent conversation history with full message storage.

**File:** `internal/storage/sqlite.go`

**Database Tables:**
- `sessions` - Chat session metadata
- `session_messages` - Full conversation history

**Features:**
- Save/retrieve session messages by chat ID
- Message count tracking
- Session listing with metadata
- Compaction summary storage

**API:**
```go
store.SaveSessionMessage(chatID, role, content)
store.GetSessionMessages(chatID, limit)
store.ListSessions(limit)
store.SaveSessionSummary(chatID, summary)
```

---

## Context Compaction

Automatic summarization of long conversations to stay within model context limits.

**Files:**
- `internal/agent/tokens.go` - Token counting
- `internal/agent/compactor.go` - Compaction logic

**Features:**
- Token estimation (~4 chars/token approximation)
- Model-aware context limits (GPT-4, Claude, Gemini, etc.)
- Configurable threshold (default: 80% of context limit)
- AI-powered summarization preserving key facts

**Supported Models:**
| Model | Context Limit |
|-------|---------------|
| gpt-4o | 128,000 |
| claude-3.5-sonnet | 200,000 |
| gemini-pro-1.5 | 1,000,000 |
| kimi-k2.5 | 131,072 |

**API:**
```go
compactor := agent.NewCompactor(aiClient, model)
if compactor.ShouldCompact(messages) {
    result, _ := compactor.Compact(ctx, messages)
    // result.Summary contains condensed conversation
}
```

---

## Cron Scheduler

Schedule recurring tasks with cron expressions.

**Files:**
- `internal/cron/scheduler.go` - Scheduler implementation
- `internal/tools/cron.go` - Agent tool interface
- `internal/storage/sqlite.go` - Job persistence

**Features:**
- Standard cron expression support (with seconds)
- Persistent job storage in SQLite
- Enable/disable jobs without deletion
- Next run time calculation

**Tool Commands:**
```
cron add "0 9 * * *" "Good morning reminder"
cron list
cron remove <job_id>
cron toggle <job_id> [on|off]
```

**Cron Expression Examples:**
- `0 9 * * *` - Daily at 9:00 AM
- `0 9 * * 1` - Every Monday at 9:00 AM
- `*/30 * * * *` - Every 30 minutes
- `0 18 * * 1-5` - Weekdays at 6:00 PM

**Dependencies:**
- `github.com/robfig/cron/v3`

---

## Web Fetch

Fetch and extract content from URLs.

**File:** `internal/tools/web_fetch.go`

**Features:**
- HTTP GET with configurable timeout (30s)
- Redirect following (max 5)
- HTML content extraction
- Script/style tag removal
- Clean text output

**Tool Schema:**
```
web_fetch <url>
```

**Example:**
```
web_fetch https://example.com/article
```

**Output:**
```
**Page Title**

URL: https://example.com/article

Extracted content here...
```

---

## Image Generation

Generate images using OpenAI's DALL-E API.

**File:** `internal/tools/image_gen.go`

**Features:**
- DALL-E 3 integration
- Multiple size options (1024x1024, 1792x1024, 1024x1792)
- Quality settings (standard, hd)
- Style options (vivid, natural)
- Base64 decoding and local file saving

**Tool Schema:**
```
image_gen <prompt> [--size 1024x1024] [--quality standard|hd] [--style vivid|natural]
```

**Example:**
```
image_gen "A sunset over mountains" --size 1792x1024 --quality hd
```

**Requirements:**
- OpenAI API key with DALL-E access

---

## Text-to-Speech

Convert text to speech using OpenAI's TTS API.

**File:** `internal/tools/tts.go`

**Features:**
- Multiple voice options
- Speed control (0.25x - 4.0x)
- MP3 output with optional OGG conversion
- Telegram voice message compatible

**Available Voices:**
- alloy, echo, fable, onyx, nova, shimmer

**Tool Schema:**
```
tts <text> [--voice alloy] [--speed 1.0]
```

**Example:**
```
tts "Hello, how are you today?" --voice nova --speed 1.2
```

**Dependencies:**
- OpenAI API key
- `ffmpeg` (optional) - for OGG conversion

---

## Heartbeat System

Proactive monitoring and notifications.

**File:** `internal/agent/heartbeat.go`

**Features:**
- Periodic background checks (configurable interval)
- Email monitoring (script-based or IMAP)
- Context usage warnings
- Custom checker registration
- Smart notification batching

**Built-in Checks:**
- Context usage monitoring
- Email check (Gmail script or IMAP)

**Custom Checker API:**
```go
heartbeat.RegisterChecker("mycheck", func(ctx context.Context) (CheckResult, error) {
    // Custom check logic
    return CheckResult{Status: "ok", Message: "All good"}, nil
})
```

**IMAP Configuration:**
```go
heartbeat.ConfigureIMAP(&IMAPConfig{
    Server:   "imap.gmail.com",
    Port:     993,
    Username: "user@gmail.com",
    Password: "app-password",
    UseTLS:   true,
})
```
