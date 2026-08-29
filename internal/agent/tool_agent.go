package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/delegation"
	"ok-gobot/internal/logger"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/tools"
)

// DefaultToolTimeout defines when a long-running tool call is moved into a
// subagent so the main conversation can stay responsive. Tests may override it.
var DefaultToolTimeout = 20 * time.Second

// ToolEventType constants for tool lifecycle events
const (
	ToolEventStarted  = "started"
	ToolEventFinished = "finished"
)

// ToolEvent represents a tool lifecycle event fired during ProcessRequest
type ToolEvent struct {
	ToolName   string
	Type       string            // ToolEventStarted or ToolEventFinished
	Input      string            // raw JSON arguments (populated on Started)
	Output     string            // truncated result text for display (populated on Finished)
	FullOutput string            // untruncated result text for internal consumers only
	Err        error             // non-nil if Type is ToolEventFinished and tool failed
	Denial     *tools.ToolDenial // non-nil when the tool was blocked by policy
}

// ToolTimeoutSpawnFunc is called when a tool execution exceeds ToolTimeout.
// It receives the tool name and raw JSON arguments and returns a user-visible
// notification string.
type ToolTimeoutSpawnFunc func(toolName, argsJSON string) string

// ToolCallingAgent handles AI requests with tool invocation
type ToolCallingAgent struct {
	aiClient       ai.Client
	tools          *tools.Registry
	personality    *Personality
	modelAliases   map[string]string
	ThinkLevel     string      // "off", "low", "medium", "high" — controls extended thinking
	PromptMode     string      // "full", "minimal", "none" — controls system prompt verbosity
	MemoryMode     string      // "eager" (default), "retrieval_first", or "startup_recent" — controls daily-note injection
	MaxToolCalls   int         // max number of tool executions allowed for this run (0 = default/unlimited)
	iterationLimit int         // test override for the model-loop cap; 0 = iterationBudget(MaxToolCalls)
	contextMode    ContextMode // chat vs job context assembly strategy
	model          string      // model name for token budget calculation
	onToolEvent    func(event ToolEvent)
	onDelta        func(delta string) // fired for each streamed text token
	onDeltaReset   func()             // fired when tool calls follow streaming text (content discarded)
	ToolTimeout    time.Duration      // max duration for a single tool call before auto-spawn (0 = no limit)
	onToolTimeout  ToolTimeoutSpawnFunc
	hookRunner     *HookRunner // lifecycle hook executor (nil = no hooks)
	reflector      *Reflector  // optional; when set, tool failures trigger async reflection

	memoryContextBuilder *memory.ContextPackBuilder
	memoryContextScope   memory.ContextPackScope
	memoryContextBudget  memory.ContextPackBudget
	memoryPolicy         *memory.RecallPolicy
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

// SetToolTimeoutCallback sets a callback that fires when a tool call exceeds
// ToolTimeout.
func (a *ToolCallingAgent) SetToolTimeoutCallback(timeout time.Duration, cb ToolTimeoutSpawnFunc) {
	a.ToolTimeout = timeout
	a.onToolTimeout = cb
}

// SetHookRunner attaches a HookRunner that fires deterministic lifecycle hooks
// at SessionStart, PreToolUse, PostToolUse, and SessionEnd.
// Pass nil to disable hooks.
func (a *ToolCallingAgent) SetHookRunner(hr *HookRunner) {
	a.hookRunner = hr
}

// SetReflector attaches a Reflector that analyses tool failures asynchronously.
// Reflection never blocks the main response flow.
func (a *ToolCallingAgent) SetReflector(r *Reflector) {
	a.reflector = r
}

// SetMemoryContextBuilder attaches active memory recall for prompt assembly.
func (a *ToolCallingAgent) SetMemoryContextBuilder(builder *memory.ContextPackBuilder, scope memory.ContextPackScope, budget memory.ContextPackBudget) {
	a.memoryContextBuilder = builder
	a.memoryContextScope = scope
	a.memoryContextBudget = budget
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

// SetMemoryMode sets the memory prompt mode. Recognized values:
// "eager" (default), "retrieval_first", "startup_recent". Invalid or empty
// values fall back to eager during prompt assembly.
func (a *ToolCallingAgent) SetMemoryMode(mode string) {
	a.MemoryMode = mode
}

// SetMaxToolCalls sets the per-run tool-call budget.
func (a *ToolCallingAgent) SetMaxToolCalls(limit int) {
	a.MaxToolCalls = limit
}

// SetContextMode sets the context assembly strategy (chat vs job).
func (a *ToolCallingAgent) SetContextMode(mode ContextMode) {
	a.contextMode = mode
}

// SetModel sets the model name used for token budget calculation.
func (a *ToolCallingAgent) SetModel(model string) {
	a.model = model
}

// SetModelAliases sets the model alias map for system prompt generation.
func (a *ToolCallingAgent) SetModelAliases(aliases map[string]string) {
	a.modelAliases = aliases
}

// SetMemoryRecallPolicy attaches the scoped memory policy for this run.
func (a *ToolCallingAgent) SetMemoryRecallPolicy(policy *memory.RecallPolicy) {
	a.memoryPolicy = policy
}

// ProcessRequest handles a user request, potentially invoking tools
func (a *ToolCallingAgent) ProcessRequest(ctx context.Context, userMessage string, session string) (*AgentResponse, error) {
	return a.ProcessRequestWithContent(ctx, userMessage, nil, session, nil)
}

// ProcessRequestWithHistory handles a user request with full conversation history.
func (a *ToolCallingAgent) ProcessRequestWithHistory(ctx context.Context, userMessage string, session string, history []ai.ChatMessage) (*AgentResponse, error) {
	return a.ProcessRequestWithContent(ctx, userMessage, nil, session, history)
}

// ProcessRequestWithContent handles a user request with optional multimodal user blocks.
func (a *ToolCallingAgent) ProcessRequestWithContent(
	ctx context.Context,
	userMessage string,
	userContent []ai.ContentBlock,
	session string,
	history []ai.ChatMessage,
) (*AgentResponse, error) {
	logger.Debugf(
		"ToolAgent: processing request, message len=%d, blocks=%d, history=%d",
		len(userMessage),
		len(userContent),
		len(history),
	)

	// Fire SessionStart hook before any LLM work begins.
	a.hookRunner.RunSessionStart(userMessage)

	// Build system prompt
	systemPrompt := a.buildSystemPrompt()
	memoryPack := a.buildMemoryContextPack(ctx, userMessage)
	if memoryPack != nil && memoryPack.HasContent() {
		systemPrompt = appendMemoryContextPack(systemPrompt, memoryPack)
	}
	logger.Debugf("ToolAgent: system prompt len=%d", len(systemPrompt))
	logger.Tracef("ToolAgent: system prompt: %.2000s", systemPrompt)

	// Prepare messages using mode-appropriate context assembly.
	// Legacy session string is promoted to a single-message history when
	// no v2 transcript is available.
	hist := history
	if len(hist) == 0 && session != "" {
		hist = []ai.ChatMessage{{Role: ai.RoleAssistant, Content: session}}
	}

	userMsg := ai.ChatMessage{Role: ai.RoleUser, Content: userMessage}
	if len(userContent) > 0 && ai.SupportsVision(a.aiClient) {
		userMsg.ContentBlocks = userContent
	}

	messages := AssembleContext(a.contextMode, systemPrompt, hist, userMsg, a.model)

	// Get tool definitions
	toolDefinitions := tools.ToOpenAITools(a.tools.List())

	// The model loop is a second budget besides MaxToolCalls. A browser_task
	// job advertises 150 calls / 10 minutes; a hardcoded 50-iteration cap
	// made that dead config. Measured 2026-08-30 00:02 IDT: the laptop
	// search worker extracted live seller pages and then died on this cap
	// after 50 browser calls / 5.4 minutes, with ~4.5 minutes of clock left.
	maxToolCalls := a.MaxToolCalls
	maxIterations := a.iterationLimit
	if maxIterations <= 0 {
		maxIterations = iterationBudget(maxToolCalls)
	}
	var finalResponse string
	var usedTools []string
	var toolResults []string
	var lastPromptTokens, totalCompletionTokens, lastTotalTokens int
	var terminalFinishReason string
	completed := false
	incomplete := false
	toolCallsUsed := 0
	emptyFinalRetries := 0

	// Fire SessionEnd hook when the function returns, capturing the final state.
	defer func() {
		a.hookRunner.RunSessionEnd(finalResponse, usedTools)
	}()

	// Resolve streaming client once so we don't re-type-assert on every iteration.
	streamClient, hasStreaming := a.aiClient.(ai.StreamingClient)

iterationLoop:
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
					IsFallback:       true,
					MemoryContext:    memoryPack,
				}, nil
			}
			// First iteration — fallback to legacy
			legacyResp, legacyErr := a.processLegacyToolCall(ctx, messages)
			if legacyResp != nil {
				legacyResp.MemoryContext = memoryPack
			}
			return legacyResp, legacyErr
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

		// An incomplete stream has already emitted whatever partial text is safe
		// to show. Stop here: retrying through the legacy client would replace or
		// append to that text, and an incomplete tool-call payload must never run.
		if choice.FinishReason == "incomplete" {
			terminalFinishReason = choice.FinishReason
			finalResponse = strings.TrimSpace(StripThinkTags(message.Content))
			incomplete = true
			logger.Warnf("ToolAgent: model stream ended incomplete after %d chars", len(finalResponse))
			break
		}

		// Check if model wants to call tools
		if len(message.ToolCalls) > 0 {
			// Execute all tool calls (parallel execution)
			for _, toolCall := range message.ToolCalls {
				if toolCall.Type != "function" {
					continue
				}
				if maxToolCalls > 0 && toolCallsUsed >= maxToolCalls {
					finalResponse = fmt.Sprintf("⚠️ Reached tool-call budget (%d/%d). Task not finished.\n\n%s", toolCallsUsed, maxToolCalls, truncateToolSummary(toolResults, maxInlinedToolSummary))
					completed = false
					break iterationLoop
				}

				functionName := toolCall.Function.Name
				arguments := toolCall.Function.Arguments
				logger.Debugf("ToolAgent: calling tool %s args_len=%d", functionName, len(arguments))
				logger.Tracef("ToolAgent: tool %s raw args: %s", functionName, arguments)

				// Fire started event
				if a.onToolEvent != nil {
					a.onToolEvent(ToolEvent{ToolName: functionName, Type: ToolEventStarted, Input: arguments})
				}

				// Fire PreToolUse lifecycle hook.
				a.hookRunner.RunPreToolUse(functionName, arguments)

				// Execute tool with optional timeout-triggered subagent spawn.
				result, err := a.executeToolWithTimeout(ctx, functionName, arguments)

				// Check for structured denial (estop / policy block).
				var denial *tools.ToolDenial
				if err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					if d, ok := tools.IsToolDenial(err); ok {
						denial = d
						result = d.FormatPlain()
					} else {
						logger.Debugf("ToolAgent: tool %s error: %v", functionName, err)
						if strings.TrimSpace(result) == "" {
							result = fmt.Sprintf("Error executing tool: %v", err)
						}
						// Trigger async reflection on real tool failures (not denials).
						if a.reflector != nil {
							a.reflector.RecordFailureAsync(functionName, arguments, err)
						}
					}
				}

				// Fire finished event
				if a.onToolEvent != nil {
					out := result
					if len(out) > 300 {
						out = out[:300] + "…"
					}
					a.onToolEvent(ToolEvent{ToolName: functionName, Type: ToolEventFinished, Output: out, FullOutput: result, Err: err, Denial: denial})
				}

				// Fire PostToolUse lifecycle hook.
				a.hookRunner.RunPostToolUse(functionName, arguments, result, err)

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

				toolCallsUsed++
				usedTools = append(usedTools, functionName)
				toolResults = append(toolResults, result)
			}

			// Continue the loop to get the final response
			continue
		}

		// No more tool calls, we have the final response
		finalResponse = strings.TrimSpace(StripThinkTags(message.Content))
		terminalFinishReason = choice.FinishReason
		logger.Tracef("ToolAgent: final response (%d chars): %.500s", len(finalResponse), finalResponse)

		// An empty final turn on top of completed tool work throws that work away.
		// Ask once more before giving up — bounded by a run-scoped counter, so a
		// model that keeps returning nothing cannot spin the loop.
		if finalResponse == "" && len(toolResults) > 0 && emptyFinalRetries < maxEmptyFinalRetries {
			emptyFinalRetries++
			logger.Warnf("ToolAgent: empty final turn after %d tool calls, asking once more", toolCallsUsed)
			messages = append(messages, ai.ChatMessage{
				Role:    ai.RoleUser,
				Content: emptyFinalRetryPrompt,
			})
			continue
		}

		completed = true
		break
	}

	// A worker that only emitted tool calls until the loop cap still holds
	// the extracted pages. Ask once for a written answer before we give up.
	if finalResponse == "" && len(toolResults) > 0 && !completed && !incomplete && ctx.Err() == nil {
		messages = append(messages, ai.ChatMessage{
			Role:    ai.RoleUser,
			Content: iterationBudgetRetryPrompt,
		})
		var (
			response *ai.ChatCompletionResponse
			synthErr error
		)
		if a.onDelta != nil && hasStreaming {
			response, synthErr = a.processWithStreamingClient(ctx, streamClient, messages, toolDefinitions)
		} else {
			response, synthErr = a.aiClient.CompleteWithTools(ctx, messages, toolDefinitions)
		}
		if synthErr == nil && response != nil && len(response.Choices) > 0 {
			choice := response.Choices[0]
			if choice.FinishReason != "incomplete" && len(choice.Message.ToolCalls) == 0 {
				text := strings.TrimSpace(StripThinkTags(choice.Message.Content))
				if text != "" {
					finalResponse = text
					completed = true
					terminalFinishReason = choice.FinishReason
				}
			}
		}
	}

	// Only flag budget_exceeded when the limit actually interrupted execution.
	// If the model used exactly maxToolCalls tools and then gave a normal final
	// response (completed == true), the run succeeded — it was not stopped by
	// the budget.
	budgetHit := maxToolCalls > 0 && toolCallsUsed >= maxToolCalls && !completed

	// Build a BudgetExceededError when the tool-call limit was reached so that
	// callers (especially the durable job runner) can distinguish budget stops
	// from normal completions.
	var budgetErr error
	if budgetHit {
		budgetErr = &delegation.BudgetExceededError{
			Reason: delegation.LimitToolCalls,
			Report: delegation.RunReport{
				Status:        "budget_exceeded",
				LimitReason:   delegation.LimitToolCalls,
				ToolCallsUsed: toolCallsUsed,
				ToolCallMax:   maxToolCalls,
				Summary:       fmt.Sprintf("Reached tool-call budget (%d/%d)", toolCallsUsed, maxToolCalls),
			},
		}
	}

	if finalResponse == "" {
		switch {
		case budgetHit:
			finalResponse = fmt.Sprintf("⚠️ Reached tool-call budget (%d/%d). Task not finished.\n\n%s", toolCallsUsed, maxToolCalls, truncateToolSummary(toolResults, maxInlinedToolSummary))
		case len(toolResults) > 0 && !completed:
			finalResponse = fmt.Sprintf("⚠️ Reached iteration limit (%d). Task not finished. Here is what the tools already returned:\n\n%s", maxIterations, truncateToolSummary(toolResults, maxInlinedToolSummary))
		case len(toolResults) > 0:
			// The run holds real work — often tens of kilobytes of fetched content.
			// Handing back an apology alone discards it and leaves the user with
			// nothing to act on, so surface the results the way the round-trip
			// error branch above already does.
			finalResponse = "⚠️ I ran the tools but could not turn the results into an answer. Here is what they returned:\n\n" +
				truncateToolSummary(toolResults, maxInlinedToolSummary)
		default:
			finalResponse = "⚠️ I couldn't generate a response (empty model output). Please retry."
		}
		return &AgentResponse{
			Message:          finalResponse,
			ToolUsed:         len(usedTools) > 0,
			ToolName:         strings.Join(usedTools, ", "),
			ToolResult:       strings.Join(toolResults, "\n\n"),
			PromptTokens:     lastPromptTokens,
			CompletionTokens: totalCompletionTokens,
			TotalTokens:      lastTotalTokens,
			IsFallback:       true,
			FinishReason:     terminalFinishReason,
			BudgetExceeded:   budgetHit,
			ToolCallsUsed:    toolCallsUsed,
			MemoryContext:    memoryPack,
		}, budgetErr
	}

	return &AgentResponse{
		Message:          finalResponse,
		ToolUsed:         len(usedTools) > 0,
		ToolName:         strings.Join(usedTools, ", "),
		ToolResult:       strings.Join(toolResults, "\n\n"),
		PromptTokens:     lastPromptTokens,
		CompletionTokens: totalCompletionTokens,
		TotalTokens:      lastTotalTokens,
		IsFallback:       incomplete,
		FinishReason:     terminalFinishReason,
		BudgetExceeded:   budgetHit,
		ToolCallsUsed:    toolCallsUsed,
		MemoryContext:    memoryPack,
	}, budgetErr
}

