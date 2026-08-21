package tools

import (
	"context"
	"fmt"
	"log"
	"time"

	"ok-gobot/internal/delegation"
)

// HostTaskTool delegates host operations to a sub-agent. It is the main agent's
// only door to execution: SOUL.md forbids the main session from running any
// shell itself, so the main agent spawns a host-ops worker that owns the exec
// tool (yolo — commands run immediately, no approval). This mirrors browser_task,
// but for mutating host work rather than read-only browsing.
//
// Before the worker runs, a restic snapshot of the agent's workspaces is taken so
// the whole task can be reverted at file granularity; the snapshot id is handed to
// the worker so it can name the restore point in its report.
type HostTaskTool struct {
	submitter   SubagentSubmitter
	chatID      int64
	snapshotter Snapshotter
}

func NewHostTaskTool(submitter SubagentSubmitter, chatID int64) *HostTaskTool {
	return &HostTaskTool{
		submitter:   submitter,
		chatID:      chatID,
		snapshotter: NewResticSnapshotter(),
	}
}

func (t *HostTaskTool) Name() string { return "host_task" }

// OwnsTimeout: the tool blocks on the snapshot + SubmitAndWait, which enforces the
// sub-agent's own MaxDuration, so the generic per-tool timeout must not apply.
func (t *HostTaskTool) OwnsTimeout() bool { return true }

func (t *HostTaskTool) Description() string {
	return "Spawn a host-operations worker to carry out a task on the machine: install packages, " +
		"edit configuration files, restart services, run smoke tests, and record evidence. Commands " +
		"run immediately (no approval). A restic snapshot of the workspaces is taken first so the " +
		"task is revertable. Use this to actually perform host operations instead of writing a " +
		"runbook and stopping."
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

	// Take a restore point before any mutation. Best-effort: a snapshot failure
	// does not block the task (yolo), but the worker is told there is no restore
	// point so it can be more careful.
	revertNote := "No pre-task snapshot was taken (snapshotter unavailable)."
	if t.snapshotter != nil {
		id, err := t.snapshotter.Snapshot(ctx, fmt.Sprintf("host_task:%d", t.chatID))
		switch {
		case err != nil:
			log.Printf("[host_task] pre-task snapshot failed: %v", err)
			revertNote = fmt.Sprintf("WARNING: pre-task snapshot FAILED (%v) — no restore point for this task.", err)
		case id == "":
			revertNote = "A restic snapshot of the workspaces was taken (nothing changed since the last one)."
		default:
			log.Printf("[host_task] pre-task restic snapshot %s", id)
			revertNote = fmt.Sprintf("A restic snapshot %s of the workspaces was taken before this task. "+
				"To revert a file: restic -r ~/.ok-gobot-restic --password-file ~/.ok-gobot/restic-repo.pass restore %s --target / --include <path>.", id, id)
		}
	}

	prompt := fmt.Sprintf(`You are a host-operations worker on this machine. Complete the task below and report exactly what you did with evidence.

TASK: %s

REVERT SAFETY: %s

HOW YOU WORK:
- Use the exec tool to run shell commands on the host. Commands run immediately — there is no approval step.
- Prefer the smallest, most reversible step. exec also backs up each existing file a command overwrites, on top of the pre-task snapshot.
- After a change, run a smoke test and verify the result yourself (rc=0 and a real check) before claiming success.
- Never send private code, credentials, or personal data to third-party services.
- If you record evidence in a note, use the obsidian tool.

RULES:
- Do NOT claim a step succeeded without live verification.
- Report failures honestly with the command output.
- Return a concise summary: what changed, what was verified (with rc/output), and how to revert if needed.`, task, revertNote)

	job := delegation.Job{
		MaxToolCalls: 60,
		MaxDuration:  15 * time.Minute,
		OutputFormat: delegation.OutputFormatText,
		OutputSchema: `Return a concise report: what changed, what was verified (with rc/output), and how to revert.`,
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
