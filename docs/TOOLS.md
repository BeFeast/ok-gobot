# ok-gobot Tools Reference

Complete reference for all available agent tools.

## Core Tools

### local
Execute local shell commands.

```
local <command>
```

**Example:**
```
local ls -la
local echo "Hello World"
```

### ssh
Execute commands on remote hosts via SSH.

```
ssh <command>
```

Configured via `TOOLS.md` in the clawd directory.

### file
Read and write files within the allowed directory.

```
file read <path>
file write <path> <content>
```

**Example:**
```
file read notes.txt
file write todo.txt "Buy milk"
```

### obsidian
Access Obsidian vault notes.

```
obsidian read <path>
obsidian write <path> <content>
obsidian list <directory>
```

**Example:**
```
obsidian read "Daily Notes/2024-01-15"
obsidian list "Projects"
```

---

## Communication Tools

### message
Send messages to other Telegram chats.

```
message <to> <text>
```

**Parameters:**
- `to` - Chat ID (numeric) or configured alias
- `text` - Message content

**Example:**
```
message admin "Backup completed"
message 123456789 "Hello!"
```

**Security:** Requires allowlist configuration.

---

## Search Tools

### search
Search the web using Brave or Exa API.

```
search <query>
```

**Example:**
```
search "best restaurants in Berlin"
```

### web_fetch
Fetch and extract content from a URL.

```
web_fetch <url>
```

**Example:**
```
web_fetch https://news.ycombinator.com
```

**Output:** Title, URL, and extracted text content.

---

## Browser Tools

### browser
Control Chrome browser for web automation.

```
browser start
browser stop
browser navigate <url>
browser click <selector>
browser fill <selector> <value>
browser screenshot
browser wait <selector>
browser text <selector>
```

**Example:**
```
browser start
browser navigate https://google.com
browser fill "input[name=q]" "ok-gobot"
browser click "input[type=submit]"
browser screenshot
```

**Requirements:** Google Chrome installed.

---

## Scheduling Tools

### cron
Manage scheduled tasks.

```
cron add <expression> <task>
cron list
cron remove <job_id>
cron toggle <job_id> [on|off]
cron help
```

**Cron Expression Format:**
```
minute hour day-of-month month day-of-week
```

**Examples:**
```
cron add "0 9 * * *" "Send daily summary"
cron add "*/30 * * * *" "Check emails"
cron add "0 18 * * 1-5" "End of workday reminder"
cron list
cron toggle 1 off
cron remove 2
```

---

## Media Tools

### image_gen
Generate images using DALL-E.

```
image_gen <prompt> [options]
```

**Options:**
- `--size` - 1024x1024 (default), 1792x1024, 1024x1792
- `--quality` - standard (default), hd
- `--style` - vivid, natural

**Example:**
```
image_gen "A futuristic city at night" --size 1792x1024 --quality hd --style vivid
```

**Output:** Path to generated image file.

### tts
Convert text to speech.

```
tts <text> [options]
```

**Options:**
- `--voice` - alloy (default), echo, fable, onyx, nova, shimmer
- `--speed` - 0.25 to 4.0 (default: 1.0)

**Example:**
```
tts "Welcome to ok-gobot" --voice nova --speed 1.1
```

**Output:** Path to audio file (OGG or MP3).

---

## Tool Configuration

Tools are loaded from `~/clawd/TOOLS.md` with the following format:

```markdown
## SSH

| Alias | Host | User | Notes |
|-------|------|------|-------|
| server1 | 192.168.1.100 | admin | Main server |
| server2 | example.com | deploy | Production |

## API Keys

Store API keys in environment variables or config.yaml:
- `OPENAI_API_KEY` - For image_gen and tts
- `BRAVE_API_KEY` - For search (Brave)
- `EXA_API_KEY` - For search (Exa)
```

---

## Adding Custom Tools

Implement the `Tool` interface:

```go
type Tool interface {
    Name() string
    Description() string
    Execute(ctx context.Context, args ...string) (string, error)
}
```

Register in the tool registry:

```go
registry.Register(&MyCustomTool{})
```
