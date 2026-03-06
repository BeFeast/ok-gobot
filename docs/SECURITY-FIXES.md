# Security Fixes — Code Review Response

Date: 2026-03-06
Reviewed by: honest-code-review.md
Fixed in: this commit

## Summary

Addressed all critical (P0) and important (P1) findings from the honest code review.
Changes span security hardening, config correctness, error handling, and test updates.

---

## Critical Fixes (Red)

### 1. Cross-Site WebSocket Hijacking (CSWSH)
**Files:** `internal/control/server.go`, `internal/control/tui_server.go`

- Added `Origin` header validation on WebSocket upgrade. Only loopback origins
  (`http://127.0.0.1`, `http://localhost`) are accepted; non-browser clients
  (empty Origin) are allowed.
- Replaced direct string comparison for token auth with `crypto/subtle.ConstantTimeCompare`
  to prevent timing side-channel attacks.
- Added Bearer header support to TUI server (parity with main control server).

### 2. Approval Race Condition — Global `currentChatID`
**Files:** `internal/bot/bot_approval.go`

- Replaced the package-level `var currentChatID int64` (unsynchronized global) with
  a goroutine-ID-keyed map protected by `sync.RWMutex`.
- Each processing goroutine now stores/retrieves its own chatID via `getGoroutineID()`.
- When chatID is 0 (no context), dangerous commands are now rejected outright instead
  of silently auto-approved/denied.

### 3. Auth Fail-Open Default
**Files:** `internal/bot/auth.go`

- Changed `CheckAccess` default case from `return true` (fail-open) to `return false`
  (fail-closed) with a log warning for unknown auth modes.
- Added brute-force protection for pairing codes: 5 failed attempts triggers a
  15-minute lockout per user ID.

### 4. SSRF Redirect Bypass
**Files:** `internal/tools/web_fetch.go`

- Added SSRF revalidation in the HTTP `CheckRedirect` callback. Each redirect target
  is now validated against the same private-IP/scheme rules as the initial URL.

### 5. Symlink Path Escape
**Files:** `internal/tools/tools.go`, `internal/tools/obsidian.go`

- `resolvePath()` now resolves symlinks via `filepath.EvalSymlinks` after the initial
  string-prefix check. A symlink inside the workspace that points outside is now caught.
- Obsidian tool uses a new `resolveVaultPath()` helper with the same symlink resolution.

### 6. Browser Tool — `file://` and Localhost
**Files:** `internal/tools/browser_tool.go`

- Added `validateBrowserURL()` that blocks `file://` scheme, `localhost`/`127.0.0.1`/
  `::1` destinations, and `.internal`/`.local` hostnames before navigation.

### 7. Web UI XSS
**Files:** `web/index.html`

- Added DOMPurify (loaded from CDN with SRI) to sanitize all `marked.parse()` output
  before insertion into `innerHTML`.
- Added SRI integrity attributes to all CDN-loaded scripts/stylesheets.
- Fixed hardcoded `ws://127.0.0.1:8787/ws` URL to use the computed `proto`/`host`/`port`
  so the UI works with custom ports, `wss`, and reverse proxies.

### 8. Dangerous Command Detection
**Files:** `internal/bot/approval.go`

- Extended `dangerousPatterns` with `sudo`, `su`, `doas`, `curl|sh`, `wget|bash`,
  `eval`, `exec`, path-qualified binaries (`/bin/rm`, `/sbin/mkfs`, etc.), `docker rmi`,
  `DROP DATABASE`, and `nftables`.

### 9. Control Server Default — Disabled
**Files:** `internal/config/config.go`, `internal/control/server.go`, `config.example.yaml`

- Changed `control.enabled` default from `true` to `false`. Operators must explicitly
  enable the control server in config.yaml.
- Removed the duplicate `control:` section from config.example.yaml to avoid ambiguity.

---

## Important Fixes (Yellow)

### 10. Config Key Mismatch (`storage.path` vs `storage_path`)
**Files:** `internal/cli/config.go`

- Fixed the default config generator to write `storage_path` and `log_level` (flat keys)
  instead of nested `storage.path` and `log.level`, matching what the loader expects.

### 11. Config.Save() Lossy Round-Trip
**Files:** `internal/config/config.go`

- `Save()` now persists `models`, `model_aliases`, `contacts`, `control.*`, and `agents`
  fields that were previously omitted, preventing silent data loss on config writes.

### 12. TTS Provider Validation
**Files:** `internal/config/config.go`

- Added validation that `tts.provider` must be `"openai"` or `"edge"` (or empty).
  Invalid values now fail at config validation time instead of at runtime.

### 13. API CORS Restriction
**Files:** `internal/api/middleware.go`

- Replaced `Access-Control-Allow-Origin: *` with dynamic origin matching restricted
  to `http://127.0.0.1` and `http://localhost` (with any port).

### 14. SSH StrictHostKeyChecking
**Files:** `internal/tools/tools.go`

- Changed from `StrictHostKeyChecking=no` (MITM-vulnerable) to
  `StrictHostKeyChecking=accept-new` (trust-on-first-use, reject changes).

### 15. Hub Shutdown Path
**Files:** `internal/control/hub.go`

- Added `Stop()` method and a `stop` channel to `Hub.Run()`. On stop, all connected
  clients are cleaned up and the run loop exits, preventing goroutine leaks.

### 16. Failover — Network Error Retry
**Files:** `internal/ai/failover.go`

- `isRetryableFromErr` now detects `net.Error`, `io.EOF`, `io.ErrUnexpectedEOF`,
  connection resets, and TLS failures as retryable, in addition to HTTP status codes.

---

## Test Updates

- `TestDefaultConfig` — updated to expect `Enabled = false`.
- `TestAuthManager_DefaultMode` — updated to expect fail-closed behavior.
- `TestCORSMiddleware` — updated to check for non-empty origin header.
- `TestResolvePath` — no test change needed; symlink logic handles temp dirs correctly.
- Config schema regenerated via `go run ./cmd/gen-config-schema`.

---

## Pre-Existing Failures (Not Addressed)

These test failures were present before the fixes and are noted in the review:

- `TestBuildSystemPrompt_OrdersSkillsAfterAgentsAndMentionsMemoryTools` — personality test.
- `TestExchangeAnthropicOAuthCode` / `TestRefreshAnthropicOAuthCredentials` /
  `TestAnthropicClientOAuthHeadersAndBetaQuery` — Anthropic OAuth test infrastructure.
- `TestHandleMessage_DeniesUnauthorizedDirectMessageWithoutSideEffects` and related —
  DB migration issue (`sessions_v2` schema).
- `TestLiveStreamEditor_ScheduleEdit_TokenThreshold` — timing-sensitive flaky test.
