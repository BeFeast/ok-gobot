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
	// Build system prompt
	systemPrompt := a.buildSystemPrompt()

	// Prepare messages
	messages := []ai.ChatMessage{
		{Role: ai.RoleSystem, Content: systemPrompt},
	}

	if session != "" {
		messages = append(messages, ai.ChatMessage{Role: ai.RoleAssistant, Content: session})
	}

	messages = append(messages, ai.ChatMessage{Role: ai.RoleUser, Content: userMessage})

	// Get tool definitions
	toolDefinitions := tools.ToOpenAITools(a.tools.List())

	// Maximum iterations to prevent infinite loops
	maxIterations := 10
	var finalResponse string
	var usedTools []string
	var toolResults []string

	for iteration := 0; iteration < maxIterations; iteration++ {
		// Try native tool calling first
		response, err := a.aiClient.CompleteWithTools(ctx, messages, toolDefinitions)

		if err != nil {
			// Fallback to legacy text-based tool calling
			return a.processLegacyToolCall(ctx, messages)
		}

		if len(response.Choices) == 0 {
			return nil, fmt.Errorf("no response from model")
		}

		choice := response.Choices[0]
		message := choice.Message

		// Check if model wants to call tools
		if len(message.ToolCalls) > 0 {
			// Execute all tool calls (parallel execution)
			for _, toolCall := range message.ToolCalls {
				if toolCall.Type != "function" {
					continue
				}

				functionName := toolCall.Function.Name
				arguments := toolCall.Function.Arguments

				// Execute tool
				result, err := a.executeToolFromJSON(ctx, functionName, arguments)
				if err != nil {
					result = fmt.Sprintf("Error executing tool: %v", err)
				}

				// Add assistant message with tool call
				messages = append(messages, ai.ChatMessage{
					Role:      ai.RoleAssistant,
					ToolCalls: []ai.ToolCall{toolCall},
				})

				// Add tool result
				messages = append(messages, ai.ChatMessage{
					Role:       ai.RoleTool,
					Content:    result,
					ToolCallID: toolCall.ID,
					Name:       functionName,
				})

				usedTools = append(usedTools, functionName)
				toolResults = append(toolResults, result)
			}

			// Continue the loop to get the final response
			continue
		}

		// No more tool calls, we have the final response
		finalResponse = message.Content
		break
	}

	if finalResponse == "" {
		finalResponse = "I've completed the requested actions."
	}

	return &AgentResponse{
		Message:    finalResponse,
		ToolUsed:   len(usedTools) > 0,
		ToolName:   strings.Join(usedTools, ", "),
		ToolResult: strings.Join(toolResults, "\n\n"),
	}, nil
}

// processLegacyToolCall handles the old text-based tool calling format as fallback
func (a *ToolCallingAgent) processLegacyToolCall(ctx context.Context, messages []ai.ChatMessage) (*AgentResponse, error) {
	// Convert ChatMessage to legacy Message format
	legacyMessages := make([]ai.Message, len(messages))
	for i, msg := range messages {
		legacyMessages[i] = ai.Message{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	// Get initial response
	response, err := a.aiClient.Complete(ctx, legacyMessages)
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
	finalMessages := append(legacyMessages,
		ai.Message{Role: ai.RoleAssistant, Content: fmt.Sprintf("I'll help you with that. Let me use the %s tool.", toolCall.Name)},
		ai.Message{Role: ai.RoleSystem, Content: fmt.Sprintf("Tool %s returned: %s", toolCall.Name, toolResult)},
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

// ToolCall represents a tool invocation (legacy format)
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

	prompt.WriteString("\nUse the native function calling capability when you need to use tools.\n")
	prompt.WriteString("The system will automatically handle tool execution and return results to you.\n")

	return prompt.String()
}

// parseToolCall extracts tool call from AI response (legacy fallback)
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

// executeToolFromJSON executes a tool with JSON arguments
func (a *ToolCallingAgent) executeToolFromJSON(ctx context.Context, toolName string, argsJSON string) (string, error) {
	tool, ok := a.tools.Get(toolName)
	if !ok {
		return "", fmt.Errorf("tool not found: %s", toolName)
	}

	// Parse arguments
	var argsMap map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &argsMap); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	// Convert args map to string slice
	var args []string

	// Handle simple "input" parameter (default schema)
	if input, ok := argsMap["input"].(string); ok {
		args = []string{input}
	} else {
		// Handle complex parameters
		for key, value := range argsMap {
			args = append(args, key)
			args = append(args, fmt.Sprintf("%v", value))
		}
	}

	return tool.Execute(ctx, args...)
}

// executeTool runs the specified tool (legacy format)
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

// GetTools returns the tool registry
func (a *ToolCallingAgent) GetTools() *tools.Registry {
	return a.tools
}
