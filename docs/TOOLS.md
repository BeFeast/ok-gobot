# ok-gobot Tools Reference

## Shell & File Tools

### local
Execute local shell commands. Dangerous commands require approval via Telegram inline keyboard.

```
local <command>
```

Dangerous patterns (require approval): `rm -rf`, `kill`, `shutdown`, `reboot`, `dd`, `mkfs`, `DROP TABLE`, `DELETE FROM`, `passwd`, `chmod 777`, etc.

### ssh
Execute commands on remote hosts via SSH. Configured in `~/ok-gobot-soul/TOOLS.md`.

```
ssh <command>
```

### file
Read and write files within the allowed directory. Path traversal protection enforced.

```
file read <path>
file write <path> <content>
```

### patch
Apply unified diff patches to files. Path traversal protection enforced.

```
patch <filepath>
<unified diff content>
```

### grep
Recursive regex file search. Skips binary files, `.git`, `node_modules`. Max 50 results.

```
grep <pattern> [directory]
```

### obsidian
Access Obsidian vault notes. Auto-adds `.md` extension and `created` frontmatter on write.

```
obsidian read <path>
obsidian write <path> <content>
obsidian list [directory]
```

---

## Web Tools

### search
Web search using Brave Search or Exa API. Returns 5 results with title, URL, snippet.

```
search <query>
```

Requires `BRAVE_API_KEY` or `EXA_API_KEY` in environment or TOOLS.md.

### web_fetch
Fetch and extract content from URLs. Uses Mozilla Readability for article extraction, falls back to basic HTML parsing. SSRF protection blocks private IPs.

```
web_fetch <url>
```

Features: 12KB content limit, 30s timeout, 5 redirect limit, metadata extraction (title, author, excerpt).

### browser
Chrome automation via ChromeDP. Persistent profile in `~/.ok-gobot/chrome-profile`.

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

Requires Google Chrome installed.

---

## Media Tools

### image_gen
Generate images using DALL-E 3.

```
image_gen <prompt> [--size 1024x1024] [--quality standard|hd] [--style vivid|natural]
```

Sizes: 1024x1024 (default), 1792x1024, 1024x1792. Requires OpenAI API key.

### tts
Text-to-speech with multiple providers.

```
tts <text> [--voice <name>] [--speed <0.25-4.0>]
tts edge:<text>           # Force Edge TTS
tts openai:<text>         # Force OpenAI TTS
```

**OpenAI voices:** alloy, echo, fable, onyx, nova, shimmer
**Edge voices:** ru-RU-DmitryNeural, ru-RU-SvetlanaNeural, en-US-GuyNeural, en-US-JennyNeural, en-US-AriaNeural

Edge TTS is free (no API key). Requires `edge-tts` CLI (`pip install edge-tts`).
OGG conversion for Telegram requires `ffmpeg`.

---

## Memory & Communication Tools

### memory_search
Semantic search over indexed markdown memory chunks (`MEMORY.md` and `memory/*.md`).

```
memory_search <query> [limit]
```

### memory_get
Read markdown memory content by source and optional header path.

```
memory_get <source> [header_path]
```

Requires `memory.enabled: true` in config and an embeddings API key for `memory_search`.

### message
Send messages to other Telegram chats. Allowlist-based security.

```
message <to> <text>
```

`<to>` can be a numeric chat ID or a configured alias.

### cron
Schedule recurring tasks with cron expressions.

```
cron add <expression> <task>
cron list
cron remove <job_id>
cron toggle <job_id> [on|off]
```

Expression format: `minute hour day month weekday` (5-field). Examples:
- `0 9 * * *` — daily at 9:00
- `*/30 * * * *` — every 30 minutes
- `0 18 * * 1-5` — weekdays at 18:00

---

## Tool Configuration

Tools are loaded from `~/ok-gobot-soul/TOOLS.md`:

```markdown
## SSH

| Alias | Host | User | Notes |
|-------|------|------|-------|
| server1 | 192.168.1.100 | admin | Main server |

## API Keys

Store in environment variables or config.yaml:
- OPENAI_API_KEY — for image_gen and tts
- BRAVE_API_KEY — for search (Brave)
- EXA_API_KEY — for search (Exa)
```

## Adding Custom Tools

Implement the `Tool` interface:

```go
type Tool interface {
    Name() string
    Description() string
    Execute(ctx context.Context, args ...string) (string, error)
}

registry.Register(&MyCustomTool{})
```

For native tool calling, optionally implement `ToolSchema` to provide custom JSON Schema parameters.

---

## Verification Gate (Paranoid Protocol)

To ensure task integrity and prevent "hallucinated success," the agent enforces a Verification Gate.

### Principles:
1. **No Evidence = No Success:** If a mutation tool (`write`, `patch`, `exec`, `ssh`, `message`, `cron`, `obsidian write`) was called, the agent MUST call a verification tool before providing the final response.
2. **Mutation Tracking:** The agent tracks if any state-changing tool was invoked during the session.
3. **Verification Tools:** Tools such as `read`, `ls`, `stat`, `status`, `git status`, `grep`, and `obsidian list` are considered verification tools.
4. **Enforcement:** If the agent attempts to return a final response after a mutation without calling a verification tool, the system will inject a mandatory verification request:
   `Verification required: provide evidence (e.g. via read, ls, stat) before claiming success.`

### Tool Result Evidence
Mutating tools return a structured `Evidence` field containing:
- **Path:** Affected file path.
- **Snippet:** A short preview of the changed content.
- **Output:** Command output snippet.

Agents should use this evidence to double-check their own work before reporting to the user.
