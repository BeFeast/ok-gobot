# API Reference

ok-gobot exposes two network interfaces:

1. **HTTP REST API** (port 8080) -- external integrations, webhooks, messaging
2. **WebSocket Control Protocol** (port 8787) -- real-time session control, streaming, approvals (TUI/web UI)

---

# HTTP REST API

Simple HTTP API for external integrations.

## Configuration

Enable the API in your `config.yaml`:

```yaml
api:
  enabled: true
  port: 8080
  api_key: "<your-api-key>"
  webhook_chat: 123456789  # Optional: default chat for webhooks
```

**Security Note**: The `api_key` is required when API is enabled. Keep it secret.

## Authentication

All endpoints except `/api/health` require authentication. Provide the API key using either:

- **Header**: `X-API-Key: <your-api-key>`
- **Bearer Token**: `Authorization: Bearer <your-api-key>`

## Endpoints

### GET /api/health

Health check endpoint. No authentication required.

**Response:**
```json
{
  "status": "ok",
  "uptime": "2h15m30s"
}
```

### GET /api/status

Get bot status information.

**Requires authentication**

**Response:**
```json
{
  "name": "Molt",
  "emoji": "🦞",
  "status": "running",
  "ai": {
    "provider": "openrouter",
    "model": "moonshotai/kimi-k2.5"
  },
  "sessions": 0
}
```

### POST /api/send

Send a message to a specific chat.

**Requires authentication**

**Request:**
```json
{
  "chat_id": 123456789,
  "text": "Hello from API!"
}
```

**Response:**
```json
{
  "success": true,
  "message": "Message sent successfully"
}
```

**Errors:**
- `400 Bad Request`: Missing or invalid parameters
- `500 Internal Server Error`: Failed to send message

### POST /api/webhook

Process a webhook event and send it to the configured webhook chat.

**Requires authentication**

**Request:**
```json
{
  "event": "deployment_completed",
  "data": {
    "environment": "production",
    "version": "v1.2.3",
    "status": "success"
  }
}
```

**Response:**
```json
{
  "success": true,
  "message": "Webhook processed successfully"
}
```

**Note**: Requires `webhook_chat` to be configured in config.yaml

**Errors:**
- `400 Bad Request`: Missing event field
- `500 Internal Server Error`: Webhook chat not configured or failed to send

## Usage Examples

### cURL

```bash
# Health check (no auth)
curl http://localhost:8080/api/health

# Get status
curl -H "X-API-Key: <your-api-key>" \
  http://localhost:8080/api/status

# Send message
curl -X POST \
  -H "X-API-Key: <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"chat_id": 123456789, "text": "Hello!"}' \
  http://localhost:8080/api/send

# Send webhook
curl -X POST \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"event": "test", "data": {"key": "value"}}' \
  http://localhost:8080/api/webhook
```

### Python

```python
import requests

API_URL = "http://localhost:8080"
API_KEY = "<your-api-key>"

headers = {
    "X-API-Key": API_KEY,
    "Content-Type": "application/json"
}

# Get status
response = requests.get(f"{API_URL}/api/status", headers=headers)
print(response.json())

# Send message
payload = {
    "chat_id": 123456789,
    "text": "Hello from Python!"
}
response = requests.post(f"{API_URL}/api/send", json=payload, headers=headers)
print(response.json())
```

### JavaScript/Node.js

```javascript
const API_URL = "http://localhost:8080";
const API_KEY = "<your-api-key>";

// Get status
fetch(`${API_URL}/api/status`, {
  headers: {
    "X-API-Key": API_KEY
  }
})
  .then(res => res.json())
  .then(data => console.log(data));

// Send message
fetch(`${API_URL}/api/send`, {
  method: "POST",
  headers: {
    "X-API-Key": API_KEY,
    "Content-Type": "application/json"
  },
  body: JSON.stringify({
    chat_id: 123456789,
    text: "Hello from JavaScript!"
  })
})
  .then(res => res.json())
  .then(data => console.log(data));
```

## Use Cases

### CI/CD Integration

Send deployment notifications:

```bash
#!/bin/bash
# deploy-notify.sh

API_URL="http://your-bot-server:8080"
API_KEY="<your-api-key>"

curl -X POST \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{
    \"event\": \"deployment\",
    \"data\": {
      \"environment\": \"$ENV\",
      \"version\": \"$VERSION\",
      \"status\": \"success\"
    }
  }" \
  "$API_URL/api/webhook"
```

### Monitoring Alerts

