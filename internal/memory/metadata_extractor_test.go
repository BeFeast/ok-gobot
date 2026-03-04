package memory

import (
	"context"
	"testing"

	"ok-gobot/internal/ai"
)

func TestLLMMetadataExtractorExtract(t *testing.T) {
	extractor := NewLLMMetadataExtractor(&stubAIClient{
		response: `{"people":["Anton"," anton "],"topics":["Release"],"action_items":["Send notes"],"type":"Decision"}`,
	})

	metadata, err := extractor.Extract(context.Background(), "Anton decided we should send notes")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if len(metadata.People) != 1 || metadata.People[0] != "Anton" {
		t.Fatalf("unexpected people metadata: %+v", metadata.People)
	}
	if metadata.Type != "decision" {
		t.Fatalf("expected normalized type=decision, got %q", metadata.Type)
	}
}

func TestLLMMetadataExtractorExtractRejectsInvalidJSON(t *testing.T) {
	extractor := NewLLMMetadataExtractor(&stubAIClient{response: "not-json"})

	_, err := extractor.Extract(context.Background(), "text")
	if err == nil {
		t.Fatal("expected extract to fail for invalid JSON response")
	}
}

type stubAIClient struct {
	response string
	err      error
}

func (s *stubAIClient) Complete(ctx context.Context, messages []ai.Message) (string, error) {
	return s.response, s.err
}

func (s *stubAIClient) CompleteWithTools(ctx context.Context, messages []ai.ChatMessage, tools []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	return nil, nil
}
