package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const chatGPTCompletedOnlyTextEvent = `data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","model":"gpt-test","output":[{"id":"msg_test","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Weather is clear."}]}]}}` + "\n\ndata: [DONE]\n\n"

const chatGPTDeltaAndCompletedTextEvent = `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Weather is clear."}` + "\n\n" +
	`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","model":"gpt-test","output":[{"id":"msg_test","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Weather is clear."}]}]}}` + "\n\ndata: [DONE]\n\n"

const chatGPTCompletedOnlyToolCallEvent = `data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","model":"gpt-test","output":[{"id":"fc_test","type":"function_call","status":"completed","call_id":"call_test","name":"web_search","arguments":"{\"query\":\"Netanya weather\"}"}]}}` + "\n\ndata: [DONE]\n\n"

const chatGPTIncrementalToolCallWithEmptyCompletedOutputEvent = `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_test","type":"function_call","status":"in_progress","call_id":"call_test","name":"web_search","arguments":""}}` + "\n\n" +
	`data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_test","delta":"{\"query\":\"Netanya"}` + "\n\n" +
	`data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_test","delta":" weather\"}"}` + "\n\n" +
	`data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_test","arguments":"{\"query\":\"Netanya weather\"}"}` + "\n\n" +
	`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_test","type":"function_call","status":"completed","call_id":"call_test","name":"web_search","arguments":"{\"query\":\"Netanya weather\"}"}}` + "\n\n" +
	`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","model":"gpt-test","output":[]}}` + "\n\ndata: [DONE]\n\n"

func TestChatGPTCompletedOnlyTextIsNotDropped(t *testing.T) {
	client := newChatGPTSSETestClient(t, chatGPTCompletedOnlyTextEvent)
	ctx := context.Background()

	complete, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "weather"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if complete != "Weather is clear." {
		t.Fatalf("Complete = %q", complete)
	}

	withTools, err := client.CompleteWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)
	if err != nil {
		t.Fatalf("CompleteWithTools: %v", err)
	}
	if got := withTools.Choices[0].Message.Content; got != "Weather is clear." {
		t.Fatalf("CompleteWithTools content = %q", got)
	}

	if got := collectChatGPTStream(t, client.CompleteStream(ctx, []Message{{Role: RoleUser, Content: "weather"}})); got != "Weather is clear." {
		t.Fatalf("CompleteStream content = %q", got)
	}
	if got := collectChatGPTStream(t, client.CompleteStreamWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)); got != "Weather is clear." {
		t.Fatalf("CompleteStreamWithTools content = %q", got)
	}
}

func TestChatGPTHTTPFailureClassifiesByStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewChatGPTClient(ProviderConfig{
		Name:    "chatgpt",
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "gpt-test",
	})
	checks := []struct {
		name string
		call func() error
	}{
		{
			name: "complete",
			call: func() error {
				_, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "hello"}})
				return err
			},
		},
		{
			name: "complete with tools",
			call: func() error {
				_, err := client.CompleteWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "hello"}}, nil)
				return err
			},
		},
		{
			name: "complete stream",
			call: func() error {
				_, _, err := drainChatGPTStream(client.CompleteStream(context.Background(), []Message{{Role: RoleUser, Content: "hello"}}))
				return err
			},
		},
		{
			name: "complete stream with tools",
			call: func() error {
				_, _, err := drainChatGPTStream(client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "hello"}}, nil))
				return err
			},
		},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			err := check.call()
			if err == nil {
				t.Fatal("request unexpectedly succeeded")
			}
			var httpErr *BackendHTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("errors.As(%v, BackendHTTPError) = %#v", err, httpErr)
			}
			if got := ClassifyBackendError(err); got != BackendFailureUnavailable {
				t.Fatalf("ClassifyBackendError(%v) = %s, want %s", err, got, BackendFailureUnavailable)
			}
		})
	}
}

