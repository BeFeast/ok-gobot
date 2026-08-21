package agent

import (
	"context"
	"testing"

	"ok-gobot/internal/delegation"
	"ok-gobot/internal/tools"
)

type stubSubagentSubmitter struct{}

func (stubSubagentSubmitter) SubmitAndWait(_ context.Context, _ int64, _ string, _ delegation.Job) (string, error) {
	return "", nil
}

func execLaneHasTool(reg *tools.Registry, name string) bool {
	for _, tool := range reg.List() {
		if tool.Name() == name {
			return true
		}
	}
	return false
}

// SOUL.md forbids the main agent from executing: it may only spawn host_task,
// while exec is reserved for sub-agents (bound to the chat for approval).
func TestBuildToolRegistry_ExecIsSubagentOnly(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistry()
	base.Register(tools.NewExecTool(""))

	resolver := &RunResolver{
		ToolRegistry:      base,
		MediaSender:       &resolverMediaSender{},
		SubagentSubmitter: stubSubagentSubmitter{},
	}
	profile := &AgentProfile{}

	main := resolver.buildToolRegistry(123, profile, false, nil)
	if execLaneHasTool(main, "exec") {
		t.Error("main agent must not have exec (SOUL.md: no exec in main session)")
	}
	if !execLaneHasTool(main, "host_task") {
		t.Error("main agent must have host_task to delegate host operations")
	}

	sub := resolver.buildToolRegistry(123, profile, true, nil)
	if !execLaneHasTool(sub, "exec") {
		t.Error("sub-agent must have exec")
	}
	if execLaneHasTool(sub, "host_task") {
		t.Error("sub-agent must not have host_task (prevents recursive spawning)")
	}

	// The sub-agent's exec must be a distinct chat-bound copy, not the base tool.
	subExec, ok := sub.Get("exec")
	if !ok {
		t.Fatal("sub-agent exec missing")
	}
	baseExec, _ := base.Get("exec")
	if subExec == baseExec {
		t.Error("sub-agent exec was not rebound with the chatID")
	}
}
