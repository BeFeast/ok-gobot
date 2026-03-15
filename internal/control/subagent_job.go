package control

import (
	"fmt"
	"strings"
	"time"

	"ok-gobot/internal/delegation"
)

func buildDelegationJob(cmd ClientMsg) (delegation.Job, error) {
	job := delegation.Job{
		Model:         strings.TrimSpace(cmd.Model),
		Thinking:      strings.TrimSpace(cmd.Thinking),
		ToolAllowlist: append([]string(nil), cmd.ToolAllowlist...),
		WorkspaceRoot: strings.TrimSpace(cmd.WorkspaceRoot),
		MaxToolCalls:  cmd.MaxToolCalls,
		OutputFormat:  strings.TrimSpace(cmd.OutputFormat),
		OutputSchema:  strings.TrimSpace(cmd.OutputSchema),
		MemoryPolicy:  strings.TrimSpace(cmd.MemoryPolicy),
	}

	if raw := strings.TrimSpace(cmd.MaxDuration); raw != "" {
		duration, err := time.ParseDuration(raw)
		if err != nil {
			return delegation.Job{}, fmt.Errorf("max_duration: %w", err)
		}
		job.MaxDuration = duration
	}

	if raw := strings.TrimSpace(cmd.OutputFormat); raw != "" {
		if _, ok := delegation.ParseOutputFormat(raw); !ok {
			return delegation.Job{}, fmt.Errorf("output_format must be one of: text, markdown, json")
		}
	}

	if raw := strings.TrimSpace(cmd.MemoryPolicy); raw != "" {
		if _, ok := delegation.ParseMemoryPolicy(raw); !ok {
			return delegation.Job{}, fmt.Errorf("memory_policy must be one of: inherit, read_only, allow_writes")
		}
	}

	if cmd.MaxToolCalls < 0 {
		return delegation.Job{}, fmt.Errorf("max_tool_calls must be >= 0")
	}

	return job.WithDefaults(), nil
}
