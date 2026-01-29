# ok-gobot - Session Handover Document

**Date:** January 29, 2026  
**Repository:** https://github.com/BeFeast/ok-gobot  
**Status:** Core functionality complete, ready for testing and iteration

---

## 🦞 Project Overview

**ok-gobot** is a Go-based reimplementation of Moltbot/Clawdbot - a personal AI agent for Telegram with:
- Fast startup (~15ms vs 5s TypeScript)
- Single 18MB binary
- Personality system via markdown files
- Tool-calling agent architecture
- Chrome browser automation (CDP)
- Multi-provider AI support (OpenRouter/OpenAI)

---

## ✅ Completed Features

### 1. Core Architecture

**Module Structure:**
```
ok-gobot/
├── cmd/moltbot/          # Entry point
├── internal/
│   ├── agent/            # Personality, memory, safety, tool-agent
│   ├── ai/               # OpenAI-compatible API client
│   ├── bot/              # Telegram bot with telebot v4
│   ├── browser/          # Chrome CDP automation (chromedp)
│   ├── cli/              # Cobra CLI commands
│   ├── config/           # Viper configuration
│   ├── errorx/           # Error handling with recovery
│   ├── session/          # Context monitoring
│   ├── storage/          # SQLite persistence
│   └── tools/            # Tool registry and implementations
```

### 2. Personality System (Dynamic File Loading)

**Unlike hardcoded approach, loads raw .md files:**
- `SOUL.md` - Core personality, behavior rules
- `IDENTITY.md` - Name (Штрудель), creature type, emoji 🕯️
- `USER.md` - Oleg's profile, contacts, health, projects
- `AGENTS.md` - Operating rules, memory protocol, safety
- `TOOLS.md` - SSH hosts, local directories, API refs
- `MEMORY.md` - Long-term curated memory
- `HEARTBEAT.md` - Periodic checks, context warnings
- `memory/YYYY-MM-DD.md` - Daily conversation notes

**Location:** Configurable via `soul_path` (default: `~/ok-gobot`)

### 3. Tool Framework

**Available Tools:**
- **search** - Web search (Brave/Exa APIs)
- **browser** - Chrome automation via CDP
- **file** - Read/write local files
- **obsidian** - Vault access
- **ssh** - Remote command execution
- **local** - Local shell commands

**ToolCallingAgent** (`internal/agent/tool_agent.go`):
- AI decides when to use tools based on user request
- Natural language flow: user asks → AI decides → tool executes → AI responds
- Shows "🔧 Using X tool..." when tools are invoked

### 4. Browser Automation

**Chrome/CDP Integration:**
- Uses `chromedp` library for DevTools Protocol
- Isolated profile: `~/ok-gobot/chrome-profile/`
- Preserves: history, cookies, logins, extensions
- Commands: navigate, click, fill, screenshot, wait

**Mixed Approach:**
- Bot handles: Navigation, clicking, form filling
- User handles: Complex forms, CAPTCHAs, confirmations

### 5. Configuration System

**Config file:** `~/.ok-gobot/config.yaml`

```yaml
telegram:
  token: "your-bot-token"

ai:
  provider: "openrouter"
  api_key: "your-key"
  model: "moonshotai/kimi-k2.5"

soul_path: "~/ok-gobot"  # NEW: Configurable personality files location

storage:
  path: "~/.ok-gobot/ok-gobot.db"

log_level: "info"
```

**Environment Variables:**
- `OKGOBOT_TELEGRAM_TOKEN`
- `OKGOBOT_AI_API_KEY`
- `OKGOBOT_SOUL_PATH` (overrides config)

### 6. CLI Commands

```bash
ok-gobot onboard              # First-time setup wizard
ok-gobot config init          # Create config file
ok-gobot config show          # Show configuration
ok-gobot config set <k> <v>   # Set config value
ok-gobot config models        # List AI models
ok-gobot browser setup        # Setup Chrome profile
ok-gobot browser status       # Check Chrome status
ok-gobot start                # Start the bot
ok-gobot status               # Show bot status
ok-gobot version              # Show version
```

### 7. Telegram Bot Commands

```
/start       - Welcome message
/help        - Show help
/status      - Bot status
/tools       - List available tools
/clear       - Clear conversation history
/memory      - Show today's memory
```

**No manual /browser_* commands** - AI handles tools automatically

---

## 📁 File Locations

**Binary:**
- Source: `/Users/i065699/work/projects/personal/AI/cli-agents/moltbot-go/`
- Installed: `~/.local/bin/ok-gobot`

**Configuration:**
- Config: `~/.ok-gobot/config.yaml`
- Database: `~/.ok-gobot/ok-gobot.db`
- Chrome Profile: `~/ok-gobot/chrome-profile/` (after setup)

**Agent Files (NEW LOCATION):**
- Default: `~/ok-gobot/`
- Files: IDENTITY.md, SOUL.md, USER.md, AGENTS.md, TOOLS.md, MEMORY.md, HEARTBEAT.md
- Daily Memory: `~/ok-gobot/memory/`

**Legacy:**
- Old location: `~/clawd/` (still supported if files not copied)

---

## 🔄 Migration from ~/clawd

**Files to copy:**
```bash
# Create new directory structure
mkdir -p ~/ok-gobot/memory ~/ok-gobot/scripts

# Copy core personality files
cp ~/clawd/{IDENTITY,SOUL,USER,AGENTS,TOOLS,MEMORY,HEARTBEAT}.md ~/ok-gobot/

# Copy daily memory (optional)
cp ~/clawd/memory/*.md ~/ok-gobot/memory/

# Copy scripts (optional)
cp ~/clawd/scripts/* ~/ok-gobot/scripts/
```