// maxEmptyFinalRetries bounds the second chance given to a model that returns an
// empty final turn while tool results are already in hand.
const maxEmptyFinalRetries = 1

// emptyFinalRetryPrompt nudges the model to convert the tool output it already
// has into an answer, without re-running any tools.
const emptyFinalRetryPrompt = "Your last turn produced no text. Write the answer for the user now, using the tool results above. Do not call any more tools."

// defaultMaxIterations is the runaway guard when MaxToolCalls is unset.
const defaultMaxIterations = 50

// iterationBudgetRetryPrompt is the one extra turn after the model-loop cap.
// The worker already has the page text; this asks it to write that down
// instead of discarding the run as an empty fallback.
const iterationBudgetRetryPrompt = "You have used the iteration budget. Write the answer now from the tool results above. Return the extracted findings even if incomplete. Do not call any more tools."

// iterationBudget is the model-loop cap. Each iteration is one model
// round-trip and can execute one or more tools. When a job sets MaxToolCalls,
// the loop must be able to spend that budget and still have one turn left
// for the final text response.
func iterationBudget(maxToolCalls int) int {
	if maxToolCalls > 0 && maxToolCalls+1 > defaultMaxIterations {
		return maxToolCalls + 1
	}
	return defaultMaxIterations
}

// maxInlinedToolSummary caps raw tool output inlined into a fallback reply so a
// failed run cannot flood the chat with tens of kilobytes of fetched pages.
const maxInlinedToolSummary = 4000

