# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest `main` | Yes |
| older releases | Best-effort |

ok-gobot follows a rolling-release model. Security fixes are applied to the latest `main` branch. If you are running an older checkout, pull the latest changes and rebuild.

## Reporting a Vulnerability

If you discover a security vulnerability, please report it responsibly:

1. **Do NOT open a public GitHub issue.**
2. Email the maintainers at **security@befeast.dev** with:
   - A description of the vulnerability.
   - Steps to reproduce.
   - Any relevant logs or screenshots (redact secrets).
3. You will receive an acknowledgment within 72 hours.
4. A fix will be developed privately and disclosed after a patch is available.

## Threat Model

ok-gobot is a Telegram-first AI agent that runs tools on the operator's machine. The following surfaces are in scope:

### Shell execution (`local` tool)
- Commands run as the OS user that started ok-gobot.
- Dangerous commands require explicit inline-keyboard approval (see `internal/bot/approval.go`).
- Emergency stop (`/estop on`) disables shell, SSH, browser, and cron tools at runtime.

### SSH (`ssh` tool)
- Connects to hosts defined in the operator's soul/TOOLS.md.
- Uses `StrictHostKeyChecking=accept-new` (trust-on-first-use).
- Subject to the same approval flow as local shell.

### Browser automation (`browser` tool)
- ChromeDP-based; `file://`, localhost, and internal hostnames are blocked before navigation.
- Runs in the operator's Chrome profile if configured.

### Network fetch (`web_fetch` tool)
- SSRF protection blocks private/loopback IPs and revalidates on redirects.
- Scheme restricted to `http` and `https`.

### Memory (`memory_search`, `memory_get` tools)
- Backed by a local SQLite database and markdown files on disk.
- Embeddings are sent to the configured embeddings API (default: OpenAI).
- MCP server, when enabled, binds to loopback by default.

### HTTP API server (`api.*` config)
- Disabled by default (`api.enabled: false`).
- When enabled, binds to `127.0.0.1` by default.
- Requires an API key (`api.api_key`) for all endpoints.
- CORS restricted to loopback origins.

### WebSocket control server (`control.*` config)
- Disabled by default (`control.enabled: false`).
- Origin validation accepts only loopback origins; non-browser clients (empty Origin) are allowed.
- Token auth uses constant-time comparison.
- `control.allow_loopback_without_token` is true by default for local development; set to `false` and configure `control.token` for hardened setups.

### DM authorization (`auth.*` config)
- Default mode is `open` -- any Telegram user can interact.
- Switch to `allowlist` or `pairing` mode for restricted access.
- Auth check is fail-closed: unknown modes deny access.
- Pairing codes have brute-force protection (5 attempts / 15-minute lockout).

### Skills
- Static safety audit before skill installation: rejects symlinks, scripts, pipe-to-shell patterns, and escaping links.
- Executable bits stripped from installed skill files.
- `.git` directories removed during install.

### Known Limitations

- **No WASM sandbox.** Tool execution (local, ssh) runs in the host process. Use capability policy and estop to limit blast radius.
- **Per-task budget caps are incomplete.** Timeout and cancellation work, but tool call count and model cost limits are not yet enforced. Set conservative `allowed_tools` for scheduled roles.
- **No audit log.** Autonomous actions (tool calls, cron runs, evolution promotions) are logged but not stored in a tamper-evident format. This is planned for Phase 5.

## Safe Defaults

ok-gobot ships with conservative defaults:

| Setting | Default | Notes |
|---------|---------|-------|
| `api.enabled` | `false` | HTTP API is off |
| `control.enabled` | `false` | WebSocket control server is off |
| `api.bind_addr` | `127.0.0.1` | Loopback only when enabled |
| `auth.mode` | `open` | Convenient but not hardened; see `doctor` warnings |
| `memory.mcp.enabled` | `false` | MCP server is off |
| `memory.mcp.host` | `127.0.0.1` | Loopback only when enabled |

Run `ok-gobot doctor` to check for risky configurations. The doctor command warns when:
- `auth.mode` is `open` while the API or control server is enabled.
- The API server is enabled without an API key.
- The control server is enabled without a token.
- The API server binds to a non-loopback address.

## Hardening Checklist

1. Set `auth.mode` to `allowlist` or `pairing` and configure `auth.admin_id`.
2. If enabling `api`, set a strong `api.api_key` and keep `api.bind_addr` as `127.0.0.1`.
3. If enabling `control`, set `control.token` and set `control.allow_loopback_without_token` to `false`.
4. Review tool access per agent via `agents[].capabilities` policies.
5. Use `/estop on` to disable dangerous tool families when not needed.

## Automated Security Checks

- **gitleaks**: scans for leaked secrets on every PR and push to `main` (`.github/workflows/secret-scan.yml`).

### Recommended additions

Operators should add the following CI jobs to their fork or deployment pipeline:

- **govulncheck**: `go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...` -- checks Go dependencies for known vulnerabilities.
- **CodeQL**: GitHub's built-in static analysis for Go -- enable via repository Settings > Code security > Code scanning.

## Prior Security Work

See [docs/SECURITY-FIXES.md](docs/SECURITY-FIXES.md) for the detailed changelog of security hardening applied to the codebase.
