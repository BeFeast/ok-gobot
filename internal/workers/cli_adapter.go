package workers

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CLIAdapter runs a task via a generic external CLI binary.
type CLIAdapter struct {
	name        string
	description string
	binary      string
	buildArgs   func(RunRequest) []string
}

// NewCLIAdapter creates a generic external CLI worker adapter.
func NewCLIAdapter(name, description, binary string, buildArgs func(RunRequest) []string) *CLIAdapter {
	return &CLIAdapter{
		name:        name,
		description: description,
		binary:      binary,
		buildArgs:   buildArgs,
	}
}

func (a *CLIAdapter) Name() string {
	return a.name
}

func (a *CLIAdapter) Description() string {
	return a.description
}

func (a *CLIAdapter) Binary() string {
	return a.binary
}

func (a *CLIAdapter) Run(ctx context.Context, req RunRequest, emit func(RunUpdate)) (RunResult, error) {
	args := a.buildArgs(req)
	cmd := exec.CommandContext(ctx, a.binary, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return RunResult{}, fmt.Errorf("%s failed: %w", a.name, err)
	}

	text := strings.TrimSpace(string(output))
	if emit != nil && text != "" {
		emit(RunUpdate{Kind: "output", Message: text})
	}

	return RunResult{Output: text}, nil
}

// NewCodexAdapter creates a minimal `codex exec` adapter.
func NewCodexAdapter(binary string) *CLIAdapter {
	if strings.TrimSpace(binary) == "" {
		binary = "codex"
	}

	return NewCLIAdapter(
		"codex_cli",
		"Runs bounded tasks through `codex exec`.",
		binary,
		func(req RunRequest) []string {
			args := []string{"exec"}
			if strings.TrimSpace(req.Model) != "" {
				args = append(args, "--model", req.Model)
			}
			if strings.TrimSpace(req.WorkingDir) != "" {
				args = append(args, "--cwd", req.WorkingDir)
			}
			args = append(args, req.Prompt)
			return args
		},
	)
}

// NewClaudeAdapter creates a minimal `claude -p` adapter.
func NewClaudeAdapter(binary string) *CLIAdapter {
	if strings.TrimSpace(binary) == "" {
		binary = "claude"
	}

	return NewCLIAdapter(
		"claude_cli",
		"Runs bounded tasks through `claude -p`.",
		binary,
		func(req RunRequest) []string {
			args := []string{"-p", req.Prompt}
			if strings.TrimSpace(req.Model) != "" {
				args = append(args, "--model", req.Model)
			}
			if strings.TrimSpace(req.WorkingDir) != "" {
				args = append(args, "--cwd", req.WorkingDir)
			}
			return args
		},
	)
}
