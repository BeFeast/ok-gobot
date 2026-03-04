package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/logger"
	"ok-gobot/internal/tools"
)

// ToolEventType constants for tool lifecycle events
const (
	ToolEventStarted  = "started"
	ToolEventFinished = "finished"
)

// ToolEvent represents a tool lifecycle event fired during ProcessRequest
type ToolEvent struct {
	ToolName string
	Type     string // ToolEventStarted or ToolEventFinished
	Err      error  // non-nil if Type is ToolEventFinished and tool failed
}

// ToolCallingAgent handles AI requests with tool invocation
type ToolCallingAgent struct {
	aiClient     ai.Client
	tools        *tools.Registry
	personality  *Personality
	modelAliases map[string]string
	ThinkLevel   string // "off", "low", "medium", "high" — controls extended thinking
	PromptMode   string // "full", "minimal", "none" — controls system prompt verbosity
	onToolEvent  func(event ToolEvent)
	onDelta      func(delta string) // fired for each streamed text token
	onDeltaReset func()             // fired when tool calls follow streaming text (content discarded)
}

// SetToolEventCallback sets a callback that fires on tool lifecycle events.
// It is called with ToolEventStarted before execution and ToolEventFinished after.
func (a *ToolCallingAgent) SetToolEventCallback(cb func(event ToolEvent)) {
	a.onToolEvent = cb
}

// SetDeltaCallback sets a callback that fires for each streamed text token.
// When the AI client supports streaming, tokens are emitted in real time.
// For non-streaming clients the callback is not called.
func (a *ToolCallingAgent) SetDeltaCallback(cb func(delta string)) {
	a.onDelta = cb
}

// SetDeltaResetCallback sets a callback fired when the model returns tool calls
// after emitting some streamed text. The caller should discard any accumulated
// streaming content because tool calls will be executed next.
func (a *ToolCallingAgent) SetDeltaResetCallback(cb func()) {
	a.onDeltaReset = cb
}

// NewToolCallingAgent creates a new agent
func NewToolCallingAgent(aiClient ai.Client, toolRegistry *tools.Registry, personality *Personality) *ToolCallingAgent {
	return &ToolCallingAgent{
		aiClient:    aiClient,
		tools:       toolRegistry,
		personality: personality,
		PromptMode:  "full",
	}
}

// SetThinkLevel sets the thinking/reasoning level for the agent
func (a *ToolCallingAgent) SetThinkLevel(level string) {
	a.ThinkLevel = level
}

// SetPromptMode sets the prompt verbosity mode ("full", "minimal", "none")
func (a *ToolCallingAgent) SetPromptMode(mode string) {
	a.PromptMode = mode
}

// SetModelAliases sets the model alias map for system prompt generation.
func (a *ToolCallingAgent) SetModelAliases(aliases map[string]string) {
	a.modelAliases = aliases
}

