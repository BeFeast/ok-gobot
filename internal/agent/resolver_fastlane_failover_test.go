package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/tools"
)

const (
	resolverFailoverDefaultModel     = "gpt-5.6-sol"
	resolverFailoverInteractionModel = "gpt-5.6-luna"
	resolverFailoverLastModel        = "gpt-5.4"
	resolverFailoverAnswer           = "Coherent fallback-only answer."
)

const resolverFailoverCompletedSSE = `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Coherent fallback-only "}` + "\n\n" +
	`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"answer."}` + "\n\n" +
	`data: {"type":"response.completed","response":{"id":"resp_fallback","status":"completed","model":"gpt-5.4","output":[{"id":"msg_fallback","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Coherent fallback-only answer."}]}]}}` + "\n\n" +
	"data: [DONE]\n\n"

type resolverFailoverStore struct{}

func (*resolverFailoverStore) GetModelOverride(int64) (string, error) { return "", nil }
func (*resolverFailoverStore) GetActiveAgent(int64) (string, error)   { return "default", nil }
func (*resolverFailoverStore) GetSessionOption(int64, string) (string, error) {
	return "", nil
}

type resolverFailoverRuntimeRequest struct {
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	Reasoning *struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
	RawBody string `json:"-"`
}

func TestRunResolverInteractionClientFailsOverAfterHealthyPreflight(t *testing.T) {
	const userMarker = "resolver-fast-lane-regression"

	var (
		mu              sync.Mutex
		probeBodies     []string
		runtimeRequests []resolverFailoverRuntimeRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/codex/responses" {
			http.Error(w, "unexpected route", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Accept") != "text/event-stream" {
			http.Error(w, "missing runtime auth headers", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "could not read body", http.StatusInternalServerError)
			return
		}
		if string(body) == "{" {
			mu.Lock()
			probeBodies = append(probeBodies, string(body))
			mu.Unlock()
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}

		var request resolverFailoverRuntimeRequest
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, "malformed runtime request", http.StatusBadRequest)
			return
		}
		request.RawBody = string(body)
		mu.Lock()
		runtimeRequests = append(runtimeRequests, request)
		mu.Unlock()

		if !request.Stream || request.Reasoning == nil || request.Reasoning.Effort != "low" || !strings.Contains(request.RawBody, userMarker) {
			http.Error(w, "wrong runtime request body", http.StatusBadRequest)
			return
		}

		switch request.Model {
		case resolverFailoverInteractionModel:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"server_error","message":"PRIMARY LUNA CONTENT MUST NOT LEAK"}}`)
		case resolverFailoverLastModel:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, resolverFailoverCompletedSSE)
		default:
			http.Error(w, "unexpected runtime model", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	fallbackOrder := []string{resolverFailoverInteractionModel, resolverFailoverLastModel}
	preflight := ai.NewBackendPreflight(ai.BackendPreflightConfig{
		Provider: ai.ProviderConfig{
			Name:       "chatgpt",
			APIKey:     "test-token",
			BaseURL:    server.URL,
			Model:      resolverFailoverDefaultModel,
			ThinkLevel: "high",
		},
		FallbackModels:  fallbackOrder,
		FallbackEnabled: true,
		Probe:           ai.ProbeProvider,
	})
	resolver := &RunResolver{
		Store:              &resolverFailoverStore{},
		DefaultPersonality: &Personality{},
		ToolRegistry:       tools.NewRegistry(),
		AIConfig: AIResolverConfig{
			Provider:               "chatgpt",
			APIKey:                 "test-token",
			BaseURL:                server.URL,
			Model:                  resolverFailoverDefaultModel,
			DefaultThinking:        "high",
			DefaultClient:          &stubAIClient{response: "default must not run"},
			FallbackModels:         fallbackOrder,
			InteractionModel:       resolverFailoverInteractionModel,
			InteractionThinking:    "low",
			BackendPreflight:       preflight.Check,
			BackendOutcomeReporter: preflight.RecordRuntimeOutcome,
		},
	}

	components, err := resolver.Resolve(42, &RunOverrides{UseInteraction: true}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if components.Model != resolverFailoverInteractionModel || components.Effort != "low" {
		t.Fatalf("resolved model=%q effort=%q, want interaction luna/low", components.Model, components.Effort)
	}
	if components.BackendHealth.Status != ai.BackendHealthHealthy || components.BackendHealth.Identity.Model != resolverFailoverInteractionModel {
		t.Fatalf("preflight health = %+v, want healthy interaction primary", components.BackendHealth)
	}
	if components.BackendHealth.Fallback.Action != ai.FallbackActionPrimary {
		t.Fatalf("preflight decision = %+v, want healthy primary", components.BackendHealth.Fallback)
	}

	streamClient, ok := components.Agent.aiClient.(ai.StreamingClient)
	if !ok {
		t.Fatalf("resolver client = %T, want ai.StreamingClient", components.Agent.aiClient)
	}
	if _, ok := components.Agent.aiClient.(*ai.FailoverClient); !ok {
		t.Fatalf("resolver client = %T, want runtime failover client", components.Agent.aiClient)
	}

	content, finishReason, streamErr := collectResolverFailoverStream(streamClient.CompleteStreamWithTools(
		context.Background(),
		[]ai.ChatMessage{{Role: ai.RoleUser, Content: userMarker}},
		nil,
	))
	if streamErr != nil {
		t.Fatalf("interaction stream: %v", streamErr)
	}
	if content != resolverFailoverAnswer || finishReason != "stop" {
		t.Fatalf("content=%q finish=%q, want coherent fallback-only response", content, finishReason)
	}
	if strings.Contains(strings.ToLower(content), "luna") || strings.Contains(content, "PRIMARY") {
		t.Fatalf("primary content leaked into fallback response: %q", content)
	}

	mu.Lock()
	gotProbeBodies := append([]string(nil), probeBodies...)
	gotRuntimeRequests := append([]resolverFailoverRuntimeRequest(nil), runtimeRequests...)
	mu.Unlock()
	if len(gotProbeBodies) != 1 || gotProbeBodies[0] != "{" {
		t.Fatalf("probe bodies = %q, want one deliberately malformed POST body", gotProbeBodies)
	}
	if len(gotRuntimeRequests) != 2 {
		t.Fatalf("runtime requests = %+v, want luna failure followed by gpt-5.4", gotRuntimeRequests)
	}
	if gotRuntimeRequests[0].Model != resolverFailoverInteractionModel || gotRuntimeRequests[1].Model != resolverFailoverLastModel {
		t.Fatalf("runtime model order = [%q %q], want [luna gpt-5.4]", gotRuntimeRequests[0].Model, gotRuntimeRequests[1].Model)
	}

	finalHealth := preflight.Snapshot()
	if finalHealth.Status != ai.BackendHealthHealthy || finalHealth.Identity.Model != resolverFailoverLastModel {
		t.Fatalf("runtime health = %+v, want healthy gpt-5.4 fallback", finalHealth)
	}
	if finalHealth.Fallback.Action != ai.FallbackActionFallback || finalHealth.Fallback.FromModel != resolverFailoverInteractionModel || finalHealth.Fallback.ToModel != resolverFailoverLastModel {
		t.Fatalf("runtime fallback decision = %+v", finalHealth.Fallback)
	}
}

func TestBuildAIClientDoesNotInventFallbackPastConfiguredOrder(t *testing.T) {
	resolver := &RunResolver{AIConfig: AIResolverConfig{
		Provider:        "chatgpt",
		APIKey:          "test-token",
		Model:           resolverFailoverDefaultModel,
		DefaultThinking: "high",
		DefaultClient:   &stubAIClient{response: "default"},
		FallbackModels:  []string{resolverFailoverInteractionModel, resolverFailoverLastModel},
	}}

	for _, model := range []string{resolverFailoverLastModel, "unknown-model"} {
		t.Run(model, func(t *testing.T) {
			client := resolver.buildAIClient(model, "override", "low")
			if _, ok := client.(*ai.FailoverClient); ok {
				t.Fatalf("buildAIClient(%q) invented a fallback past the configured order", model)
			}
			if _, ok := client.(ai.StreamingClient); !ok {
				t.Fatalf("buildAIClient(%q) = %T, want provider streaming client", model, client)
			}
		})
	}
}

func collectResolverFailoverStream(chunks <-chan ai.StreamChunk) (content, finishReason string, err error) {
	var text strings.Builder
	for chunk := range chunks {
		text.WriteString(chunk.Content)
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
		if chunk.Error != nil {
			return text.String(), finishReason, chunk.Error
		}
	}
	return text.String(), finishReason, nil
}
