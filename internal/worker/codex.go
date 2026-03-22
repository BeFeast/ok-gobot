package worker

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CodexConfig holds Codex CLI-specific configuration.
type CodexConfig struct {
	BinaryPath string `mapstructure:"binary_path"` // Path to codex binary (default: "codex")
	WorkDir    string `mapstructure:"work_dir"`    // Working directory for codex execution
}

// CodexAdapter executes worker tasks through the OpenAI Codex CLI.
type CodexAdapter struct {
	config CodexConfig
}

var _ Adapter = (*CodexAdapter)(nil)

// NewCodexAdapter creates a Codex CLI-backed worker adapter.
func NewCodexAdapter(cfg CodexConfig) *CodexAdapter {
	if cfg.BinaryPath == "" {
		cfg.BinaryPath = "codex"
	}
	return &CodexAdapter{config: cfg}
}

func (a *CodexAdapter) buildArgs(req Request) []string {
	args := []string{"--quiet", "--full-auto"}

	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	args = append(args, req.Task)
	return args
}

func (a *CodexAdapter) workDir(req Request) string {
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		workDir = strings.TrimSpace(a.config.WorkDir)
	}
	return workDir
}

// Run executes a codex task and returns its final output.
func (a *CodexAdapter) Run(ctx context.Context, req Request) (*Result, error) {
	args := a.buildArgs(req)
	cmd := exec.CommandContext(ctx, a.config.BinaryPath, args...)
	if dir := a.workDir(req); dir != "" {
		cmd.Dir = dir
	}

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("codex exec failed (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("codex exec failed: %w", err)
	}

	return &Result{Content: strings.TrimSpace(string(output))}, nil
}

// Stream executes a codex task and streams stdout line by line.
func (a *CodexAdapter) Stream(ctx context.Context, req Request) <-chan Event {
	ch := make(chan Event, 100)

	go func() {
		defer close(ch)

		args := a.buildArgs(req)
		cmd := exec.CommandContext(ctx, a.config.BinaryPath, args...)
		if dir := a.workDir(req); dir != "" {
			cmd.Dir = dir
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- Event{Error: fmt.Errorf("failed to create stdout pipe: %w", err)}
			return
		}

		if err := cmd.Start(); err != nil {
			ch <- Event{Error: fmt.Errorf("failed to start codex: %w", err)}
			return
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				ch <- Event{Error: ctx.Err(), Done: true}
				return
			default:
			}

			line := scanner.Text()
			if line != "" {
				ch <- Event{Content: line}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- Event{Error: fmt.Errorf("stream read error: %w", err), Done: true}
		}

		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				ch <- Event{
					Error: fmt.Errorf("codex exited with code %d: %s",
						exitErr.ExitCode(), string(exitErr.Stderr)),
					Done: true,
				}
			}
		} else {
			ch <- Event{Done: true}
		}
	}()

	return ch
}