// ProcessRequest handles a user request, potentially invoking tools
func (a *ToolCallingAgent) ProcessRequest(ctx context.Context, userMessage string, session string) (*AgentResponse, error) {
	logger.Debugf("ToolAgent: processing request, message len=%d", len(userMessage))

	// Build system prompt
	systemPrompt := a.buildSystemPrompt()
	logger.Debugf("ToolAgent: system prompt len=%d", len(systemPrompt))
	logger.Tracef("ToolAgent: system prompt: %.2000s", systemPrompt)

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
	var lastPromptTokens, totalCompletionTokens, lastTotalTokens int

	// Resolve streaming client once so we don't re-type-assert on every iteration.
	streamClient, hasStreaming := a.aiClient.(ai.StreamingClient)

	for iteration := 0; iteration < maxIterations; iteration++ {
		logger.Debugf("ToolAgent: iteration %d/%d", iteration+1, maxIterations)
		// Use streaming when a delta callback is wired and the client supports it.
		var (
			response *ai.ChatCompletionResponse
			err      error
		)
		if a.onDelta != nil && hasStreaming {
			response, err = a.processWithStreamingClient(ctx, streamClient, messages, toolDefinitions)
		} else {
			response, err = a.aiClient.CompleteWithTools(ctx, messages, toolDefinitions)
		}

		if err != nil {
			logger.Warnf("ToolAgent: CompleteWithTools failed on iteration %d: %v", iteration+1, err)
			// If we already executed tools, return collected results instead of fallback
			if len(toolResults) > 0 {
				summary := strings.Join(toolResults, "\n\n")
				if finalResponse == "" {
					finalResponse = "⚠️ Tool executed but model failed to analyze results:\n\n" + summary
				}
				return &AgentResponse{
					Message:          finalResponse,
					ToolUsed:         true,
					ToolName:         strings.Join(usedTools, ", "),
					ToolResult:       summary,
					PromptTokens:     lastPromptTokens,
					CompletionTokens: totalCompletionTokens,
					TotalTokens:      lastTotalTokens,
				}, nil
			}
			// First iteration — fallback to legacy
			return a.processLegacyToolCall(ctx, messages)
		}

		// Track token usage
		if response.Usage != nil {
			lastPromptTokens = response.Usage.PromptTokens
			totalCompletionTokens += response.Usage.CompletionTokens
			lastTotalTokens = response.Usage.TotalTokens
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
				logger.Debugf("ToolAgent: calling tool %s args_len=%d", functionName, len(arguments))
				logger.Tracef("ToolAgent: tool %s raw args: %s", functionName, arguments)

				// Fire started event
				if a.onToolEvent != nil {
					a.onToolEvent(ToolEvent{ToolName: functionName, Type: ToolEventStarted})
				}

				// Execute tool
				result, err := a.executeToolFromJSON(ctx, functionName, arguments)
				if err != nil {
					logger.Debugf("ToolAgent: tool %s error: %v", functionName, err)
					result = fmt.Sprintf("Error executing tool: %v", err)
				}

				// Fire finished event
				if a.onToolEvent != nil {
					a.onToolEvent(ToolEvent{ToolName: functionName, Type: ToolEventFinished, Err: err})
				}
				logger.Tracef("ToolAgent: tool %s result (%d chars): %.500s", functionName, len(result), result)

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
		logger.Tracef("ToolAgent: final response (%d chars): %.500s", len(finalResponse), finalResponse)
		break
	}

	if finalResponse == "" {
		finalResponse = "I've completed the requested actions."
	}

	return &AgentResponse{
		Message:          finalResponse,
		ToolUsed:         len(usedTools) > 0,
		ToolName:         strings.Join(usedTools, ", "),
		ToolResult:       strings.Join(toolResults, "\n\n"),
		PromptTokens:     lastPromptTokens,
		CompletionTokens: totalCompletionTokens,
		TotalTokens:      lastTotalTokens,
	}, nil
}

// processWithStreamingClient executes one AI round-trip using the streaming API.
// Text content deltas are forwarded to onDelta as they arrive.
// If the model returns tool calls, onDeltaReset is called (if set) to signal that
// any accumulated streaming text should be discarded, and the tool calls are returned
// in the response so the main loop can execute them.
func (a *ToolCallingAgent) processWithStreamingClient(
	ctx context.Context,
	streamClient ai.StreamingClient,
	messages []ai.ChatMessage,
	toolDefs []ai.ToolDefinition,
) (*ai.ChatCompletionResponse, error) {
	ch := streamClient.CompleteStreamWithTools(ctx, messages, toolDefs)

	const toolCallMarker = "\n__TOOL_CALLS__:"
	var contentBuilder strings.Builder
	var toolCallsJSON string

	for chunk := range ch {
		if chunk.Error != nil {
			// Drain remaining chunks so the goroutine can exit.
			go func() {
				for range ch {
				}
			}()
			return nil, chunk.Error
		}

		content := chunk.Content

		// Detect the tool-calls marker embedded in the content.
		if idx := strings.Index(content, toolCallMarker); idx >= 0 {
			// Emit any text that precedes the marker.
			if idx > 0 {
				prefix := content[:idx]
				contentBuilder.WriteString(prefix)
				if a.onDelta != nil {
					a.onDelta(prefix)
				}
			}
			toolCallsJSON = content[idx+len(toolCallMarker):]
			// Drain remaining chunks to allow the goroutine to exit cleanly.
			go func() {
				for range ch {
				}
			}()
			break
		}

		if content != "" {
			contentBuilder.WriteString(content)
			if a.onDelta != nil {
				a.onDelta(content)
			}
		}

		if chunk.Done {
			break
		}
	}

	finalContent := contentBuilder.String()

	// Parse tool calls from the marker payload.
	var toolCalls []ai.ToolCall
	if toolCallsJSON != "" {
		if err := json.Unmarshal([]byte(toolCallsJSON), &toolCalls); err != nil {
			logger.Warnf("ToolAgent: failed to parse streaming tool calls: %v", err)
		}
		// When tool calls follow streamed text, signal the caller to discard the text.
		if len(toolCalls) > 0 && a.onDeltaReset != nil {
			a.onDeltaReset()
		}
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{{
			Index: 0,
			Message: ai.ChatMessage{
				Role:      ai.RoleAssistant,
				Content:   finalContent,
				ToolCalls: toolCalls,
			},
			FinishReason: finishReason,
		}},
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
	Message          string
	ToolUsed         bool
	ToolName         string
	ToolResult       string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ToolCall represents a tool invocation (legacy format)
type ToolCall struct {
	Name string                 `json:"tool"`
	Args map[string]interface{} `json:"args"`
}

// buildSystemPrompt creates the system prompt with tool descriptions
func (a *ToolCallingAgent) buildSystemPrompt() string {
	return bootstrap.BuildPrompt(a.personality.Loader(), a.tools, bootstrap.PromptOptions{
		Mode:         a.PromptMode,
		ThinkLevel:   a.ThinkLevel,
		ModelAliases: a.modelAliases,
	})
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

// JSONExecutor is implemented by tools that accept structured JSON params
// directly, bypassing positional arg conversion.
type JSONExecutor interface {
	ExecuteJSON(ctx context.Context, params map[string]string) (string, error)
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

	// If the tool supports structured JSON params, use that path directly.
	// This preserves all named params (e.g. snapshot_id, ref) without loss.
	if je, ok := tool.(JSONExecutor); ok {
		strParams := make(map[string]string, len(argsMap))
		for k, v := range argsMap {
			strParams[k] = fmt.Sprintf("%v", v)
		}
		return je.ExecuteJSON(ctx, strParams)
	}

	// Convert args map to string slice
	var args []string

	// Handle simple "input" parameter (default schema)
	if input, ok := argsMap["input"].(string); ok {
		args = []string{input}
		// Append optional extra params (e.g. grep: input + path)
		for _, key := range []string{"path", "directory"} {
			if v, ok := argsMap[key].(string); ok {
				args = append(args, v)
			}
		}
	} else if to, ok := argsMap["to"].(string); ok {
		// Message-style tool with "to" + "text" fields
		args = []string{to}
		if text, ok := argsMap["text"].(string); ok {
			args = append(args, text)
		}
	} else if cmd, ok := argsMap["command"].(string); ok {
		// Structured tool with "command" field (e.g. browser, file)
		args = []string{cmd}
		// Append known positional params in order
		for _, key := range []string{"url", "path", "snapshot_id", "ref", "selector", "value", "query", "content", "expression", "task"} {
			if v, ok := argsMap[key]; ok {
				if rendered := stringifyToolArg(v); rendered != "" {
					args = append(args, rendered)
				}
			}
		}

		// Append common optional flags used by structured tools.
		for _, key := range []string{"category", "limit", "person"} {
			if v, ok := argsMap[key]; ok {
				if rendered := stringifyToolArg(v); rendered != "" {
					args = append(args, fmt.Sprintf("--%s=%s", key, rendered))
				}
			}
		}

		// Preserve nested filter objects as JSON for tools that support it.
		if filter, ok := argsMap["filter"]; ok {
			if raw, err := json.Marshal(filter); err == nil && string(raw) != "null" {
				args = append(args, "--filter="+string(raw))
			}
		}

		// `forget`-style commands expect ID as positional argument.
		if id, ok := argsMap["id"]; ok {
			if rendered := stringifyToolArg(id); rendered != "" {
				args = append(args, rendered)
			}
		}
	} else if op, ok := argsMap["operation"].(string); ok {
		// Structured tool with "operation" field
		args = []string{op}
		for _, key := range []string{"path", "content", "value"} {
			if v, ok := argsMap[key].(string); ok {
				args = append(args, v)
			}
		}
	} else if query, ok := argsMap["query"].(string); ok {
		// Structured tools with query + optional numeric/string limit (e.g. memory_search)
		args = []string{query}
		if limitRaw, ok := argsMap["limit"]; ok {
			switch limit := limitRaw.(type) {
			case float64:
				if limit > 0 {
					args = append(args, strconv.Itoa(int(limit)))
				}
			case string:
				if strings.TrimSpace(limit) != "" {
					args = append(args, strings.TrimSpace(limit))
				}
			}
		}
	} else if source, ok := argsMap["source"].(string); ok {
		// Structured tools with source + optional header path (e.g. memory_get)
		args = []string{source}
		if headerPath, ok := argsMap["header_path"].(string); ok && strings.TrimSpace(headerPath) != "" {
			args = append(args, strings.TrimSpace(headerPath))
		}
	} else {
		// Fallback: pass values only (skip keys)
		for _, value := range argsMap {
			args = append(args, fmt.Sprintf("%v", value))
		}
	}

	return tool.Execute(ctx, args...)
}

func stringifyToolArg(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	default:
		return fmt.Sprintf("%v", v)
	}
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
