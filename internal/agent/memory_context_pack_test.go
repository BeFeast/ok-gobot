package agent

import (
	"context"
	"strings"
	"testing"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/tools"
)

func TestToolCallingAgentAddsMemoryContextPackToSystemPrompt(t *testing.T) {
	aiClient := &captureMemoryContextAI{}
	agent := NewToolCallingAgent(aiClient, tools.NewRegistry(), &Personality{
		Files: map[string]string{"IDENTITY.md": "Name: Test Bot"},
	})
	agent.SetMemoryContextBuilder(
		memory.NewContextPackBuilder(&agentMemorySearcher{results: []memory.MemoryResult{
			{
				SourceFile:   "MEMORY.md",
				HeaderPath:   "Projects > OK Gobot",
				ChunkOrdinal: 4,
				Content:      "Runtime prompts should use a cited memory context pack.",
				Similarity:   0.93,
			},
		}}),
		memory.ContextPackScope{SessionKey: "dm:1", ChatID: 1, AgentName: "default", Surface: "runtime"},
		memory.ContextPackBudget{MaxChars: 1200, MaxItems: 2},
	)

	resp, err := agent.ProcessRequest(context.Background(), "what did we decide about memory context?", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}
	if resp.MemoryContext == nil || !resp.MemoryContext.HasContent() {
		t.Fatalf("expected response memory context, got %+v", resp.MemoryContext)
	}
	if len(aiClient.messages) == 0 {
		t.Fatal("expected captured model messages")
	}

	systemPrompt := aiClient.messages[0].Content
	for _, want := range []string{"## Memory Context Pack", "Source: MEMORY.md", "Header: Projects > OK Gobot", "score: 0.93"} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("expected %q in system prompt:\n%s", want, systemPrompt)
		}
	}
	if !strings.Contains(resp.MemoryContext.SourceSummary(), "MEMORY.md") {
		t.Fatalf("expected MEMORY.md in source summary, got %q", resp.MemoryContext.SourceSummary())
	}
}

type captureMemoryContextAI struct {
	messages []ai.ChatMessage
}

func (c *captureMemoryContextAI) Complete(_ context.Context, _ []ai.Message) (string, error) {
	return "ok", nil
}

func (c *captureMemoryContextAI) CompleteWithTools(_ context.Context, messages []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	c.messages = append([]ai.ChatMessage(nil), messages...)
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{
			{
				Message:      ai.ChatMessage{Role: ai.RoleAssistant, Content: "ok"},
				FinishReason: "stop",
			},
		},
	}, nil
}

type agentMemorySearcher struct {
	results []memory.MemoryResult
}

func (s *agentMemorySearcher) Search(_ context.Context, _ string, _ int) ([]memory.MemoryResult, error) {
	return s.results, nil
}