Integrate with monitoring systems:

```python
# Send alert to Telegram via bot API
def send_alert(alert_message):
    import requests
    
    response = requests.post(
        "http://localhost:8080/api/send",
        headers={"X-API-Key": "<your-api-key>"},
        json={
            "chat_id": 123456789,
            "text": f"🚨 Alert: {alert_message}"
        }
    )
    return response.json()
```

### GET /api/jobs

List durable background jobs.

**Requires authentication**

**Query parameters:**
- `status` (optional): filter by status (pending, running, succeeded, failed, cancelled, timed_out)
- `limit` (optional): max results (default 50, max 200)

### GET /api/jobs/{id}

Get job detail with events and display-safe artifacts.

**Requires authentication**

Artifact objects include stable presentation fields for Mission Control and API
clients:

```json
{
  "id": 12,
  "job_id": "job_abc",
  "type": "screenshot",
  "label": "Home page proof",
  "mime_type": "image/png",
  "path": "/home/user/.ok-gobot/screenshots/proof.png",
  "created_at": "2026-04-30T10:00:00Z",
  "display": {
    "kind": "image",
    "safe": true,
    "preview": true,
    "href": "/api/artifacts/12/content"
  }
}
```

Supported `display.kind` values include `image`, `url`, `text_report`, and
`artifact`. Local file paths are only exposed when they are inside configured
artifact roots. Unsafe local paths are redacted and returned with
`display.safe=false` and a reason.

### GET /api/artifacts/{id}/content

Serve a local artifact file for read-only previews.

**Requires authentication**

Only files under configured artifact roots are served. The built-in roots are
`~/.ok-gobot/screenshots` and `~/.ok-gobot/artifacts`; operators can add roots
with `artifacts.roots` in config or `OK_GOBOT_ARTIFACT_ROOTS`.

### POST /api/jobs/{id}/cancel

Request cancellation of a running or pending job.

**Requires authentication**

### GET /api/workers

List active session workers.

**Requires authentication**

### GET /api/mission/roles

List registered agent roles/profiles.

**Requires authentication**

### GET /api/mission/schedules

List cron schedules with next-run times.

**Requires authentication**

### GET /api/mission/runs

List recent job runs with role, model/tier, and tool call counts.

**Requires authentication**

**Query parameters:**
- `status` (optional): filter by status
- `limit` (optional): max results (default 50, max 200)

### GET /api/mission/stats

Daily aggregate statistics (tokens, messages, job counts).

**Requires authentication**

**Query parameters:**
- `days` (optional): number of days to include (default 7, max 90)

### GET /api/mission/estop

Get the current emergency stop state.

**Requires authentication**

**Response:**
```json
{
  "estop_enabled": false
}
```

### GET /api/mission/providers

Get the active AI provider and model.

**Requires authentication**

**Response:**
```json
{
  "status": "ok",
  "provider": "openrouter",
  "model": "moonshotai/kimi-k2.5"
}
```

### GET /api/mission/supervisor

Get the latest supervisor stuck-state decision and last safe recovery action.

**Requires authentication**

The response includes `current_decision` with the state, target, blocker reason,
safe actions, and any approval-only action that still requires an operator.
`last_safe_action` records the latest idempotent safe action applied by the
supervisor.

## Event and Artifact Retention

Job records, events, and artifacts are stored in SQLite and retained indefinitely by default.

- **Jobs**: Persisted in the `jobs` table with full lifecycle metadata including role name, model/tier, tool call count, and duration.
- **Job events**: Stored in the `job_events` table (up to 200 per job in API responses).
- **Job artifacts**: Stored in the `job_artifacts` table with content, URIs, and metadata. API responses redact unsafe local paths outside artifact roots.
- **Sessions**: Stored in `sessions_v2` with token accounting and message history.

There is currently no automatic TTL or pruning. Operators who need to manage storage size can:
1. Use SQLite directly: `DELETE FROM jobs WHERE created_at < date('now', '-90 days')`
2. Delete associated events: `DELETE FROM job_events WHERE job_id NOT IN (SELECT job_id FROM jobs)`
3. Delete associated artifacts: `DELETE FROM job_artifacts WHERE job_id NOT IN (SELECT job_id FROM jobs)`

Export job data before cleanup using: `ok-gobot jobs export <job-id> --format json`

## Error Responses

All error responses follow this format:

```json
{
  "error": "Error description"
}
```

