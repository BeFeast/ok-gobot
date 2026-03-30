package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// IsHermesModel
// ---------------------------------------------------------------------------

func TestIsHermesModel(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  bool
	}{
		{"hermes3 exact", "hermes3", true},
		{"hermes3 uppercase", "Hermes3", true},
		{"nous-hermes-2", "nous-hermes-2-mistral-7b", true},
		{"openhermes", "openhermes-2.5-mistral-7b", true},
		{"hermes mixed case", "NousResearch/Hermes-3-Llama-3.1-8B", true},
		{"gpt-4o not hermes", "gpt-4o", false},
		{"ollama llama3", "llama3", false},
		{"empty string", "", false},
		{"mistral", "mistral-7b-instruct", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsHermesModel(tc.model)
			if got != tc.want {
				t.Errorf("IsHermesModel(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParseHermesToolCalls
// ---------------------------------------------------------------------------

func TestParseHermesToolCalls_NoTags(t *testing.T) {
	content := "This is just plain text with no tool calls."
	cleanContent, toolCalls := ParseHermesToolCalls(content)
	if cleanContent != content {
		t.Errorf("content changed: got %q, want %q", cleanContent, content)
	}
	if len(toolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(toolCalls))
	}
}

func TestParseHermesToolCalls_SingleToolCall(t *testing.T) {
	content := `I'll search for that.
<tool_call>
{"name": "web_search", "arguments": {"query": "golang testing"}}
</tool_call>`

	cleanContent, toolCalls := ParseHermesToolCalls(content)

	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}

	tc := toolCalls[0]
	if tc.Function.Name != "web_search" {
		t.Errorf("name = %q, want %q", tc.Function.Name, "web_search")
	}
	if tc.Type != "function" {
		t.Errorf("type = %q, want %q", tc.Type, "function")
	}
	if tc.ID == "" {
		t.Error("expected non-empty ID")
	}

	// Arguments should be valid JSON
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Errorf("arguments is not valid JSON: %v", err)
	}
	if args["query"] != "golang testing" {
		t.Errorf("query = %v, want %q", args["query"], "golang testing")
	}

	// Content should not contain the <tool_call> block
	if contains(cleanContent, "<tool_call>") {
		t.Error("cleaned content still contains <tool_call> tag")
	}
}

func TestParseHermesToolCalls_MultipleToolCalls(t *testing.T) {
	content := `<tool_call>
{"name": "memory_search", "arguments": {"query": "user preferences"}}
</tool_call>
<tool_call>
{"name": "web_search", "arguments": {"query": "weather today"}}
</tool_call>`

	_, toolCalls := ParseHermesToolCalls(content)

	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolCalls))
	}
	if toolCalls[0].Function.Name != "memory_search" {
		t.Errorf("first tool name = %q", toolCalls[0].Function.Name)
	}
	if toolCalls[1].Function.Name != "web_search" {
		t.Errorf("second tool name = %q", toolCalls[1].Function.Name)
	}
	// IDs should be distinct
	if toolCalls[0].ID == toolCalls[1].ID {
		t.Error("tool call IDs should be distinct")
	}
}

func TestParseHermesToolCalls_EmptyArguments(t *testing.T) {
	content := `<tool_call>
{"name": "get_time", "arguments": {}}
</tool_call>`

	_, toolCalls := ParseHermesToolCalls(content)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Function.Arguments != "{}" {
		t.Errorf("arguments = %q, want %q", toolCalls[0].Function.Arguments, "{}")
	}
}

func TestParseHermesToolCalls_NullArguments(t *testing.T) {
	content := `<tool_call>
{"name": "ping", "arguments": null}
</tool_call>`

	_, toolCalls := ParseHermesToolCalls(content)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Function.Arguments != "{}" {
		t.Errorf("null arguments should normalise to {}, got %q", toolCalls[0].Function.Arguments)
	}
}

func TestParseHermesToolCalls_InvalidJSON_Kept(t *testing.T) {
	// An invalid JSON block should be kept in the content unchanged.
	content := `<tool_call>not valid json</tool_call>`
	cleanContent, toolCalls := ParseHermesToolCalls(content)
	if len(toolCalls) != 0 {
		t.Errorf("expected no tool calls for invalid JSON, got %d", len(toolCalls))
	}
	if !contains(cleanContent, "not valid json") {
		t.Error("invalid block should remain in content")
	}
}

