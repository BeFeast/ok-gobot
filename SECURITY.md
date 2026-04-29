# Security Policy

## Reporting Vulnerabilities

If you discover a security vulnerability in ok-gobot, please report it responsibly by opening a private security advisory on GitHub:

**GitHub Security Advisories:** [BeFeast/ok-gobot Security](https://github.com/BeFeast/ok-gobot/security/advisories)

Do not open a public issue for security vulnerabilities.

## Security Posture

ok-gobot is a single-operator Telegram bot that executes shell commands, accesses the filesystem, and makes network requests on the operator's behalf. The security model is designed for a trusted-operator deployment, not a multi-tenant service.

### Implemented Controls

**Authentication & Authorization**
- DM authorization: open, allowlist, or pairing code modes
- Pairing code brute-force protection (lockout after 5 failed attempts)
- Auth fail-closed by default (unknown modes deny access)
- Admin-only commands for sensitive operations

**Execution Safety**
- Dangerous command approval via Telegram inline keyboard
- Emergency stop (`/estop`) to disable dangerous tool families without restart
- Capability policies per agent: shell, filesystem, network, cron, memory, spawn
- Symlink escape prevention with `filepath.EvalSymlinks`

**Network Safety**
- SSRF protection: blocks private IPs, validates redirect targets
- Browser tool blocks `file://`, localhost, `.internal`/`.local`
- CORS restriction: loopback-only origins for API and control server
- Control server disabled by default, loopback-only binding

**Data Protection**
- Log redaction for API keys, Bearer tokens, bot tokens
- OAuth credentials stored with 0600 permissions
- Config file permissions set to 0600 by `config init`
- WebSocket origin validation (CSWSH protection)
- Constant-time token comparison

**Skill Safety**
- Security audit on skill install: rejects symlinks, scripts, pipe-to-shell, path escaping
- Markdown-only enforcement for skill content

### Known Limitations

- **No budget enforcement for autonomous runs.** Scheduled roles execute tool calls without per-role cost caps. Operators should restrict tool access via capability policies until budget controls ship.
- **No WASM sandboxing.** Tool execution happens in the host process. Capability policies restrict which tools are available but do not sandbox execution.
- **Single-operator model.** ok-gobot is not designed for multi-tenant deployments. All authorized users share the same execution context.

### Security Hardening History

See [docs/SECURITY-FIXES.md](docs/SECURITY-FIXES.md) for the detailed security hardening changelog.