// truncateToolSummary joins tool results and caps them at max runes, marking the
// cut so the reader knows the output continued.
func truncateToolSummary(results []string, max int) string {
	joined := strings.Join(results, "\n\n")
	if runes := []rune(joined); len(runes) > max {
		return strings.TrimSpace(string(runes[:max])) + "\n\n[…tool output truncated]"
	}
	return joined
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
	var terminalFinishReason string
	chunksSeen := 0

	for chunk := range ch {
		chunksSeen++
		if chunk.FinishReason != "" {
			terminalFinishReason = chunk.FinishReason
		}
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

	// A client that closes its channel without emitting anything has told us
	// nothing — but synthesizing a response here would present it to the caller
	// as a model that deliberately answered with silence. Every in-tree client
	// is expected to emit at least one chunk or an error; hold them to it.
	if chunksSeen == 0 {
		return nil, errors.New("streaming client closed without emitting any chunk")
	}

	finalContent := StripThinkTags(contentBuilder.String())

	// Parse tool calls from the marker payload.
	var toolCalls []ai.ToolCall
	if toolCallsJSON != "" {
		if err := json.Unmarshal([]byte(toolCallsJSON), &toolCalls); err != nil {
			logger.Warnf("ToolAgent: failed to parse streaming tool calls: %v", err)
		}
		// When tool calls follow streamed text, signal the caller to discard the text.
		if len(toolCalls) > 0 && terminalFinishReason != "incomplete" && a.onDeltaReset != nil {
			a.onDeltaReset()
		}
	}

	finishReason := terminalFinishReason
	if finishReason == "incomplete" {
		// Keep the terminal state even if the truncated stream contained a
		// parseable tool marker; ProcessRequest must not execute that payload.
	} else if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	} else if finishReason == "" {
		finishReason = "stop"
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
	IsFallback       bool // true when the response is a synthetic fallback, not model-generated
	FinishReason     string
	BudgetExceeded   bool // true when the run was stopped because a budget limit was hit
	ToolCallsUsed    int  // number of tool calls consumed during this run
	MemoryContext    *memory.ContextPack
}

// ToolCall represents a tool invocation (legacy format)
type ToolCall struct {
	Name string                 `json:"tool"`
	Args map[string]interface{} `json:"args"`
}

// buildSystemPrompt creates the system prompt with tool descriptions
func (a *ToolCallingAgent) buildSystemPrompt() string {
	var allowMemory func(string) bool
	var sanitizeMemory func(string, string) string
	memorySummary := ""
	if a.memoryPolicy != nil {
		allowMemory = a.memoryPolicy.AllowSource
		sanitizeMemory = func(_ string, content string) string {
			return memory.SanitizeSnippet(content)
		}
		memorySummary = a.memoryPolicy.Summary()
	}
	prompt := bootstrap.BuildPrompt(a.personality.Loader(), a.tools, bootstrap.PromptOptions{
		Mode:                   a.PromptMode,
		ThinkLevel:             a.ThinkLevel,
		MemoryMode:             a.MemoryMode,
		ModelAliases:           a.modelAliases,
		MemorySourceAllowed:    allowMemory,
		MemoryContentSanitizer: sanitizeMemory,
		MemoryPolicySummary:    memorySummary,
	})
	return appendNativeRuntimeGuard(prompt)
}

func (a *ToolCallingAgent) buildMemoryContextPack(ctx context.Context, query string) *memory.ContextPack {
	if a.memoryContextBuilder == nil || strings.TrimSpace(query) == "" {
		return nil
	}

	pack, err := a.memoryContextBuilder.Build(ctx, memory.ContextPackRequest{
		Query:  query,
		Scope:  a.memoryContextScope,
		Budget: a.memoryContextBudget,
	})
	if err != nil {
		logger.Warnf("ToolAgent: memory context pack failed: %v", err)
		return nil
	}
	return &pack
}

func appendMemoryContextPack(systemPrompt string, pack *memory.ContextPack) string {
	if pack == nil || !pack.HasContent() || strings.TrimSpace(pack.Text) == "" {
		return systemPrompt
	}

	var out strings.Builder
	out.WriteString(systemPrompt)
	if !strings.HasSuffix(systemPrompt, "\n") {
		out.WriteString("\n")
	}
	out.WriteString("\n")
	out.WriteString(pack.Text)
	if !strings.HasSuffix(pack.Text, "\n") {
		out.WriteString("\n")
	}
	out.WriteString("Use this cited memory only when relevant to the user's request.\n")
	return out.String()
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

// executeToolWithTimeout wraps executeToolFromJSON with an optional deadline.
// If ToolTimeout > 0 and onToolTimeout is set, the tool runs in a goroutine
// with a deadline. If the deadline fires, the callback spawns the work as a
// subagent and a notification string is returned as the tool "result" so the
// model can inform the user.
func (a *ToolCallingAgent) executeToolWithTimeout(ctx context.Context, toolName, argsJSON string) (out string, err error) {
	// Telemetry lives here, not in executeToolFromJSON: on the timeout-spawn
	// path the tool keeps running in a detached goroutine, so logging there
	// would report ok=false long after this call already returned success —
	// telemetry that lies is worse than none. Named returns + defer give one
	// line per call describing what the agent loop actually received.
	start := time.Now()
	spawned := false
	defer func() { logToolCall(toolName, start, len(out), err, spawned) }()

	// Tools that manage their own deadline (browser_task via SubmitAndWait,
	// image_gen via the AI client's internal timeout) declare it through
	// OwnsTimeout and skip the generic timeout.
	selfBounded := false
	if tool, ok := a.tools.Get(toolName); ok {
		selfBounded = tools.OwnsTimeout(tool)
	}
	if a.ToolTimeout <= 0 || a.onToolTimeout == nil || selfBounded {
		return a.executeToolFromJSON(ctx, toolName, argsJSON)
	}

	type toolResult struct {
		output string
		err    error
	}

	toolCtx, toolCancel := context.WithTimeout(ctx, a.ToolTimeout)
	defer toolCancel()

	ch := make(chan toolResult, 1)
	go func() {
		out, err := a.executeToolFromJSON(toolCtx, toolName, argsJSON)
		ch <- toolResult{out, err}
	}()

	select {
	case res := <-ch:
		return res.output, res.err
	case <-toolCtx.Done():
		if ctx.Err() != nil {
			// Parent context cancelled (user interrupt, shutdown) — propagate.
			return "", ctx.Err()
		}
		// Tool exceeded timeout — spawn as subagent.
		logger.Warnf("ToolAgent: tool %s exceeded %s timeout, spawning subagent", toolName, a.ToolTimeout)
		notification := a.onToolTimeout(toolName, argsJSON)
		spawned = true
		return notification, nil
	}
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
	start := time.Now()
	out, err := a.executeToolInner(ctx, toolCall)
	logToolCall(toolCall.Name, start, len(out), err, false)
	return out, err
}

func (a *ToolCallingAgent) executeToolInner(ctx context.Context, toolCall *ToolCall) (string, error) {
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
