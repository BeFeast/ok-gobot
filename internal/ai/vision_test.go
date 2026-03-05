package ai

import (
	"context"
	"testing"
)

type visionStubClient struct {
	vision bool
}

func (c *visionStubClient) Complete(_ context.Context, _ []Message) (string, error) {
	return "", nil
}

func (c *visionStubClient) CompleteWithTools(_ context.Context, _ []ChatMessage, _ []ToolDefinition) (*ChatCompletionResponse, error) {
	return &ChatCompletionResponse{}, nil
}

func (c *visionStubClient) SupportsVision() bool {
	return c.vision
}

func TestSupportsVision(t *testing.T) {
	t.Parallel()

	if SupportsVision(nil) {
		t.Fatal("expected nil client to not support vision")
	}
	if !SupportsVision(&visionStubClient{vision: true}) {
		t.Fatal("expected explicit vision-capable client to support vision")
	}
	if SupportsVision(&visionStubClient{vision: false}) {
		t.Fatal("expected explicit non-vision client to not support vision")
	}
}

func TestFailoverClientSupportsVision(t *testing.T) {
	t.Parallel()

	allVision := &FailoverClient{
		entries: []failoverEntry{
			{model: "m1", client: &visionStubClient{vision: true}},
			{model: "m2", client: &visionStubClient{vision: true}},
		},
	}
	if !allVision.SupportsVision() {
		t.Fatal("expected failover client with all vision entries to support vision")
	}

	mixed := &FailoverClient{
		entries: []failoverEntry{
			{model: "m1", client: &visionStubClient{vision: true}},
			{model: "m2", client: &visionStubClient{vision: false}},
		},
	}
	if mixed.SupportsVision() {
		t.Fatal("expected failover client with non-vision fallback to disable vision")
	}
}
