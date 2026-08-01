package evidence

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"ok-gobot/internal/redact"
)

const (
	EventPreflight      = "preflight"
	EventBackendModel   = "backend_model"
	EventWorkspace      = "workspace"
	EventCommand        = "command"
	EventPullRequest    = "pull_request"
	EventCheckRollup    = "check_rollup"
	EventReviewFeedback = "review_feedback"
	EventRetryDecision  = "retry_decision"
	EventFinalDecision  = "final_decision"
	EventJob            = "job_event"
	EventArtifact       = "artifact"
)

const (
	MaxSummaryRunes       = 320
	MaxPayloadStringRunes = 1200
	MaxPayloadKeys        = 64
	MaxPayloadItems       = 32
)

// Event is one structured, append-only evidence ledger entry for a session.
type Event struct {
	ID         int64          `json:"id,omitempty"`
	SessionKey string         `json:"session_key,omitempty"`
	JobID      string         `json:"job_id,omitempty"`
	Type       string         `json:"type"`
	Status     string         `json:"status,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
	CreatedAt  string         `json:"created_at,omitempty"`
}

// RenderOptions controls compact Markdown rendering for Mission Control and Telegram.
type RenderOptions struct {
	Limit          int
	Heading        string
	MaxSummaryRune int
}

// SanitizeEvent redacts secrets and caps large raw fields before persistence or rendering.
func SanitizeEvent(event Event) Event {
	event.SessionKey = strings.TrimSpace(event.SessionKey)
	event.JobID = strings.TrimSpace(event.JobID)
	event.Type = strings.TrimSpace(event.Type)
	event.Status = clipOneLine(redact.Redact(strings.TrimSpace(event.Status)), MaxSummaryRunes)
	event.Summary = clipOneLine(redact.Redact(strings.TrimSpace(event.Summary)), MaxSummaryRunes)
	if event.Payload != nil {
		if payload, ok := sanitizeValue(event.Payload, "").(map[string]any); ok {
			event.Payload = payload
		}
	}
	return event
}

// PayloadMap converts arbitrary structured payloads into a map suitable for an Event.
func PayloadMap(payload any) (map[string]any, error) {
	if payload == nil {
		return nil, nil
	}
	switch v := payload.(type) {
	case map[string]any:
		return v, nil
	case map[string]string:
		out := make(map[string]any, len(v))
		for k, value := range v {
			out[k] = value
		}
		return out, nil
	case string:
		return payloadFromJSONOrValue([]byte(v), v), nil
	case []byte:
		return payloadFromJSONOrValue(v, string(v)), nil
	case json.RawMessage:
		return payloadFromJSONOrValue(v, string(v)), nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal evidence payload: %w", err)
		}
		return payloadFromJSONOrValue(b, string(b)), nil
	}
}

// MarshalPayload sanitizes and serializes an event payload for storage.
func MarshalPayload(payload map[string]any) (string, error) {
	if len(payload) == 0 {
		return "", nil
	}
	clean, ok := sanitizeValue(payload, "").(map[string]any)
	if !ok || len(clean) == 0 {
		return "", nil
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return "", fmt.Errorf("marshal evidence payload: %w", err)
	}
	return string(b), nil
}

// DecodePayload deserializes a stored payload string. Invalid legacy values are preserved.
func DecodePayload(payload string) map[string]any {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return map[string]any{"payload": clipOneLine(redact.Redact(payload), MaxPayloadStringRunes)}
	}
	if m, ok := decoded.(map[string]any); ok {
		return m
	}
	return map[string]any{"payload": decoded}
}

// JSONLine returns an append-friendly JSONL representation of one sanitized event.
func JSONLine(event Event) ([]byte, error) {
	clean := SanitizeEvent(event)
	b, err := json.Marshal(clean)
	if err != nil {
		return nil, fmt.Errorf("marshal evidence event: %w", err)
	}
	return append(b, '\n'), nil
}

// RenderMarkdown renders a concise evidence timeline suitable for compact UIs.
func RenderMarkdown(events []Event, opts RenderOptions) string {
	limit := opts.Limit
	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}
	maxSummary := opts.MaxSummaryRune
	if maxSummary <= 0 {
		maxSummary = MaxSummaryRunes
	}

	var b strings.Builder
	if opts.Heading != "" {
		b.WriteString(opts.Heading)
		b.WriteString("\n")
	}
	if limit == 0 {
		b.WriteString("No evidence recorded.")
		return b.String()
	}
	for i := 0; i < limit; i++ {
		line := CompactLine(SanitizeEvent(events[i]), maxSummary)
		b.WriteString("- ")
		b.WriteString(line)
		if i < limit-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// CompactLine renders a single event as one Markdown-safe timeline line.
func CompactLine(event Event, maxSummaryRunes int) string {
	if maxSummaryRunes <= 0 {
		maxSummaryRunes = MaxSummaryRunes
	}
	parts := []string{eventLabel(event.Type)}
	if ts := formatTime(event.CreatedAt); ts != "" {
		parts = append([]string{ts}, parts...)
	}
	if event.Status != "" {
		parts = append(parts, "["+event.Status+"]")
	}

	summary := event.Summary
	if summary == "" {
		summary = deriveSummary(event)
	}
	if summary == "" {
		summary = "recorded"
	}
	return strings.Join(parts, " ") + ": " + clipOneLine(summary, maxSummaryRunes)
}

func payloadFromJSONOrValue(raw []byte, fallback string) map[string]any {
	var decoded any
	if len(strings.TrimSpace(string(raw))) > 0 && json.Unmarshal(raw, &decoded) == nil {
		if m, ok := decoded.(map[string]any); ok {
			return m
		}
		return map[string]any{"value": decoded}
	}
	return map[string]any{"value": fallback}
}

func sanitizeValue(value any, key string) any {
	if isSensitiveKey(key) {
		return "***"
	}
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return clipOneLine(redact.Redact(v), MaxPayloadStringRunes)
	case []byte:
		return clipOneLine(redact.Redact(string(v)), MaxPayloadStringRunes)
	case json.RawMessage:
		return clipOneLine(redact.Redact(string(v)), MaxPayloadStringRunes)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, min(len(keys), MaxPayloadKeys))
		for i, k := range keys {
			if i >= MaxPayloadKeys {
				out["_truncated_keys"] = len(keys) - MaxPayloadKeys
				break
			}
			cleanKey := clipOneLine(redact.Redact(strings.TrimSpace(k)), 96)
			if cleanKey == "" {
				continue
			}
			out[cleanKey] = sanitizeValue(v[k], k)
		}
		return out
	case map[string]string:
		converted := make(map[string]any, len(v))
		for k, item := range v {
			converted[k] = item
		}
		return sanitizeValue(converted, key)
	case []any:
		limit := min(len(v), MaxPayloadItems)
		out := make([]any, 0, limit+1)
		for i := 0; i < limit; i++ {
			out = append(out, sanitizeValue(v[i], key))
		}
		if len(v) > limit {
			out = append(out, map[string]any{"_truncated_items": len(v) - limit})
		}
		return out
	default:
		return v
	}
}

func isSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{"api_key", "apikey", "token", "secret", "password", "credential", "authorization"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func deriveSummary(event Event) string {
	switch event.Type {
	case EventBackendModel:
		return joinPayloadFields(event.Payload, "backend", "model", "model_tier", "role")
	case EventWorkspace:
		return joinPayloadFields(event.Payload, "branch", "worktree", "worktree_path")
	case EventCommand:
		return joinPayloadFields(event.Payload, "command", "exit_status")
	case EventPullRequest:
		return joinPayloadFields(event.Payload, "url", "pr_url")
	case EventCheckRollup:
		return joinPayloadFields(event.Payload, "conclusion", "failed", "pending", "passed")
	case EventReviewFeedback:
		return joinPayloadFields(event.Payload, "state", "author", "body")
	case EventRetryDecision:
		return joinPayloadFields(event.Payload, "decision", "retry_job_id", "reason")
	case EventFinalDecision:
		return joinPayloadFields(event.Payload, "outcome", "blocker", "limit_reason")
	default:
		return ""
	}
}

func joinPayloadFields(payload map[string]any, keys ...string) string {
	if len(payload) == 0 {
		return ""
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " - ")
}

func eventLabel(eventType string) string {
	switch eventType {
	case EventPreflight:
		return "Preflight"
	case EventBackendModel:
		return "Backend/model"
	case EventWorkspace:
		return "Workspace"
	case EventCommand:
		return "Command"
	case EventPullRequest:
		return "PR"
	case EventCheckRollup:
		return "Checks"
	case EventReviewFeedback:
		return "Review"
	case EventRetryDecision:
		return "Retry"
	case EventFinalDecision:
		return "Final"
	case EventArtifact:
		return "Artifact"
	case EventJob:
		return "Job"
	default:
		if eventType == "" {
			return "Evidence"
		}
		return strings.ReplaceAll(eventType, "_", " ")
	}
}

func formatTime(ts string) string {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return ""
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.Format("15:04:05")
		}
	}
	return ts
}

func clipOneLine(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxRunes <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "... [truncated]"
}