func TestChatGPTCompletedTextDoesNotDuplicateStreamedDeltas(t *testing.T) {
	client := newChatGPTSSETestClient(t, chatGPTDeltaAndCompletedTextEvent)
	ctx := context.Background()

	withTools, err := client.CompleteWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)
	if err != nil {
		t.Fatalf("CompleteWithTools: %v", err)
	}
	if got := withTools.Choices[0].Message.Content; got != "Weather is clear." {
		t.Fatalf("CompleteWithTools content = %q", got)
	}

	if got := collectChatGPTStream(t, client.CompleteStreamWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)); got != "Weather is clear." {
		t.Fatalf("CompleteStreamWithTools content = %q", got)
	}
}

func TestChatGPTCompletedOnlyToolCallIsNotDropped(t *testing.T) {
	client := newChatGPTSSETestClient(t, chatGPTCompletedOnlyToolCallEvent)
	ctx := context.Background()

	withTools, err := client.CompleteWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)
	if err != nil {
		t.Fatalf("CompleteWithTools: %v", err)
	}
	assertChatGPTToolCall(t, withTools.Choices[0].Message.ToolCalls)

	streamed := collectChatGPTStream(t, client.CompleteStreamWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil))
	const marker = "\n__TOOL_CALLS__:"
	if !strings.HasPrefix(streamed, marker) {
		t.Fatalf("streamed tool call = %q", streamed)
	}
	var toolCalls []ToolCall
	if err := json.Unmarshal([]byte(strings.TrimPrefix(streamed, marker)), &toolCalls); err != nil {
		t.Fatalf("decode streamed tool calls: %v", err)
	}
	assertChatGPTToolCall(t, toolCalls)
}

func TestChatGPTIncrementalToolCallSurvivesEmptyCompletedOutput(t *testing.T) {
	client := newChatGPTSSETestClient(t, chatGPTIncrementalToolCallWithEmptyCompletedOutputEvent)

	withTools, err := client.CompleteWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)
	if err != nil {
		t.Fatalf("CompleteWithTools: %v", err)
	}
	assertChatGPTToolCall(t, withTools.Choices[0].Message.ToolCalls)
}

func TestChatGPTCompleteWithToolsRejectsTerminalFailureEvents(t *testing.T) {
	tests := []struct {
		name       string
		stream     string
		wantErrSub string
	}{
		{
			name:       "failed",
			stream:     `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"upstream failed"}}}` + "\n\ndata: [DONE]\n\n",
			wantErrSub: "response failed: server_error: upstream failed",
		},
		{
			name:       "incomplete",
			stream:     `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\ndata: [DONE]\n\n",
			wantErrSub: "response incomplete: max_output_tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newChatGPTSSETestClient(t, tt.stream)
			_, err := client.CompleteWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("CompleteWithTools error = %v, want substring %q", err, tt.wantErrSub)
			}
		})
	}
}

func TestChatGPTCompleteWithToolsRejectsEmptyTerminalResponse(t *testing.T) {
	stream := `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","status":"completed"}]}}` + "\n\ndata: [DONE]\n\n"
	client := newChatGPTSSETestClient(t, stream)

	_, err := client.CompleteWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "no usable text or tool calls") {
		t.Fatalf("CompleteWithTools error = %v", err)
	}
}

func TestChatGPTCompleteWithToolsReportsScannerError(t *testing.T) {
	client := newChatGPTSSETestClient(t, "data: "+strings.Repeat("x", 1024*1024+1)+"\n\n")

	_, err := client.CompleteWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "stream read error") {
		t.Fatalf("CompleteWithTools error = %v", err)
	}
}

func newChatGPTSSETestClient(t *testing.T, stream string) *ChatGPTClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, stream)
	}))
	t.Cleanup(server.Close)
	return NewChatGPTClient(ProviderConfig{
		Name:    "chatgpt",
		APIKey:  "static-test-key",
		BaseURL: server.URL,
		Model:   "gpt-test",
	})
}

