# ok-gobot Security Posture

Last updated: April 29, 2026.

This document describes the security model, hardening measures, and known limitations of ok-gobot. For the changelog of specific security fixes, see [SECURITY-FIXES.md](SECURITY-FIXES.md).

## Threat Model

ok-gobot is a single-operator Telegram bot that executes AI-directed tool calls (shell commands, file operations, web requests, browser automation) on the host machine. The primary threats are:

1. **Unauthorized access** -- untrusted Telegram users issuing commands.
2. **AI-directed misuse** -- the AI model requesting dangerous tool calls.
3. **Injection via external content** -- web pages, documents, or user input containing prompt injection or command injection payloads.
4. **Network-level attacks** -- SSRF, CSWSH, or redirect-based bypasses.
5. **Credential leakage** -- API keys or tokens appearing in logs, configs, or responses.

## Defense Layers

### 1. Access Control

| Layer | Mechanism |
|-------|-----------|
| DM authorization | Three modes: `open`, `allowlist`, `pairing`. Default can be set to any; unknown modes are **fail-closed**. |
| Pairing brute-force protection | 5 failed attempts triggers 15-minute lockout per user ID. |
| Admin-only commands | `/estop`, `/auth`, `/reload`, `/restart` require `auth.admin_id`. |
| Group activation | Groups default to `standby` (respond only to mentions). |

### 2. Tool Execution Safety

| Layer | Mechanism |
|-------|-----------|
| Exec approval | Dangerous shell commands require Telegram inline keyboard confirmation. Auto-deny after 60s. |
| Dangerous command detection | Patterns: `rm -rf`, `kill`, `shutdown`, `sudo`, `su`, `doas`, `curl\|sh`, `wget\|bash`, `eval`, `exec`, `DROP TABLE`, `DROP DATABASE`, Docker destructive ops, and path-qualified binaries (`/bin/rm`, `/sbin/mkfs`). |
| Emergency stop (estop) | `/estop on` disables tool families: `local`, `ssh`, `browser`, `cron`, `message`. Immediate effect, no restart needed. |
| Capability policy | Per-agent structured policy: `shell`, `network`, `network_allowlist`, `cron`, `memory_write`, `spawn`, `filesystem_roots`, `file_write_scope`. Denied tool calls return a reason and remediation hint. |

### 3. Path and Filesystem Safety

| Layer | Mechanism |
|-------|-----------|
| Path traversal prevention | All file/patch/grep operations resolve paths against allowed roots. |
| Symlink escape prevention | `filepath.EvalSymlinks` after prefix check catches symlinks pointing outside workspace. |
| Obsidian vault isolation | Separate `resolveVaultPath()` with the same symlink resolution. |
| Filesystem roots | Capability policy can restrict agents to specific directory trees. |
| Read-only mode | `file_write_scope: "read_only"` blocks all write operations. |

### 4. Network Safety

| Layer | Mechanism |
|-------|-----------|
| SSRF protection (web_fetch) | Blocks localhost, private IPs (10.x, 172.16-31.x, 192.168.x, fc00::/7). DNS resolved before request to prevent rebinding. |
| Redirect revalidation | Each HTTP redirect target is revalidated against the same private-IP/scheme rules. |
| Browser URL validation | Blocks `file://` scheme, localhost/127.0.0.1/::1, `.internal`/`.local` hostnames. |
| CORS restriction | API and control server accept only loopback origins (`http://127.0.0.1`, `http://localhost`). |
| CSWSH protection | WebSocket upgrade validates `Origin` header. Non-browser (empty Origin) allowed; non-loopback browser origins rejected. |
| SSH host key policy | `StrictHostKeyChecking=accept-new` (trust-on-first-use, reject changes). |

### 5. Credential Safety

| Layer | Mechanism |
|-------|-----------|
| Log redaction | Masks API keys (sk-...), Bearer tokens, bot tokens, and long hex/base64 strings in log output. |
| Config file permissions | `config init` sets 0600 on config.yaml. |
| OAuth credential storage | Anthropic/ChatGPT OAuth credentials stored in `~/.ok-gobot/oauth/` with 0600 permissions. |
| Control server default | `control.enabled` defaults to `false`. Must be explicitly enabled. |
| Token auth | Control server token comparison uses `crypto/subtle.ConstantTimeCompare` (timing-safe). |

### 6. Input Sanitization

| Layer | Mechanism |
|-------|-----------|
| Shell argument escaping | `SanitizeShellArg` escapes shell metacharacters. |
| Telegram markdown escaping | `SanitizeTelegramMarkdown` escapes MarkdownV2 special characters. |
| Control character stripping | `StripControlChars` removes non-printable characters. |
| Web UI XSS prevention | DOMPurify sanitizes all `marked.parse()` output before innerHTML insertion. CDN scripts loaded with SRI integrity attributes. |

## Known Limitations

1. **No token/cost budgets for scheduled roles.** Roles running on cron can consume unbounded API tokens. Budget enforcement is planned for Mission Control v1. Until then, scheduled autonomy should not be considered production-safe for unattended operation.

2. **No WASM sandboxing.** Tool execution happens in-process on the host. The capability policy restricts which tools are available, but does not sandbox their execution environment.

3. **No taint tracking.** Content fetched from the web is not tracked through the system. Prompt injection from web content could influence tool calls within the same session.

4. **Single-machine deployment.** Security controls assume a single operator on a single machine. Multi-tenant or multi-machine deployments are not a design target.

## Hardening Recommendations

1. Use `auth.mode: "pairing"` or `"allowlist"` in production. Never `"open"`.
2. Keep `control.enabled: false` unless actively using TUI/web UI.
3. If enabling control server, set a strong `control.token`.
4. Use capability policy to restrict agents that run less-trusted models.
5. Set `filesystem_roots` for agents that only need access to specific directories.
6. Monitor `/estop status` and `ok-gobot evolution metrics` for anomalies.
7. Review `docs/SECURITY-FIXES.md` for the full history of security hardening.
