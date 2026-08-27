package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	chatGPTFailoverIntegrationPrimaryModel  = "gpt-5.6-sol"
	chatGPTFailoverIntegrationFallbackModel = "gpt-5.6-luna"
	chatGPTFailoverIntegrationAnswer        = "Coherent fallback answer."
)

const chatGPTFailoverIntegrationCompletedSSE = `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Coherent fallback "}` + "\n\n" +
	`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"answer."}` + "\n\n" +
	`data: {"type":"response.completed","response":{"id":"resp_fallback","status":"completed","model":"gpt-5.6-luna","output":[{"id":"msg_fallback","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Coherent fallback answer."}]}]}}` + "\n\n" +
	"data: [DONE]\n\n"

type chatGPTFailoverIntegrationCounts struct {
	probe    atomic.Int32
	primary  atomic.Int32
	fallback atomic.Int32
}

func newChatGPTFailoverIntegrationServer(t *testing.T, primarySSE string) (*httptest.Server, *chatGPTFailoverIntegrationCounts) {
	t.Helper()

	counts := &chatGPTFailoverIntegrationCounts{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != chatGPTCodexPath {
			t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, chatGPTCodexPath)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if string(body) == "{" {
			counts.probe.Add(1)
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}

		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode runtime request %q: %v", body, err)
			http.Error(w, "invalid runtime request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		switch request.Model {
		case chatGPTFailoverIntegrationPrimaryModel:
			counts.primary.Add(1)
			_, _ = io.WriteString(w, primarySSE)
		case chatGPTFailoverIntegrationFallbackModel:
			counts.fallback.Add(1)
			_, _ = io.WriteString(w, chatGPTFailoverIntegrationCompletedSSE)
		default:
			t.Errorf("runtime model = %q, want primary or fallback", request.Model)
			http.Error(w, "unexpected model", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return server, counts
}

func chatGPTFailoverIntegrationConfig(t *testing.T, baseURL string) ProviderConfig {
	t.Helper()
	return ProviderConfig{
		Name:               "chatgpt",
		Model:              chatGPTFailoverIntegrationPrimaryModel,
		BaseURL:            baseURL,
		ChatGPTAuthFile:    writeFakeCodexAuth(t, time.Now().Add(2*time.Hour)),
		ChatGPTCodexBinary: "/bin/false",
	}
}

func newChatGPTFailoverIntegrationClient(t *testing.T, cfg ProviderConfig) *FailoverClient {
	t.Helper()
	client, err := NewClientWithFailover(cfg, []string{chatGPTFailoverIntegrationFallbackModel})
	if err != nil {
		t.Fatalf("NewClientWithFailover: %v", err)
	}
	return client
}

type chatGPTFailoverIntegrationStream struct {
	name string
	run  func(*FailoverClient) <-chan StreamChunk
}

func chatGPTFailoverIntegrationStreams() []chatGPTFailoverIntegrationStream {
	return []chatGPTFailoverIntegrationStream{
		{
			name: "CompleteStream",
			run: func(client *FailoverClient) <-chan StreamChunk {
				return client.CompleteStream(context.Background(), []Message{{Role: RoleUser, Content: "hello"}})
			},
		},
		{
			name: "CompleteStreamWithTools",
			run: func(client *FailoverClient) <-chan StreamChunk {
				return client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "hello"}}, nil)
			},
		},
	}
}