Common HTTP status codes:
- `200 OK`: Request successful
- `400 Bad Request`: Invalid request parameters
- `401 Unauthorized`: Invalid or missing API key
- `405 Method Not Allowed`: Wrong HTTP method
- `500 Internal Server Error`: Server-side error

---

# WebSocket Control Protocol

Real-time bidirectional protocol for session control, token streaming, tool events,
and command approvals. Used by the TUI and web UI.

## Configuration

```yaml
control:
  enabled: true
  port: 8787
  token: ""
  allow_loopback_without_token: true
```

Disabled by default. Binds to `127.0.0.1` only.

## Connection

```
ws://127.0.0.1:8787/ws
```

Authentication (when `token` is set):
- Header: `Authorization: Bearer <token>`
- Query: `?token=<token>`

Loopback connections skip token check when `allow_loopback_without_token: true`.

Origin header is validated to prevent cross-site WebSocket hijacking.

## Message Format

All messages are JSON text frames using a single session-oriented protocol for
streaming, approvals, and sub-agent notifications.

Server -> Client:
```json
{"type": "event", "kind": "token", "session_id": "...", "content": "Hello"}
```

Client -> Server:
```json
{"type": "send", "session_id": "...", "text": "Hello"}
```

## Client Commands

| Type | Fields | Description |
|------|--------|-------------|
| `send` | `session_id`, `text`, `image_data` | Send message (with optional image) |
| `abort` | `session_id` | Cancel active run |
| `approve` | `approval_id`, `approved` | Respond to approval |
| `set_model` | `session_id`, `model` | Change model |
| `list_sessions` | -- | Request session list |
| `new_session` | `name` | Create new session |
| `switch_session` | `session_id` | Switch active session |
| `spawn_subagent` | `task`, `model`, `thinking`, `tool_allowlist`, `workspace_root`, `max_tool_calls`, `max_duration`, `output_format`, `output_schema`, `memory_policy` | Spawn sub-agent with an explicit delegated-run contract (legacy/frozen compatibility surface) |
| `bot_command` | `session_id`, `text` | Execute slash command |
| `list_jobs` | `limit` | List background jobs |
| `get_job` | `job_id` | Get job detail |
| `list_job_events` | `job_id`, `limit` | List job events |
| `list_job_artifacts` | `job_id`, `limit` | List job artifacts |
| `cancel_job` | `job_id` | Request job cancellation |
| `list_workers` | -- | List active workers |

## Server Messages

| Type | Kind | Key fields | Description |
|------|------|-----------|-------------|
| `connected` | -- | -- | Connection established |
| `sessions` | -- | `sessions[]` | Session list |
| `event` | `token` | `content` | Streaming text token |
| `event` | `message` | `content`, `role`, `model`, `*_tokens` | Complete message |
| `event` | `tool_start` | `tool_name`, `tool_args` | Tool execution began |
| `event` | `tool_end` | `tool_name`, `tool_result`, `tool_error` | Tool execution ended |
| `event` | `run_start` | `session_id` | Run began |
| `event` | `run_end` | `session_id` | Run completed |
| `event` | `approval_request` | `approval_id`, `command` | Needs approval |
| `event` | `queue_update` | `queue_depth` | Queue depth changed |
| `event` | `child_done` | `child_session_key`, `content` | Sub-agent completed |
| `event` | `child_failed` | `child_session_key`, `message` | Sub-agent failed |
| `error` | -- | `message` | Error message |

`spawn_subagent` delegated-run defaults:
- `max_tool_calls`: `50`
- `max_duration`: `10m`
- `output_format`: `markdown`
- `memory_policy`: `read_only`

## Example: Streaming Session

```javascript
const ws = new WebSocket("ws://127.0.0.1:8787/ws");

ws.onopen = () => {
  // List sessions
  ws.send(JSON.stringify({type: "list_sessions"}));
};

ws.onmessage = (e) => {
  const msg = JSON.parse(e.data);

  switch (msg.type) {
    case "sessions":
      console.log("Sessions:", msg.sessions);
      break;
    case "event":
      switch (msg.kind) {
        case "token":
          process.stdout.write(msg.content);
          break;
        case "tool_start":
          console.log(`Tool: ${msg.tool_name}`);
          break;
        case "approval_request":
          // Auto-approve for demo (don't do this in production)
          ws.send(JSON.stringify({
            type: "approve",
            approval_id: msg.approval_id,
            approved: true
          }));
          break;
      }
      break;
  }
};

// Send a message
ws.send(JSON.stringify({
  type: "send",
  text: "Hello, what's the weather like?"
}));
```
