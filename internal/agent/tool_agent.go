package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/tools"
)

// ToolCallingAgent handles AI requests with tool invocation
type ToolCallingAgent struct {
	aiClient    ai.Client
	tools       *tools.Registry
	personality *Personality
}

// NewToolCallingAgent creates a new agent
func NewToolCallingAgent(aiClient ai.Client, toolRegistry *tools.Registry, personality *Personality) *ToolCallingAgent {
	return &ToolCallingAgent{
		aiClient:    aiClient,
		tools:       toolRegistry,
		personality: personality,
	}
}

// ProcessRequest handles a user request, potentially invoking tools
func (a *ToolCallingAgent) ProcessRequest(ctx context.Context, userMessage string, session string) (*AgentResponse, error) {
	// Build system prompt with available tools
	systemPrompt := a.buildSystemPrompt()

	// Prepare messages
	messages := []ai.Message{
		{Role: "system", Content: systemPrompt},
	}

	if session != "" {
		messages = append(messages, ai.Message{Role: "assistant", Content: session})
	}

	messages = append(messages, ai.Message{Role: "user", Content: userMessage})

	// Get initial response
	response, err := a.aiClient.Complete(ctx, messages)
	if err != nil {
		return nil, err
	}

	// Check if response contains a tool call
	toolCall := a.parseToolCall(response)
	if toolCall == nil {
		// No tool call, return direct response
		return &AgentResponse{
			Message:  response,
			ToolUsed: false,
		}, nil
	}

	// Execute tool
	toolResult, err := a.executeTool(ctx, toolCall)
	if err != nil {
		return &AgentResponse{
			Message:  fmt.Sprintf("❌ Tool execution failed: %v", err),
			ToolUsed: true,
			ToolName: toolCall.Name,
		}, nil
	}

	// Get final response with tool result
	finalMessages := append(messages,
		ai.Message{Role: "assistant", Content: fmt.Sprintf("I'll help you with that. Let me use the %s tool.", toolCall.Name)},
		ai.Message{Role: "system", Content: fmt.Sprintf("Tool %s returned: %s", toolCall.Name, toolResult)},
	)

	finalResponse, err := a.aiClient.Complete(ctx, finalMessages)
	if err != nil {
		return &AgentResponse{
			Message:    toolResult, // Return raw tool result if AI fails
			ToolUsed:   true,
			ToolName:   toolCall.Name,
			ToolResult: toolResult,
		}, nil
	}

	return &AgentResponse{
		Message:    finalResponse,
		ToolUsed:   true,
		ToolName:   toolCall.Name,
		ToolResult: toolResult,
	}, nil
}

// AgentResponse represents the agent's response
type AgentResponse struct {
	Message    string
	ToolUsed   bool
	ToolName   string
	ToolResult string
}

// ToolCall represents a tool invocation
type ToolCall struct {
	Name string                 `json:"tool"`
	Args map[string]interface{} `json:"args"`
}

// buildSystemPrompt creates the system prompt with tool descriptions
func (a *ToolCallingAgent) buildSystemPrompt() string {
	var prompt strings.Builder

	prompt.WriteString(a.personality.GetSystemPrompt())
	prompt.WriteString("\n\nYou have access to the following tools:\n\n")

	for _, tool := range a.tools.List() {
		prompt.WriteString(fmt.Sprintf("Tool: %s\n", tool.Name()))
		prompt.WriteString(fmt.Sprintf("Description: %s\n\n", tool.Description()))
	}

	prompt.WriteString(`\nWhen you need to use a tool, respond with a JSON object in this format:
{"tool": "tool_name", "args": {"arg1": "value1", "arg2": "value2"}}

For example:
- To search the web: {"tool": "search", "args": {"query": "best phones 2024"}}
- To use browser: {"tool": "browser", "args": {"action": "navigate", "url": "https://example.com"}}
- To read a file: {"tool": "file", "args": {"action": "read", "path": "notes.txt"}}

If no tool is needed, respond normally.`)

	return prompt.String()
}

// parseToolCall extracts tool call from AI response
func (a *ToolCallingAgent) parseToolCall(response string) *ToolCall {
	// Look for JSON in the response
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")

	if start == -1 || end == -1 || end <= start {
		return nil
	}

	jsonStr := response[start : end+1]

	var toolCall ToolCall
	if err := json.Unmarshal([]byte(jsonStr), &toolCall); err != nil {
		return nil
	}

	// Validate tool exists
	if _, ok := a.tools.Get(toolCall.Name); !ok {
		return nil
	}

	return &toolCall
}

// executeTool runs the specified tool
func (a *ToolCallingAgent) executeTool(ctx context.Context, toolCall *ToolCall) (string, error) {
	tool, ok := a.tools.Get(toolCall.Name)
	if !ok {
		return "", fmt.Errorf("tool not found: %s", toolCall.Name)
	}

	// Convert args map to string slice
	var args []string
	for key, value := range toolCall.Args {
		args = append(args, key)
		args = append(args, fmt.Sprintf("%v", value))
	}

	return tool.Execute(ctx, args...)
}

// GetAvailableTools returns a list of available tool names and descriptions
func (a *ToolCallingAgent) GetAvailableTools() []string {
	var list []string
	for _, tool := range a.tools.List() {
		list = append(list, fmt.Sprintf("• %s: %s", tool.Name(), tool.Description()))
	}
	return list
}