func collectChatGPTStream(t *testing.T, chunks <-chan StreamChunk) string {
	t.Helper()
	var content strings.Builder
	done := false
	for chunk := range chunks {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		content.WriteString(chunk.Content)
		if chunk.Done {
			done = true
		}
	}
	if !done {
		t.Fatal("stream ended without a done chunk")
	}
	return content.String()
}

func assertChatGPTToolCall(t *testing.T, toolCalls []ToolCall) {
	t.Helper()
	if len(toolCalls) != 1 {
		t.Fatalf("tool calls = %+v", toolCalls)
	}
	toolCall := toolCalls[0]
	if toolCall.ID != "call_test" || toolCall.Function.Name != "web_search" || toolCall.Function.Arguments != `{"query":"Netanya weather"}` {
		t.Fatalf("tool call = %+v", toolCall)
	}
}

// --- streaming parser parity with CompleteWithTools ---
//
// The buffered parser has rejected terminal-failure events, reasoning-only
// completions and truncated streams since b668b81. The streaming parser — the
// one the Telegram path actually uses, because it wires OnDelta — did not, and
// turned every one of those into an empty answer with a nil error. These tests
// pin the parity.

// drainChatGPTStream collects a stream without failing on errors, so tests can
// assert on the terminal condition itself.
func drainChatGPTStream(chunks <-chan StreamChunk) (content string, finishReason string, err error) {
	var b strings.Builder
	for chunk := range chunks {
		if chunk.Error != nil {
			err = chunk.Error
			continue
		}
		b.WriteString(chunk.Content)
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
	}
	return b.String(), finishReason, err
}

func TestChatGPTStreamWithToolsSurfacesTerminalEvents(t *testing.T) {
	tests := []struct {
		name       string
		stream     string
		wantErrSub string
	}{
		{
			name:       "failed",
			stream:     `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"upstream failed"}}}` + "\n\ndata: [DONE]\n\n",
			wantErrSub: "response failed: server_error: upstream failed",
		},
		{
			name:       "incomplete",
			stream:     `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\ndata: [DONE]\n\n",
			wantErrSub: "response incomplete: max_output_tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newChatGPTSSETestClient(t, tt.stream)
			_, _, err := drainChatGPTStream(client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil))
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("stream error = %v, want substring %q", err, tt.wantErrSub)
			}
		})
	}
}

func TestChatGPTStreamWithToolsRejectsEmptyCompletedTurn(t *testing.T) {
	stream := `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","status":"completed"}]}}` + "\n\ndata: [DONE]\n\n"
	client := newChatGPTSSETestClient(t, stream)

	content, _, err := drainChatGPTStream(client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil))
	if err == nil || !strings.Contains(err.Error(), "no usable text or tool calls") {
		t.Fatalf("stream error = %v, content = %q", err, content)
	}
}

// The production incident: the SSE stream ends without response.completed. The
// old parser closed the channel having emitted nothing, which the agent loop
// could not tell apart from a model that chose to answer with silence.
func TestChatGPTStreamWithToolsReportsStreamEndedEarly(t *testing.T) {
	stream := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_test","type":"reasoning","status":"in_progress"}}` + "\n\n"
	client := newChatGPTSSETestClient(t, stream)

	content, _, err := drainChatGPTStream(client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil))
	if err == nil || !strings.Contains(err.Error(), "ended without a completed response") {
		t.Fatalf("stream error = %v, content = %q", err, content)
	}
}