func TestChatGPTFailoverIntegrationRetryableSSEFailures(t *testing.T) {
	tests := []struct {
		name        string
		primarySSE  string
		failureKind BackendFailureKind
	}{
		{
			name:        "response.failed server_error",
			primarySSE:  chatGPTFailoverResponseFailedSSE("server_error"),
			failureKind: BackendFailureUnavailable,
		},
		{
			name:        "response.failed internal_error",
			primarySSE:  chatGPTFailoverResponseFailedSSE("internal_error"),
			failureKind: BackendFailureUnavailable,
		},
		{
			name:        "response.failed service_unavailable",
			primarySSE:  chatGPTFailoverResponseFailedSSE("service_unavailable"),
			failureKind: BackendFailureUnavailable,
		},
		{
			name:        "top-level rate_limit_exceeded",
			primarySSE:  "data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"PRIMARY rate limit must not leak\"}\n\ndata: [DONE]\n\n",
			failureKind: BackendFailureRateLimit,
		},
		{
			name:        "clean early EOF",
			primarySSE:  "",
			failureKind: BackendFailureUnavailable,
		},
	}

	for _, test := range tests {
		for _, stream := range chatGPTFailoverIntegrationStreams() {
			t.Run(test.name+"/"+stream.name, func(t *testing.T) {
				server, counts := newChatGPTFailoverIntegrationServer(t, test.primarySSE)
				client := newChatGPTFailoverIntegrationClient(t, chatGPTFailoverIntegrationConfig(t, server.URL))

				content, finishReason, err := drainFailoverStream(stream.run(client))
				if err != nil {
					t.Fatalf("stream error = %v", err)
				}
				if content != chatGPTFailoverIntegrationAnswer || finishReason != "stop" {
					t.Fatalf("content=%q finish=%q, want coherent fallback only", content, finishReason)
				}
				if strings.Contains(content, "PRIMARY") {
					t.Fatalf("primary content leaked into fallback response: %q", content)
				}
				if got := counts.probe.Load(); got != 0 {
					t.Fatalf("probe calls = %d, want 0", got)
				}
				if got := counts.primary.Load(); got != 1 {
					t.Fatalf("primary calls = %d, want 1", got)
				}
				if got := counts.fallback.Load(); got != 1 {
					t.Fatalf("fallback calls = %d, want 1", got)
				}

				decision := client.LastFallbackDecision()
				if decision.Action != FallbackActionFallback || decision.FailureKind != test.failureKind || decision.FromModel != chatGPTFailoverIntegrationPrimaryModel || decision.ToModel != chatGPTFailoverIntegrationFallbackModel {
					t.Fatalf("fallback decision = %+v", decision)
				}
			})
		}
	}
}

func TestChatGPTFailoverIntegrationPreflightRuntimeHealth(t *testing.T) {
	server, counts := newChatGPTFailoverIntegrationServer(t, chatGPTFailoverResponseFailedSSE("server_error"))
	cfg := chatGPTFailoverIntegrationConfig(t, server.URL)
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider:        cfg,
		FallbackModels:  []string{chatGPTFailoverIntegrationFallbackModel},
		FallbackEnabled: true,
		Probe:           ProbeProvider,
	})

	preflight, err := checker.Check(context.Background(), chatGPTFailoverIntegrationPrimaryModel, "interaction", "high")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if preflight.Status != BackendHealthHealthy || preflight.Identity.Model != chatGPTFailoverIntegrationPrimaryModel {
		t.Fatalf("preflight health = %+v, want healthy primary", preflight)
	}
	if preflight.Fallback.Action != FallbackActionPrimary {
		t.Fatalf("preflight decision = %+v, want primary", preflight.Fallback)
	}
	if got := counts.probe.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	if got := counts.primary.Load(); got != 0 {
		t.Fatalf("primary runtime calls before stream = %d, want 0", got)
	}
	if got := counts.fallback.Load(); got != 0 {
		t.Fatalf("fallback runtime calls before stream = %d, want 0", got)
	}

	client := newChatGPTFailoverIntegrationClient(t, cfg)
	tracked, ok := TrackClient(client, BackendIdentity{
		Provider: "chatgpt",
		Backend:  "chatgpt",
		Model:    chatGPTFailoverIntegrationPrimaryModel,
		Tier:     "interaction",
		Effort:   "high",
		BaseURL:  server.URL,
	}, checker.RecordRuntimeOutcome).(*FailoverClient)
	if !ok {
		t.Fatalf("tracked client type = %T, want *FailoverClient", tracked)
	}

	content, finishReason, err := drainFailoverStream(tracked.CompleteStreamWithTools(
		context.Background(),
		[]ChatMessage{{Role: RoleUser, Content: "hello"}},
		nil,
	))
	if err != nil {
		t.Fatalf("CompleteStreamWithTools: %v", err)
	}
	if content != chatGPTFailoverIntegrationAnswer || finishReason != "stop" {
		t.Fatalf("content=%q finish=%q, want coherent fallback only", content, finishReason)
	}
	if strings.Contains(content, "PRIMARY") {
		t.Fatalf("primary content leaked into fallback response: %q", content)
	}
	if got := counts.probe.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	if got := counts.primary.Load(); got != 1 {
		t.Fatalf("primary runtime calls = %d, want 1", got)
	}
	if got := counts.fallback.Load(); got != 1 {
		t.Fatalf("fallback runtime calls = %d, want 1", got)
	}

	finalHealth := checker.Snapshot()
	if finalHealth.Status != BackendHealthHealthy || finalHealth.Identity.Model != chatGPTFailoverIntegrationFallbackModel {
		t.Fatalf("final health = %+v, want healthy fallback", finalHealth)
	}
	if finalHealth.Fallback.Action != FallbackActionFallback || finalHealth.Fallback.FromModel != chatGPTFailoverIntegrationPrimaryModel || finalHealth.Fallback.ToModel != chatGPTFailoverIntegrationFallbackModel {
		t.Fatalf("final health decision = %+v", finalHealth.Fallback)
	}
}

