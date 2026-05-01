package evidence

import (
	"strings"
	"testing"
)

func TestSanitizeEventRedactsSecretsAndTruncatesPayload(t *testing.T) {
	longOutput := strings.Repeat("line ", 400)
	event := SanitizeEvent(Event{
		Type:    EventCommand,
		Status:  "failed",
		Summary: "command failed with sk-1234567890abcdefghijklmnop",
		Payload: map[string]any{
			"command":   "go test ./...",
			"api_token": "plain-local-token",
			"stdout":    longOutput,
			"nested": map[string]any{
				"authorization": "Bearer abc.def.ghi",
			},
		},
	})

	if strings.Contains(event.Summary, "abcdefghijklmnop") {
		t.Fatalf("summary leaked secret: %q", event.Summary)
	}
	if !strings.Contains(event.Summary, "sk-123456***") {
		t.Fatalf("summary did not keep redacted prefix: %q", event.Summary)
	}
	if got := event.Payload["api_token"]; got != "***" {
		t.Fatalf("api_token = %v, want redacted", got)
	}
	stdout, ok := event.Payload["stdout"].(string)
	if !ok || !strings.Contains(stdout, "[truncated]") {
		t.Fatalf("stdout was not truncated: %#v", event.Payload["stdout"])
	}
	nested := event.Payload["nested"].(map[string]any)
	if nested["authorization"] != "***" {
		t.Fatalf("authorization = %v, want redacted", nested["authorization"])
	}
}

func TestRenderMarkdownCompactsEvidenceTimeline(t *testing.T) {
	events := []Event{
		{
			Type:      EventPreflight,
			Status:    "passed",
			Summary:   "go vet ./... completed",
			CreatedAt: "2026-05-01 12:00:00",
		},
		{
			Type:   EventCommand,
			Status: "failed",
			Payload: map[string]any{
				"command":     "go test ./...",
				"exit_status": 1,
			},
			CreatedAt: "2026-05-01 12:01:00",
		},
	}

	md := RenderMarkdown(events, RenderOptions{Heading: "Evidence", Limit: 2})
	for _, want := range []string{
		"Evidence",
		"- 12:00:00 Preflight [passed]: go vet ./... completed",
		"- 12:01:00 Command [failed]: go test ./... - 1",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("expected %q in markdown:\n%s", want, md)
		}
	}
	if strings.Contains(md, "\n  -") {
		t.Fatalf("expected flat markdown bullets, got:\n%s", md)
	}
}

func TestJSONLineSanitizesEvent(t *testing.T) {
	line, err := JSONLine(Event{
		Type:    EventPullRequest,
		Summary: "opened with Bearer secret-token",
	})
	if err != nil {
		t.Fatalf("JSONLine error = %v", err)
	}
	if !strings.HasSuffix(string(line), "\n") {
		t.Fatalf("JSONLine missing newline: %q", string(line))
	}
	if strings.Contains(string(line), "secret-token") {
		t.Fatalf("JSONLine leaked secret: %s", string(line))
	}
}