// A stream that dies after real text must keep the text — replacing a partial
// answer with an error would lose work the user already saw.
func TestChatGPTStreamWithToolsKeepsPartialTextOnTruncation(t *testing.T) {
	stream := `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Half an ans"}` + "\n\n"
	client := newChatGPTSSETestClient(t, stream)

	content, finishReason, err := drainChatGPTStream(client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "Half an ans" {
		t.Fatalf("content = %q, want the partial text preserved", content)
	}
	if finishReason != "incomplete" {
		t.Fatalf("finishReason = %q, want %q", finishReason, "incomplete")
	}
}

// Parity with TestChatGPTIncrementalToolCallSurvivesEmptyCompletedOutput: the
// streaming parser ignored the .done events, so a tool call that only arrives
// that way was dropped.
func TestChatGPTStreamWithToolsRecoversToolCallFromDoneEvents(t *testing.T) {
	// Arguments arrive ONLY in response.output_item.done — no argument deltas —
	// so a parser that ignores the .done events reconstructs an empty call.
	stream := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_test","type":"function_call","status":"in_progress","call_id":"call_test","name":"web_search","arguments":""}}` + "\n\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_test","type":"function_call","status":"completed","call_id":"call_test","name":"web_search","arguments":"{\"query\":\"Netanya weather\"}"}}` + "\n\n" +
		`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","model":"gpt-test","output":[]}}` + "\n\ndata: [DONE]\n\n"
	client := newChatGPTSSETestClient(t, stream)

	content, _, err := drainChatGPTStream(client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	idx := strings.Index(content, "\n__TOOL_CALLS__:")
	if idx < 0 {
		t.Fatalf("no tool-call marker in %q", content)
	}
	var toolCalls []ToolCall
	if err := json.Unmarshal([]byte(content[idx+len("\n__TOOL_CALLS__:"):]), &toolCalls); err != nil {
		t.Fatalf("tool calls did not parse: %v", err)
	}
	if len(toolCalls) != 1 || toolCalls[0].Function.Name != "web_search" {
		t.Fatalf("tool calls = %+v", toolCalls)
	}
	if toolCalls[0].Function.Arguments != `{"query":"Netanya weather"}` {
		t.Fatalf("arguments = %q", toolCalls[0].Function.Arguments)
	}
}

func TestChatGPTCompleteStreamSurfacesTerminalEvents(t *testing.T) {
	stream := `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"upstream failed"}}}` + "\n\ndata: [DONE]\n\n"
	client := newChatGPTSSETestClient(t, stream)

	_, _, err := drainChatGPTStream(client.CompleteStream(context.Background(), []Message{{Role: RoleUser, Content: "weather"}}))
	if err == nil || !strings.Contains(err.Error(), "response failed: server_error: upstream failed") {
		t.Fatalf("stream error = %v", err)
	}
}

func TestChatGPTCompleteStreamReportsStreamEndedEarly(t *testing.T) {
	client := newChatGPTSSETestClient(t, "data: {\"type\":\"response.created\"}\n\n")

	_, _, err := drainChatGPTStream(client.CompleteStream(context.Background(), []Message{{Role: RoleUser, Content: "weather"}}))
	if err == nil || !strings.Contains(err.Error(), "ended without a completed response") {
		t.Fatalf("stream error = %v", err)
	}
}

func TestChatGPTStreamWithToolsSurfacesBareErrorEvent(t *testing.T) {
	stream := `data: {"type":"error","code":"rate_limit_exceeded","message":"slow down"}` + "\n\n"
	client := newChatGPTSSETestClient(t, stream)

	_, _, err := drainChatGPTStream(client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil))
	if err == nil || !strings.Contains(err.Error(), "rate_limit_exceeded: slow down") {
		t.Fatalf("stream error = %v", err)
	}
}

// The error for a stream that produced nothing has to carry enough to tell the
// three ways of producing nothing apart — otherwise a recurrence is as
// undiagnosable as the first one was.
func TestChatGPTStreamEndedEarlyErrorCarriesATrace(t *testing.T) {
	stream := `data: {"type":"response.created"}` + "\n\n" +
		`data: {"type":"response.in_progress"}` + "\n\n"
	client := newChatGPTSSETestClient(t, stream)

	_, _, err := drainChatGPTStream(client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"events=2", "unhandled=2", `last="response.in_progress"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}
