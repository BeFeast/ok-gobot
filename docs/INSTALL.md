# Installation & Configuration Guide

## Prerequisites

- **Go 1.24+** (with CGO enabled for SQLite)
- **Telegram bot token** from [@BotFather](https://t.me/BotFather)
- **AI provider access** — one or more of:
  - **Anthropic** (OAuth via Claude MAX subscription, or API key)
  - **ChatGPT** (OAuth via ChatGPT Pro/Plus — uses `chatgpt.com/backend-api` codex endpoint)
  - **Google Gemini** (API key from Google AI Studio)
  - **OpenAI** (API key from platform.openai.com)
  - Any **OpenAI-compatible** API with a custom base URL

### Platform-Specific Dependencies

| Platform | CGO Dependency | Notes |
|----------|---------------|-------|
| macOS | Xcode CLT (`xcode-select --install`) | Apple Silicon and Intel supported |
| Linux | `gcc`, `libc-dev` | `apt install build-essential` (Debian/Ubuntu) or `dnf groupinstall "Development Tools"` (Fedora) |
| Windows | MinGW-w64 or TDM-GCC | Required for `go-sqlite3`. Set `CC=gcc` in env |

Optional:
- **Google Chrome / Chromium** — for browser automation tool
- **ffmpeg** — for TTS audio conversion

---

## Installation

### From Source

```bash
git clone https://github.com/your-org/ok-gobot.git
cd ok-gobot

make build
# Binary: bin/ok-gobot

# Stripped binary (smaller):
make build-small
```

### Cross-Compilation

```bash
make build-linux     # Linux AMD64
make build-darwin    # macOS Intel + Apple Silicon
make build-all       # All platforms
```

### Install to PATH

```bash
make install   # copies to $GOPATH/bin/
# Or manually:
cp bin/ok-gobot /usr/local/bin/
```

---

## Directory Layout

ok-gobot uses a separated layout — source code, config, and workspace live in
different directories. The recommended structure:

```
ok-gobot/                         # source code (git repo)
ok-gobot-assets/
  config/                         # config.yaml + oauth credentials
  workspace/                      # personality, tools, memory
    SOUL.md
    IDENTITY.md
    USER.md
    AGENTS.md
    TOOLS.md
    MEMORY.md
    HEARTBEAT.md
    memory/                       # daily memory notes
    skills/                       # optional skill plugins
```

### Initial Setup

```bash
# 1. Create the assets structure
mkdir -p ok-gobot-assets/config
mkdir -p ok-gobot-assets/workspace/memory
mkdir -p ok-gobot-assets/workspace/skills

# 2. Scaffold personality files into the workspace
cd ok-gobot
ok-gobot onboard --path ../ok-gobot-assets/workspace

# 3. Move/symlink the generated config to the assets directory
mv ~/.ok-gobot/config.yaml ../ok-gobot-assets/config/config.yaml
ln -sf "$(pwd)/../ok-gobot-assets/config/config.yaml" ~/.ok-gobot/config.yaml
```

After this, edit `ok-gobot-assets/config/config.yaml` and set `soul_path` to
point at your workspace:

```yaml
soul_path: "/absolute/path/to/ok-gobot-assets/workspace"
```

Or use the environment variable:

```bash
export OKGOBOT_SOUL_PATH="/absolute/path/to/ok-gobot-assets/workspace"
```

---

## AI Provider Configuration

### Anthropic (OAuth — Claude MAX)

The recommended approach for Claude MAX subscribers. No API key needed — uses
browser-based OAuth with PKCE and auto-refreshing tokens.

```bash
ok-gobot auth anthropic login
```

This will:
1. Open a browser to `claude.ai/oauth/authorize`
2. Ask you to paste the callback code
3. Save OAuth credentials to `~/.ok-gobot/oauth/anthropic.json` (0600)
4. Set `ai.provider: anthropic` and `ai.api_key: oauth:<token>` in config

```bash
# Check status / expiry
ok-gobot auth anthropic status

# Remove credentials
ok-gobot auth anthropic logout
```

Config (auto-set by `auth anthropic login`):
```yaml
ai:
  provider: "anthropic"
  api_key: "oauth:<auto-populated>"
  model: "claude-sonnet-4-5-20250929"
```

### ChatGPT (OAuth — ChatGPT Pro/Plus)

Uses the `chatgpt.com/backend-api/codex/responses` endpoint with an OAuth JWT
token from your ChatGPT session.

```yaml
ai:
  provider: "chatgpt"           # or "openai-codex"
  api_key: "<your-chatgpt-oauth-jwt>"
  base_url: "https://chatgpt.com/backend-api"
  model: "gpt-5.4"
```

The auth mode is `oauth` — the token is your ChatGPT session JWT. To obtain it,
inspect a `chatgpt.com` request in browser DevTools and copy the
`Authorization: Bearer <token>` value.

### Google Gemini (API Key)

Uses Google's OpenAI-compatible endpoint with a Gemini API key from
[Google AI Studio](https://aistudio.google.com/apikey).

```yaml
ai:
  provider: "custom"
  api_key: "<your-gemini-api-key>"
  base_url: "https://generativelanguage.googleapis.com/v1beta/openai"
  model: "gemini-2.5-pro"
```

Or via environment:
```bash
export OKGOBOT_AI_PROVIDER=custom
export OKGOBOT_AI_API_KEY="<your-gemini-api-key>"
export OKGOBOT_AI_BASE_URL="https://generativelanguage.googleapis.com/v1beta/openai"
export OKGOBOT_AI_MODEL="gemini-2.5-pro"
```

### OpenAI (API Key)

```yaml
ai:
  provider: "openai"
  api_key: "<your-openai-api-key>"
  model: "gpt-4o"
```

### Droid Provider (CLI Agent Transport)

The `droid` provider uses an external CLI agent as the AI backend. ok-gobot spawns
the agent binary as a subprocess per request, passing the conversation as a prompt.
The agent handles model selection, tool execution, and MCP tools internally.

This enables using any installed CLI agent as a transport layer:

| Agent | Binary | Install |
|-------|--------|---------|
| Factory Droid | `droid` | [factory.ai](https://factory.ai) |
| Claude Code | `claude` | `npm install -g @anthropic-ai/claude-code` |
| OpenAI Codex CLI | `codex` | `npm install -g @openai/codex` |
| Gemini CLI | `gemini` | `npm install -g @anthropic-ai/gemini-cli` |
| Cline | `cline` | VS Code extension / CLI |
| OpenCode | `opencode` | `go install github.com/opencode-ai/opencode@latest` |

**Prerequisite:** The chosen CLI agent must be installed and authenticated
(OAuth, API key, etc.) independently before ok-gobot can use it as a transport.

```yaml
ai:
  provider: "droid"
  model: "claude-sonnet-4-5-20250929"  # model hint passed to the agent
  droid:
    binary_path: "droid"               # or: claude, codex, gemini, opencode
    auto_level: "low"                  # autonomy: low, medium, high
    work_dir: ""                       # working directory for agent execution
```

The agent binary receives the full message history formatted as a single prompt
and streams JSON events back. Tool definitions are not forwarded -- the agent
discovers tools from its own configuration (e.g., MCP servers, built-in tools).

### Failover Models

Configure automatic fallback when the primary model returns 429/5xx or network
errors:

```yaml
ai:
  provider: "anthropic"
  model: "claude-sonnet-4-5-20250929"
  fallback_models:
    - "claude-haiku-3-5-20241022"
```

Note: fallback models share the same provider and API key as the primary.

---

## Core Configuration

### Telegram

```yaml
telegram:
  token: "YOUR_BOT_TOKEN"       # from @BotFather
```

### Authentication

```yaml
auth:
  mode: "pairing"                # recommended for production
  admin_id: 123456789            # your Telegram user ID
```

| Mode | Description |
|------|-------------|
| `pairing` | Users pair via 6-digit code (recommended). Brute-force protected with lockout. |
| `allowlist` | Only explicitly listed user IDs can interact. |
| `open` | Anyone can use the bot (development only). |

Unknown/misconfigured modes are denied (fail-closed).

### Control Server (TUI/Web UI)

Disabled by default for security. Enable when you need TUI or web UI access:

```yaml
control:
  enabled: true
  port: 8787
  token: "a-strong-random-token"
  allow_loopback_without_token: true
```

- Binds to `127.0.0.1` only
- Origin header validated (CSWSH protection)
- Constant-time token comparison

### TTS (Text-to-Speech)

```yaml
tts:
  provider: "edge"               # "openai" or "edge"
  default_voice: "en-US-GuyNeural"
```

### Memory (Semantic Search)

```yaml
memory:
  enabled: true
  embeddings_api_key: ""         # reuses ai.api_key if empty
  embeddings_model: "text-embedding-3-small"
```

### Multi-Agent

```yaml
agents:
  - name: "default"
    soul_path: "/path/to/ok-gobot-assets/workspace"
    allowed_tools: []             # empty = all tools

  - name: "coder"
    soul_path: "/path/to/ok-gobot-assets/workspace-coder"
    model: "claude-sonnet-4-5-20250929"
    allowed_tools: ["local", "file", "patch", "grep"]
```

---

## Running

### Validate & Start

```bash
ok-gobot doctor    # check config, dependencies, connectivity
ok-gobot start     # run in foreground
```

### As a systemd Service (Linux)

Create `/etc/systemd/system/ok-gobot.service`:

```ini
[Unit]
Description=ok-gobot Telegram AI Bot
After=network.target

[Service]
Type=simple
User=okgobot
ExecStart=/usr/local/bin/ok-gobot start
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now ok-gobot
```

### As a launchd Service (macOS)

Create `~/Library/LaunchAgents/com.okgobot.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.okgobot</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/ok-gobot</string>
    <string>start</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/tmp/ok-gobot.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/ok-gobot.err</string>
</dict>
</plist>
```

```bash
launchctl load ~/Library/LaunchAgents/com.okgobot.plist
```

### On Windows

```powershell
# Run directly
.\ok-gobot.exe start

# Or as a service via NSSM (https://nssm.cc):
nssm install ok-gobot "C:\path\to\ok-gobot.exe" start
nssm start ok-gobot
```

Windows requires MinGW-w64 for the CGO SQLite dependency during build.

---

## Environment Variables

All config keys can be overridden via `OKGOBOT_` prefix:

```bash
export OKGOBOT_TELEGRAM_TOKEN="your-token"
export OKGOBOT_AI_PROVIDER="anthropic"
export OKGOBOT_AI_API_KEY="oauth:..."
export OKGOBOT_AI_MODEL="claude-sonnet-4-5-20250929"
export OKGOBOT_SOUL_PATH="/path/to/ok-gobot-assets/workspace"
export OKGOBOT_AUTH_MODE="pairing"
```

---

## File Locations

| Path | Purpose |
|------|---------|
| `~/.ok-gobot/config.yaml` | Main configuration (symlink to assets) |
| `~/.ok-gobot/ok-gobot.db` | SQLite database (sessions, auth, messages) |
| `~/.ok-gobot/oauth/anthropic.json` | Anthropic OAuth credentials (0600) |
| `~/.ok-gobot/screenshots/` | Browser tool screenshots |
| `~/.ok-gobot/chrome-profile/` | Chrome automation profile |
| `ok-gobot-assets/workspace/` | Personality files, tools, memory |

---

## Security Recommendations

1. Use `auth.mode: "pairing"` or `"allowlist"` in production -- never `"open"`.
2. Keep `control.enabled: false` unless actively using TUI/web UI.
3. If enabling control server, set a strong `control.token`.
4. Config file permissions should be `0600` (set automatically by `config init`).
5. OAuth credentials stored in `~/.ok-gobot/oauth/` are automatically set to `0600`.
6. See `docs/SECURITY-FIXES.md` for full details on security hardening.

---

## Model Aliases

Built-in shortcuts for the `/model` command and TUI picker:

| Alias | Model |
|-------|-------|
| `sonnet` | `claude-sonnet-4-5-20250929` |
| `opus` | `claude-opus-4-5-20251101` |
| `haiku` | `claude-haiku-3-5-20241022` |
| `gpt4` | `openai/gpt-4o` |
| `gpt4m` | `openai/gpt-4o-mini` |
| `gemini` | `google/gemini-2.5-pro` |
| `flash` | `google/gemini-2.5-flash` |
| `deepseek` | `deepseek/deepseek-chat-v3-0324` |

Custom aliases in config:
```yaml
model_aliases:
  mymodel: "provider/model-name"
```
