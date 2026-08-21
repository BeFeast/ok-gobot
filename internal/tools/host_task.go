package tools

import (
	"context"
	"fmt"
	"time"

	"ok-gobot/internal/delegation"
)

// HostTaskTool delegates host operations to a sub-agent. It is the main agent's
// only door to execution: SOUL.md forbids the main session from running any
// shell itself, so the main agent spawns a host-ops worker that owns the exec
// tool (deny-by-default, operator-approved). This mirrors browser_task, but for
// mutating host work (install packages, edit configs, restart services, smoke
// tests) rather than read-only browsing.
type HostTaskTool struct {
	submitter SubagentSubmitter
	chatID    int64
}

func NewHostTaskTool(submitter SubagentSubmitter, chatID int64) *HostTaskTool {
	return &HostTaskTool{submitter: submitter, chatID: chatID}
}

func (t *HostTaskTool) Name() string { return "host_task" }

// OwnsTimeout: the tool blocks on SubmitAndWait, which enforces the sub-agent's
// own MaxDuration, so the generic per-tool timeout must not apply.
func (t *HostTaskTool) OwnsTimeout() bool { return true }

func (t *HostTaskTool) Description() string {
	return "Spawn a host-operations worker to carry out a task on the machine: install packages, " +
		"back up and edit configuration files, restart services, run smoke tests, and record " +
		"evidence. The worker uses the exec tool — read-only commands run immediately, and any " +
		"state-changing command asks Oleg for approval in Telegram before it runs. Use this to " +
		"actually perform host operations instead of writing a runbook and stopping."
}

func (t *HostTaskTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("task description required")
	}
	return t.run(ctx, args[0])
}

func (t *HostTaskTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	task := firstNonEmptyParam(params, "task", "input", "description")
	if task == "" {
		return "", fmt.Errorf("'task' is required")
	}
	return t.run(ctx, task)
}

func (t *HostTaskTool) run(ctx context.Context, task string) (string, error) {
	if t.submitter == nil {
		return "", fmt.Errorf("subagent submitter not configured")
	}

	prompt := fmt.Sprintf(`You are a host-operations worker on this machine. Complete the task below and report exactly what you did with evidence.

TASK: %s

HOW YOU WORK:
- Use the exec tool to run shell commands on the host.
- Read-only commands (ls, cat, systemctl status, journalctl, git status, curl localhost, …) run immediately.
- Any state-changing command (install, edit a config, restart a service, delete, chmod, sudo, …) will ask Oleg for approval in Telegram. Wait for his decision; if denied, stop and report what was blocked.
- Prefer the smallest, most reversible step. Back up a config before editing it. After a change, run a smoke test and verify the result yourself before claiming success.
- If you record evidence in a note, use the obsidian tool.

RULES:
- Do NOT claim a step succeeded without live verification (rc=0 and a real check).
- Report failures honestly with the command output.
- Return a concise summary of what changed, what was verified, and anything still blocked or pending approval.`, task)

	job := delegation.Job{
		MaxToolCalls: 60,
		MaxDuration:  15 * time.Minute,
		OutputFormat: delegation.OutputFormatText,
		OutputSchema: `Return a concise report: what changed, what was verified (with rc/output), and anything blocked or pending approval.`,
		MemoryPolicy: delegation.MemoryPolicyReadOnly,
		ToolAllowlist: []string{
			"exec",
			"obsidian",
			"grep",
			"search_file",
			"web_fetch",
		},
	}.WithDefaults()

	result, err := t.submitter.SubmitAndWait(ctx, t.chatID, prompt, job)
	if err != nil {
		return "", fmt.Errorf("host task failed: %w", err)
	}
	return result, nil
}

func (t *HostTaskTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{
				"type": "string",
				"description": "Focused host-operations task, e.g. 'Add the ox-alpha provider to the OpenCode config: " +
					"back up the config, add the provider block, run a smoke test, and record evidence in the setup note.'",
			},
		},
		"required": []string{"task"},
	}
}