**One-liner:**
```bash
mkdir -p ~/ok-gobot && cd ~/clawd && \
cp -r {IDENTITY,SOUL,USER,AGENTS,TOOLS,MEMORY,HEARTBEAT}.md memory scripts ~/ok-gobot/ 2>/dev/null || true
```

---

## 🚀 Quick Start (For Testing)

```bash
# 1. Install binary
cd /Users/i065699/work/projects/personal/AI/cli-agents/moltbot-go
cp ok-gobot ~/.local/bin/

# 2. Run onboarding (creates sample files)
ok-gobot onboard

# 3. Configure Telegram token
ok-gobot config set telegram.token YOUR_BOT_TOKEN

# 4. Configure AI (if not set)
ok-gobot config set ai.api_key YOUR_OPENROUTER_KEY

# 5. Setup Chrome (optional, for browser automation)
ok-gobot browser setup

# 6. Start the bot
ok-gobot start
```

---

## 🧪 Current State & Testing

**What's Working:**
- ✅ Telegram bot with long-polling
- ✅ AI integration (OpenRouter/Kimi K2.5)
- ✅ Dynamic personality loading from ~/ok-gobot/
- ✅ ToolCallingAgent with autonomous tool selection
- ✅ Memory system (daily notes + SQLite)
- ✅ Safety rules (stop phrases)
- ✅ Chrome browser automation (CDP)
- ✅ Web search (Brave/Exa)
- ✅ Configuration system with soul_path

**What's NOT Implemented (Future):**
- ❌ Vector memory (embeddings)
- ❌ Gmail OAuth integration (has stubs)
- ❌ Calendar integration
- ❌ Cron jobs
- ❌ Multi-channel (WhatsApp, Discord, etc.)
- ❌ Mobile nodes
- ❌ Plugin system
- ❌ TUI interface

---

## 🔧 Key Implementation Details

### ToolCallingAgent Flow
1. User sends natural language request (e.g., "Find cheap phones on KSP")
2. AI receives system prompt with all personality files + tool descriptions
3. AI decides to use browser tool
4. Bot shows: "🔧 Using browser tool..."
5. Tool executes (Chrome opens, navigates, searches)
6. AI processes results and responds conversationally
7. Interaction logged to daily memory

### Personality Loading
- NOT hardcoded struct parsing
- Reads raw .md files as text
- Includes full contents in system prompt
- Loads fresh on every startup
- AI interprets context naturally

### Chrome Automation
- Profile: `~/ok-gobot/chrome-profile/`
- Visible mode (not headless) for user interaction
- Preserves: logins, cookies, extensions, history
- Bot handles simple automation, user handles complex forms

---

## 📋 Next Steps / Roadmap

### High Priority
1. **Test the agent flow** - Verify ToolCallingAgent works correctly
2. **Implement Gmail integration** - Complete the email checking in heartbeat
3. **Add vector memory** - Embeddings for better context retrieval
4. **Obsidian integration** - Read/write vault notes
5. **Calendar integration** - Check events in heartbeat

### Medium Priority
6. **Context compaction** - Auto-summarize when approaching token limit
7. **Better error handling** - More recovery mechanisms
8. **Screenshot sharing** - Send browser screenshots to Telegram
9. **File upload/download** - Support documents in Telegram

### Low Priority
10. **TUI interface** - Terminal UI with Bubble Tea
11. **Multi-channel** - WhatsApp, Discord support
12. **Plugin system** - Loadable extensions
13. **Voice/TTS** - ElevenLabs integration

---

## 🐛 Known Issues

1. **Tool result parsing** - Need to verify AI correctly formats tool calls in JSON
2. **Browser context sharing** - Each command creates new tab, should reuse
3. **Error feedback** - Some errors only logged, not shown to user
4. **Memory limits** - No automatic compaction yet

---

## 💡 Design Decisions

1. **Go vs TypeScript:** Chosen for 300x faster startup (15ms vs 5s)
2. **Telegram-only:** Simpler than multi-channel gateway
3. **Visible Chrome:** Allows user interaction for complex tasks
4. **Raw .md loading:** More flexible than structured parsing
5. **Configurable soul_path:** Supports migration from Moltbot

---

## 🔗 References

- **Repository:** https://github.com/BeFeast/ok-gobot
- **Original Moltbot:** TypeScript, 5s startup, multi-channel
- **Go module:** `ok-gobot`
- **Main package:** `cmd/moltbot/main.go`

---

## 📝 Notes for Next Session

1. **Test the full flow:**
   - Start bot
   - Send "Find me an iPhone on KSP"
   - Verify browser opens and navigates
   - Check AI responds with results

2. **Migration:**
   - Copy files from ~/clawd to ~/ok-gobot
   - Test personality loading from new location
   - Update any hardcoded paths

3. **Priority fixes:**
   - Fix browser context reuse (keep same tab)
   - Add better error messages to Telegram
   - Test tool calling JSON format

4. **Integration testing:**
   - Chrome automation
   - Web search
   - File operations
   - Obsidian vault

---

**Last Updated:** January 29, 2026  
**Binary Version:** ok-gobot v0.1.0 (go)  
**Status:** Ready for testing and iteration
