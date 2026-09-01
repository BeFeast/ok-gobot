package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReasoningEffortMapping(t *testing.T) {
	t.Parallel()

	pass := map[string]string{
		"low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max",
		"HIGH": "high", "  high  ": "high",
	}
	for in, want := range pass {
		c := &OpenAICompatibleClient{config: ProviderConfig{ThinkLevel: in}}
		if got := c.reasoningEffort(); got != want {
			t.Errorf("reasoningEffort(%q) = %q, want %q", in, got, want)
		}
	}

	// The wire accepts low/medium/high/xhigh/max only, so anything else must be
	// omitted rather than guessed at — including "off", which has no
	// representation here.
	for _, in := range []string{"", "off", "none", "adaptive", "nonsense"} {
		c := &OpenAICompatibleClient{config: ProviderConfig{ThinkLevel: in}}
		if got := c.reasoningEffort(); got != "" {
			t.Errorf("reasoningEffort(%q) = %q, want it omitted", in, got)
		}
	}
}

// Every request path must carry the level. Dropping it on one path is the
// failure this guards: the level is invisible in logs, so a half-wired change
// degrades answers silently instead of erroring.
func TestReasoningEffortReachesEveryRequestPath(t *testing.T) {
	t.Parallel()

	bodies := make(chan map[string]any, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	newClient := func() *OpenAICompatibleClient {
		return &OpenAICompatibleClient{
			config:     ProviderConfig{Name: "openai", BaseURL: server.URL, Model: "m", ThinkLevel: "high"},
			httpClient: &http.Client{},
		}
	}

	ctx := context.Background()
	_, _ = newClient().Complete(ctx, []Message{{Role: "user", Content: "hi"}})
	_, _ = newClient().CompleteWithTools(ctx, []ChatMessage{{Role: "user", Content: "hi"}}, nil)
	for range newClient().CompleteStream(ctx, []Message{{Role: "user", Content: "hi"}}) {
	}
	for range newClient().CompleteStreamWithTools(ctx, []ChatMessage{{Role: "user", Content: "hi"}}, nil) {
	}

	seen := 0
	for len(bodies) > 0 {
		body := <-bodies
		seen++
		if body["reasoning_effort"] != "high" {
			t.Errorf("request %d omitted reasoning_effort: %v", seen, body["reasoning_effort"])
		}
	}
	if seen != 4 {
		t.Fatalf("expected all four request paths to reach the server, saw %d", seen)
	}
}

func TestReasoningEffortOmittedWhenUnset(t *testing.T) {
	t.Parallel()

	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	c := &OpenAICompatibleClient{
		config:     ProviderConfig{Name: "openai", BaseURL: server.URL, Model: "m", ThinkLevel: "off"},
		httpClient: &http.Client{},
	}
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, present := body["reasoning_effort"]; present {
		t.Fatalf("reasoning_effort must be absent for level off, body = %v", body)
	}
}
