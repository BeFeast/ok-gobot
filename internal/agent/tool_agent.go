package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/logger"
	"ok-gobot/internal/tools"
)

// ToolCallingAgent handles AI requests with tool invocation
type ToolCallingAgent struct {
	aiClient     ai.Client
	tools        *tools.Registry
	personality  *Personality
	modelAliases map[string]string
	ThinkLevel   string // "off", "low", "medium", "high" — controls extended thinking
	PromptMode   string // "full", "minimal", "none" — controls system prompt verbosity
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

	for iteration := 0; iteration < maxIterations; iteration++ {
		logger.Debugf("ToolAgent: iteration %d/%d", iteration+1, maxIterations)
		// Try native tool calling first
		response, err := a.aiClient.CompleteWithTools(ctx, messages, toolDefinitions)

		if err != nil {
			// Fallback to legacy text-based tool calling
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
		Message:          finalResponse,
		ToolUsed:         len(usedTools) > 0,
		ToolName:         strings.Join(usedTools, ", "),
		ToolResult:       strings.Join(toolResults, "\n\n"),
		PromptTokens:     lastPromptTokens,
		CompletionTokens: totalCompletionTokens,
		TotalTokens:      lastTotalTokens,
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
	var prompt strings.Builder

	mode := a.PromptMode
	if mode == "" {
		mode = "full"
	}

	// Personality section depends on prompt mode
	switch mode {
	case "none":
		prompt.WriteString(a.personality.GetIdentityLine())
		prompt.WriteString("\n\n")
	case "minimal":
		prompt.WriteString(a.personality.GetMinimalSystemPrompt())
	default: // "full"
		prompt.WriteString(a.personality.GetSystemPrompt())

		// Add skills section if skills are available (full mode only)
		skillsSummary := a.personality.GetSkillsSummary()
		if skillsSummary != "" {
			prompt.WriteString("\n## Skills\n\n")
			prompt.WriteString("Before replying: scan the available skills below.\n")
			prompt.WriteString("- If exactly one skill clearly applies: read its SKILL.md with the `file` tool, then follow it.\n")
			prompt.WriteString("- If multiple could apply: choose the most specific one, then read/follow it.\n")
			prompt.WriteString("- If none clearly apply: do not read any SKILL.md.\n\n")
			prompt.WriteString("Available skills:\n")
			prompt.WriteString(skillsSummary)
			prompt.WriteString("\n")
		}
	}

	// Tools list (always included)
	prompt.WriteString("\nYou have access to the following tools:\n\n")
	for _, tool := range a.tools.List() {
		prompt.WriteString(fmt.Sprintf("Tool: %s\n", tool.Name()))
		prompt.WriteString(fmt.Sprintf("Description: %s\n\n", tool.Description()))
	}

	// Full mode: include all guidance sections
	if mode == "full" {
		prompt.WriteString("\n## Tool Usage Guidelines\n\n")
		prompt.WriteString("You are running on the user's computer with REAL access to all listed tools.\n")
		prompt.WriteString("You CAN and SHOULD use tools to fulfill requests. Never say you \"can't\" do something if a tool exists for it.\n")
		prompt.WriteString("Use the native function calling capability when you need to use tools.\n")
		prompt.WriteString("The system will automatically handle tool execution and return results to you.\n\n")
		prompt.WriteString("## Tool Call Style\n\n")
		prompt.WriteString("Default: do not narrate routine, low-risk tool calls — just call the tool.\n")
		prompt.WriteString("Narrate only when it helps: multi-step work, complex problems, sensitive actions, or when user explicitly asks.\n\n")

		prompt.WriteString("## Silent Replies\n\n")
		prompt.WriteString("If you have nothing meaningful to add (e.g. heartbeat poll with no issues, acknowledgment-only situations), reply with exactly: SILENT_REPLY\n")
		prompt.WriteString("The system will suppress this and send nothing to the user.\n\n")

		// Memory guidance (only if memory tool available)
		if _, hasMemory := a.tools.Get("memory"); hasMemory {
			prompt.WriteString("## Memory\n\n")
			prompt.WriteString("Before answering anything about prior work, decisions, dates, people, preferences, or todos:\n")
			prompt.WriteString("search memory first using the memory tool, then use the results to inform your answer.\n\n")
		}

		prompt.WriteString("## Reply Tags\n\n")
		prompt.WriteString("To reply to the user's message natively (as a Telegram reply): include [[reply_to_current]] anywhere in your response.\n")
		prompt.WriteString("To reply to a specific message: include [[reply_to:<message_id>]]. Tags are stripped from the final message.\n\n")

		prompt.WriteString("## Reactions\n\n")
		prompt.WriteString("You can react to the user's message with an emoji by including [[react:emoji]] in your response (e.g. [[react:👍]] or [[react:😂]]).\n")
		prompt.WriteString("Use reactions sparingly — only when truly relevant (at most 1 reaction per 5-10 exchanges). The tag is stripped from the final message.\n\n")

		// Model aliases section
		if len(a.modelAliases) > 0 {
			prompt.WriteString("## Model Aliases\n")
			prompt.WriteString("Prefer aliases when discussing model overrides with the user:\n")
			for alias, fullName := range a.modelAliases {
				prompt.WriteString(fmt.Sprintf("  %s → %s\n", alias, fullName))
			}
			prompt.WriteString("\n")
		}
	}

	// Reasoning section (all modes, when thinking is enabled)
	if a.ThinkLevel != "" && a.ThinkLevel != "off" {
		prompt.WriteString("\n## Reasoning\n\n")
		prompt.WriteString("When solving complex problems, use structured thinking:\n")
		prompt.WriteString("<think>\n[your reasoning process here]\n</think>\n")
		prompt.WriteString("Then provide your final answer directly.\n\n")
	}

	// Runtime info (always included)
	prompt.WriteString(fmt.Sprintf("Runtime: os=%s arch=%s date=%s\n",
		runtime.GOOS, runtime.GOARCH, time.Now().Format("2006-01-02")))

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
