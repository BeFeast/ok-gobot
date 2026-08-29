package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// OwnsTimeout reports that the tool manages its own deadline through
// SubmitAndWait, so the generic tool timeout must not apply.
func (t *BrowserTaskTool) OwnsTimeout() bool { return true }

func (t *BrowserTaskTool) Description() string {
	return "Spawn a sub-agent to perform a focused browser task (e.g. visit a site, extract data). " +
		"Each task gets its own iteration budget and returns structured results. " +
		"Use this instead of calling browser tool directly for multi-site or complex tasks. " +
		"This tool is read-only and cannot edit code, files, configuration, services, or deployments."
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
	if browserTaskRequestsImplementationMutation(task) {
		return "", browserTaskReadOnlyDenial()
	}

	if policy := NetworkPolicyFromContext(ctx); policy != nil && len(policy.NetworkAllowlist) > 0 {
		return "", browserTaskAllowlistDenial()
	}

	if t.submitter == nil {
		return "", fmt.Errorf("subagent submitter not configured")
	}

	prompt := fmt.Sprintf(`You are a browser worker. Your ONLY job is to complete this task and return structured data.

TASK: %s

RULES:
- Use the browser tool to navigate, snapshot, and extract data
- Prefer snapshot and page text over clicking. If the requested data is already on the page, extract it and stop.
- Snapshot returns a text field with the visible page contents. Read that first. If ax_error is set, do not retry snapshot; use the text field or browser text with no selector.
- browser text with no selector dumps the visible page text. Use that instead of guessing CSS selectors.
- Click only using snapshot_id + ref from the latest snapshot. Do not guess CSS selectors.
- If click, fill, or text returns "not found" or "not visible", do not retry that selector. Snapshot again or return what is already visible.
- After two failed interactions, stop with NOT_FOUND: <reason>
- Return ONLY the extracted data as plain text — no screenshots, no commentary
- If the site has a Cloudflare challenge, say "BLOCKED: Cloudflare challenge" and stop
- If you can't find the data, say "NOT_FOUND: <reason>" and stop
- Be concise — extract the specific data requested, nothing more
- Do NOT send messages to the user — just return your findings as your final response`, task)

	job := delegation.Job{
		// Measured 2026-08-21: 5 of 8 background jobs died at 201-238s against
		// the old 3-minute cap. Real sites (x.com, GitHub) spend 5-15s per
		// navigate+snapshot, so a handful of steps used to exhaust the budget
		// before the worker could answer.
		//
		// Raising MaxDuration to 10m alone only moved the bottleneck to the
		// call counter: measured 2026-08-24 14:55:07-14:58:23, a run spent
		// exactly 50 browser calls in 203.6s and died on tool_call_limit with
		// 66% of its clock unused. Across all-time telemetry the 10-minute
		// budget was never once reached.
		//
		// That run also gives the pace: 50 calls / 203.6s = 4.07s per call
		// (median 4s, p90 6s), of which only 40.8s was in-tool time — browser
		// steps are cheap, the model round-trip between them is not. A
		// 10-minute clock therefore buys 600s / 4.07s = ~147 calls, so 150 is
		// the ceiling at which the two budgets expire together at the observed
		// pace. Slower-than-median runs now stop on the clock, which is the
		// intended safety stop; the counter stays as a runaway guard. For
		// scale, the largest browser burst that ran to completion used 25.
		MaxToolCalls: 150,
		MaxDuration:  10 * time.Minute,
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

// browserTaskReadOnlyDenial is the refusal for tasks that ask browser_task to
// change something. It is a *ToolDenial rather than a bare error so every
// layer classifies it as a deliberate policy decision: telemetry logs
// denied=true instead of ok=false, the agent loop skips failure reflection,
// and the model gets the DENIED wording with remediation. Measured
// 2026-08-24: 2 of browser_task's 4 all-time "failures" were this refusal.
func browserTaskReadOnlyDenial() *ToolDenial {
	return &ToolDenial{
		ToolName:    "browser_task",
		Family:      "spawn",
		Reason:      "browser_task is read-only and cannot change implementations, files, configuration, services, or deployments",
		Remediation: "Use browser_task only to read or extract. To change something, use the file, patch, or local tools directly.",
	}
}

func browserTaskRequestsImplementationMutation(task string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(task), " "))
	mutationTerms := []string{
		"fix", "implement", "edit", "modify", "patch", "refactor", "write", "update",
		"configure", "deploy", "restart", "remove", "delete", "create",
		"исправ", "реализ", "измен", "обнов", "настрой", "развер", "перезапуст", "удал", "созда",
	}
	implementationTerms := []string{
		"implementation", "code", "codebase", "repository", "repo", "file", "config", "configuration",
		"service", "deployment", "script", "skill", "path", "video summary", "video-summary", "video_summary",
		"реализац", "код", "репозитор", "файл", "конфиг", "сервис", "скрипт", "навык", "путь",
	}
	return containsAny(normalized, mutationTerms) && containsAny(normalized, implementationTerms)
}

func containsAny(input string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(input, needle) {
			return true
		}
	}
	return false
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
