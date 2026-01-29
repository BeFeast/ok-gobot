# Changelog

All notable changes to ok-gobot will be documented in this file.

## [Unreleased]

### Added

#### Streaming Responses
- Added `CompleteStream()` method to AI client for SSE streaming
- Added `StreamEditor` for rate-limited Telegram message editing
- Integrated streaming into bot message handler with automatic fallback

#### Media Handling
- Added `MediaHandler` for processing incoming media
- Support for photos, voice messages, audio files, and documents
- Whisper integration for audio transcription (optional)
- PDF text extraction via pdftotext (optional)
- Methods for sending photos, documents, and voice messages

#### Message Tool
- New `message` tool for cross-chat messaging
- Allowlist-based security for allowed targets
- Support for chat ID and alias resolution

#### Session Management
- Added `session_messages` table for full conversation history
- Session listing with metadata
- Message count tracking per session
- Compaction summary storage

#### Context Compaction
- Added `TokenCounter` for estimating token usage
- Model-aware context limits for popular models
- Configurable compaction threshold (default 80%)
- AI-powered conversation summarization

#### Cron Scheduler
- Full cron job scheduler using robfig/cron/v3
- Persistent job storage in SQLite
- Enable/disable jobs without deletion
- Agent tool for managing scheduled tasks
- Support for standard 5-field cron expressions

#### Web Fetch Tool
- URL content fetching with configurable timeout
- HTML parsing and content extraction
- Script/style tag removal
- Clean text output with truncation

#### Image Generation
- DALL-E 3 integration via OpenAI API
- Support for multiple sizes, quality, and style options
- Base64 decoding and local file saving
- Revised prompt reporting

#### Text-to-Speech
- OpenAI TTS API integration
- Six voice options (alloy, echo, fable, onyx, nova, shimmer)
- Speed control (0.25x - 4.0x)
- Optional OGG conversion for Telegram compatibility

#### Heartbeat System
- IMAP email checking support
- Custom checker registration API
- Pluggable heartbeat check system

### Fixed
- Fixed `ErrorLevel` type/const name collision in `errorx/handler.go`

### Changed
- Enhanced `Compactor` with token-aware compaction logic
- Extended SQLite schema with new tables and columns

### Dependencies
- Added `github.com/robfig/cron/v3` for cron scheduling
- Added `golang.org/x/net/html` for HTML parsing

## [0.1.0] - 2024-XX-XX

### Added
- Initial release
- Telegram Bot API support
- OpenRouter/OpenAI compatible AI client
- Personality system (SOUL.md, IDENTITY.md, USER.md)
- Memory system (daily notes, MEMORY.md)
- Safety rules (stop phrases)
- Tool calling agent
- Basic tools: local, ssh, file, obsidian, browser, search
- SQLite storage
- Cobra CLI with config management
- Basic heartbeat system