func TestParseHermesToolCalls_TextBeforeAndAfter(t *testing.T) {
	content := "Sure, let me help.\n<tool_call>\n{\"name\": \"calc\", \"arguments\": {\"expr\": \"1+1\"}}\n</tool_call>\nDone!"
	cleanContent, toolCalls := ParseHermesToolCalls(content)

	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if !contains(cleanContent, "Sure, let me help.") {
		t.Error("text before tag should be preserved")
	}
	if !contains(cleanContent, "Done!") {
		t.Error("text after tag should be preserved")
	}
	if contains(cleanContent, "<tool_call>") {
		t.Error("cleaned content should not contain <tool_call> tag")
	}
}

// ---------------------------------------------------------------------------
// NewClient auto-detection
// ---------------------------------------------------------------------------

func TestNewClient_HermesModelReturnsHermesClient(t *testing.T) {
	client, err := NewClientWithDroid(ProviderConfig{
		Name:    "ollama",
		BaseURL: "http://localhost:11434/v1",
		Model:   "hermes3",
	}, DroidConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := client.(*HermesClient); !ok {
		t.Errorf("expected *HermesClient for hermes3 model, got %T", client)
	}
}

func TestNewClient_NonHermesModelReturnsOpenAIClient(t *testing.T) {
	client, err := NewClientWithDroid(ProviderConfig{
		Name:    "ollama",
		BaseURL: "http://localhost:11434/v1",
		Model:   "llama3",
	}, DroidConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := client.(*OpenAICompatibleClient); !ok {
		t.Errorf("expected *OpenAICompatibleClient for llama3 model, got %T", client)
	}
}

func TestNewClient_HermesVariants(t *testing.T) {
	hermesModels := []string{
		"hermes3",
		"nous-hermes-2-mistral-7b",
		"NousResearch/Hermes-3-Llama-3.1-8B",
		"openhermes-2.5-mistral-7b",
	}
	for _, model := range hermesModels {
		t.Run(model, func(t *testing.T) {
			client, err := NewClientWithDroid(ProviderConfig{
				Name:    "ollama",
				BaseURL: "http://localhost:11434/v1",
				Model:   model,
			}, DroidConfig{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if _, ok := client.(*HermesClient); !ok {
				t.Errorf("expected *HermesClient for %q, got %T", model, client)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HermesClient.CompleteWithTools — integration via httptest
// ---------------------------------------------------------------------------

// hermesToolCallResponse returns an OpenAI-compatible JSON response where the
// tool call is embedded in the content (Hermes format) rather than tool_calls.
func hermesToolCallResponse(content string) string {
	resp := ChatCompletionResponse{
		Model: "hermes3",
		Choices: []struct {
			Index        int         `json:"index"`
			Message      ChatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		}{
			{
				Index:        0,
				Message:      ChatMessage{Role: "assistant", Content: content},
				FinishReason: "stop",
			},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func TestHermesClient_CompleteWithTools_ExtractsToolCalls(t *testing.T) {
	serverContent := fmt.Sprintf(
		"I'll search for that.\n<tool_call>\n{\"name\": \"web_search\", \"arguments\": {\"query\": \"go generics\"}}\n</tool_call>",
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, hermesToolCallResponse(serverContent))
	}))
	defer srv.Close()

	client := newHermesClient(ProviderConfig{
		Name:    "ollama",
		BaseURL: srv.URL,
		Model:   "hermes3",
	})

	resp, err := client.CompleteWithTools(context.Background(), []ChatMessage{
		{Role: "user", Content: "search for go generics"},
	}, []ToolDefinition{
		{Type: "function", Function: FunctionDefinition{Name: "web_search", Description: "Search the web"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}

	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "web_search" {
		t.Errorf("tool name = %q, want %q", msg.ToolCalls[0].Function.Name, "web_search")
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want %q", resp.Choices[0].FinishReason, "tool_calls")
	}
	// Content should be cleaned
	if contains(msg.Content, "<tool_call>") {
		t.Error("content should not contain <tool_call> tag after extraction")
	}
}

func TestHermesClient_CompleteWithTools_PlainResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, hermesToolCallResponse("Here is the answer."))
	}))
	defer srv.Close()

	client := newHermesClient(ProviderConfig{
		Name:    "ollama",
		BaseURL: srv.URL,
		Model:   "hermes3",
	})

	resp, err := client.CompleteWithTools(context.Background(), []ChatMessage{
		{Role: "user", Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) != 0 {
		t.Errorf("expected no tool calls for plain response, got %d", len(msg.ToolCalls))
	}
	if msg.Content != "Here is the answer." {
		t.Errorf("content = %q, want %q", msg.Content, "Here is the answer.")
	}
}

// TestHermesClient_ImplementsStreamingClient checks the interface at compile time.
func TestHermesClient_ImplementsStreamingClient(t *testing.T) {
	var _ StreamingClient = (*HermesClient)(nil)
}
