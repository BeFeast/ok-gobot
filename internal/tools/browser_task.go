package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ok-gobot/internal/delegation"
)

// SubagentSubmitter allows tools to spawn subagent runs and wait for results.
// This is a legacy compatibility seam while the chat/jobs runtime takes over.
type SubagentSubmitter interface {
	// SubmitAndWait spawns a subagent with an explicit delegated-run contract.
	SubmitAndWait(ctx context.Context, chatID int64, task string, job delegation.Job) (string, error)
}

// BrowserTaskTool decomposes browser tasks into subagent runs.
// It is part of the frozen legacy hub/subagent runtime surface and should only
// receive compatibility fixes until the chat/jobs replacement lands.
type BrowserTaskTool struct {
	submitter SubagentSubmitter
	chatID    int64
}

func NewBrowserTaskTool(submitter SubagentSubmitter, chatID int64) *BrowserTaskTool {
	return &BrowserTaskTool{submitter: submitter, chatID: chatID}
}

func (t *BrowserTaskTool) Name() string { return "browser_task" }

func (t *BrowserTaskTool) Description() string {
	return "Spawn a sub-agent to perform a focused browser task (e.g. visit a site, extract data). " +
		"Each task gets its own iteration budget and returns structured results. " +
		"Use this instead of calling browser tool directly for multi-site or complex tasks."
}

func (t *BrowserTaskTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("task description required")
	}
	return t.run(ctx, args[0])
}

func (t *BrowserTaskTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	task := params["task"]
	if task == "" {
		return "", fmt.Errorf("'task' is required")
	}
	return t.run(ctx, task)
}

func (t *BrowserTaskTool) run(ctx context.Context, task string) (string, error) {
	if t.submitter == nil {
		return "", fmt.Errorf("subagent submitter not configured")
	}

	prompt := fmt.Sprintf(`You are a browser worker. Your ONLY job is to complete this task and return structured data.

TASK: %s

RULES:
- Use the browser tool to navigate, snapshot, and extract data
- Return ONLY the extracted data as plain text — no screenshots, no commentary
- If the site has a Cloudflare challenge, say "BLOCKED: Cloudflare challenge" and stop
- If you can't find the data, say "NOT_FOUND: <reason>" and stop
- Be concise — extract the specific data requested, nothing more
- Do NOT send messages to the user — just return your findings as your final response`, task)

	job := delegation.Job{
		MaxToolCalls: 50,
		MaxDuration:  3 * time.Minute,
		OutputFormat: delegation.OutputFormatText,
		OutputSchema: `Return extracted findings only. On failure use "BLOCKED: <reason>" or "NOT_FOUND: <reason>".`,
		MemoryPolicy: delegation.MemoryPolicyReadOnly,
		ToolAllowlist: []string{
			"browser",
		},
	}.WithDefaults()

	result, err := t.submitter.SubmitAndWait(ctx, t.chatID, prompt, job)
	if err != nil {
		return "", fmt.Errorf("browser task failed: %w", err)
	}

	return result, nil
}

func (t *BrowserTaskTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{
				"type":        "string",
				"description": "Focused browser task description, e.g. 'Go to ksp.co.il, search for iPhone 16 Pro, find the price'",
			},
		},
		"required": []string{"task"},
	}
}

func (t *BrowserTaskTool) IsReadOnly() bool { return true }

func (t *BrowserTaskTool) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"name":        t.Name(),
		"description": t.Description(),
		"schema":      t.GetSchema(),
	})
}