func TestChatGPTFailoverIntegrationIncompleteDoesNotFallback(t *testing.T) {
	const incompleteSSE = "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\ndata: [DONE]\n\n"

	for _, stream := range chatGPTFailoverIntegrationStreams() {
		t.Run(stream.name, func(t *testing.T) {
			server, counts := newChatGPTFailoverIntegrationServer(t, incompleteSSE)
			cfg := chatGPTFailoverIntegrationConfig(t, server.URL)
			checker := NewBackendPreflight(BackendPreflightConfig{
				Provider:        cfg,
				FallbackModels:  []string{chatGPTFailoverIntegrationFallbackModel},
				FallbackEnabled: true,
			})
			client := newChatGPTFailoverIntegrationClient(t, cfg)
			tracked := TrackClient(client, BackendIdentity{
				Provider: "chatgpt",
				Backend:  "chatgpt",
				Model:    chatGPTFailoverIntegrationPrimaryModel,
				BaseURL:  server.URL,
			}, checker.RecordRuntimeOutcome).(*FailoverClient)

			content, finishReason, err := drainFailoverStream(stream.run(tracked))
			if err == nil || !strings.Contains(err.Error(), "max_output_tokens") {
				t.Fatalf("stream content=%q finish=%q error=%v, want terminal max_output_tokens error", content, finishReason, err)
			}
			if content != "" {
				t.Fatalf("content = %q, want no content from incomplete primary", content)
			}
			if got := ClassifyBackendError(err); got != BackendFailureUnknown {
				t.Fatalf("ClassifyBackendError(%v) = %s, want unknown/non-fallback", err, got)
			}
			if got := counts.primary.Load(); got != 1 {
				t.Fatalf("primary calls = %d, want 1", got)
			}
			if got := counts.fallback.Load(); got != 0 {
				t.Fatalf("fallback calls = %d, want 0", got)
			}

			decision := tracked.LastFallbackDecision()
			if decision.Action != FallbackActionApproval || decision.FromModel != chatGPTFailoverIntegrationPrimaryModel || decision.ToModel != "" {
				t.Fatalf("incomplete fallback decision = %+v, want non-fallback approval", decision)
			}
			health := checker.Snapshot()
			if health.Identity.Model != chatGPTFailoverIntegrationPrimaryModel || health.Status != BackendHealthApprovalRequired || health.Fallback.Action != FallbackActionApproval {
				t.Fatalf("incomplete runtime health = %+v, want primary approval-required", health)
			}
		})
	}
}

func chatGPTFailoverResponseFailedSSE(code string) string {
	return fmt.Sprintf("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":%q,\"message\":\"PRIMARY failure must not leak\"}}}\n\ndata: [DONE]\n\n", code)
}
