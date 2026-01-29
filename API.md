# HTTP API Documentation

ok-gobot provides a simple HTTP API for external integrations.

## Configuration

Enable the API in your `config.yaml`:

```yaml
api:
  enabled: true
  port: 8080
  api_key: "your-secret-api-key"
  webhook_chat: 123456789  # Optional: default chat for webhooks
```

**Security Note**: The `api_key` is required when API is enabled. Keep it secret.

## Authentication

All endpoints except `/api/health` require authentication. Provide the API key using either:

- **Header**: `X-API-Key: your-secret-api-key`
- **Bearer Token**: `Authorization: Bearer your-secret-api-key`

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
curl -H "X-API-Key: your-secret-api-key" \
  http://localhost:8080/api/status

# Send message
curl -X POST \
  -H "X-API-Key: your-secret-api-key" \
  -H "Content-Type: application/json" \
  -d '{"chat_id": 123456789, "text": "Hello!"}' \
  http://localhost:8080/api/send

# Send webhook
curl -X POST \
  -H "Authorization: Bearer your-secret-api-key" \
  -H "Content-Type: application/json" \
  -d '{"event": "test", "data": {"key": "value"}}' \
  http://localhost:8080/api/webhook
```

### Python

```python
import requests

API_URL = "http://localhost:8080"
API_KEY = "your-secret-api-key"

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
const API_KEY = "your-secret-api-key";

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
API_KEY="your-secret-api-key"

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
        headers={"X-API-Key": "your-secret-api-key"},
        json={
            "chat_id": 123456789,
            "text": f"🚨 Alert: {alert_message}"
        }
    )
    return response.json()
```

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
