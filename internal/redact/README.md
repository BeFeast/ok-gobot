# Redact Package

A simple and efficient Go package for redacting sensitive information from log messages and strings.

## Features

The `redact` package automatically masks the following sensitive patterns:

- **API Keys**: OpenAI keys (`sk-...`), OpenRouter keys (`sk-or-...`), generic keys (`key-...`)
- **Bearer Tokens**: OAuth and JWT tokens in `Bearer <token>` format
- **Bot Tokens**: Telegram-style bot tokens (`digits:alphanumeric`)
- **Generic Secrets**: Long hexadecimal and base64-encoded strings (32+ characters)

## Usage

```go
import "ok-gobot/internal/redact"

// Redact a log message
logMessage := "Connecting to API with key sk-test123456789012345678901234567890"
safe := redact.Redact(logMessage)
// Output: "Connecting to API with key sk-test12***"
```

## Redaction Rules

The package uses the following redaction strategy:

| Pattern Type | Detection | Redaction Format | Example |
|-------------|-----------|------------------|---------|
| API Keys (`sk-...`) | Starts with `sk-` + 16+ chars | First 6 chars + `***` | `sk-test12***` |
| OpenRouter Keys | Starts with `sk-or-` + 13+ chars | First 6 chars after prefix + `***` | `sk-or-v1-***` |
| Generic Keys | Starts with `key-` + 16+ chars | First 6 chars + `***` | `key-abc123***` |
| Bearer Tokens | `Bearer` + space + token | `Bearer ***` | `Bearer ***` |
| Bot Tokens | 6+ digits + `:` + 10+ chars | First 6 digits + `***` | `123456***` |
| Hex Secrets | 32+ hex characters | First 6 chars + `***` | `a1b2c3***` |
| Base64 Secrets | 32+ base64 chars with `+/=` | First 6 chars + `***` | `dGhpc2***` |

## Examples

### Multiple Secrets

```go
logMessage := "Auth: sk-abc123def456789012 Bot: 123456789:ABCdefGHIjklMNOpqrsTUVwxyz"
safe := redact.Redact(logMessage)
// Output: "Auth: sk-abc123*** Bot: 123456***"
```

### Bearer Token

```go
logMessage := "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
safe := redact.Redact(logMessage)
// Output: "Authorization: Bearer ***"
```

### JSON Configuration

```go
config := `{"api_key":"sk-1234567890abcdefghijklmnopqrstuvwxyz"}`
safe := redact.Redact(config)
// Output: {"api_key":"sk-123456***"}
```

## What's NOT Redacted

The package is conservative to avoid false positives:

- UUIDs (contain dashes)
- Short strings (< 32 characters for generic secrets)
- Normal words and sentences
- Numeric IDs
- Short hex codes (like color codes `#ff5733`)

## Performance

Benchmarks on Apple M1 Max:

```
BenchmarkRedact-10             111399    10577 ns/op    1341 B/op    23 allocs/op
BenchmarkRedactNoSecrets-10    123295     9707 ns/op    1058 B/op    18 allocs/op
```

The package is optimized for typical log messages and has minimal performance impact.

## Integration with Logging

Use as a drop-in filter for your logging:

```go
// Before logging
logger.Info(redact.Redact(fmt.Sprintf("User authenticated with token: %s", token)))

// Or wrap your logger
type SafeLogger struct {
    logger *log.Logger
}

func (l *SafeLogger) Info(msg string) {
    l.logger.Info(redact.Redact(msg))
}
```

## Testing

Run tests:

```bash
go test ./internal/redact/...
```

Run benchmarks:

```bash
go test ./internal/redact/... -bench=. -benchmem
```

Run examples:

```bash
go test ./internal/redact/... -run Example
```
