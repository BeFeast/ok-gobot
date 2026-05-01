package worker

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// OpenCodeConfig holds OpenCode CLI-specific configuration.
type OpenCodeConfig struct {
	BinaryPath string `mapstructure:"binary_path"` // Path to opencode binary (default: "opencode")
	WorkDir    string `mapstructure:"work_dir"`    // Working directory for opencode execution
}

// OpenCodeAdapter executes worker tasks through `opencode run`.
type OpenCodeAdapter struct {
	config OpenCodeConfig
}

var _ Adapter = (*OpenCodeAdapter)(nil)

// NewOpenCodeAdapter creates an OpenCode-backed worker adapter.
func NewOpenCodeAdapter(cfg OpenCodeConfig) *OpenCodeAdapter {
	if cfg.BinaryPath == "" {
		cfg.BinaryPath = "opencode"
	}
	return &OpenCodeAdapter{config: cfg}
}

func (a *OpenCodeAdapter) buildArgs(req Request) []string {
	args := []string{"run"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, req.Task)
	return args
}

func (a *OpenCodeAdapter) workDir(req Request) string {
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		workDir = strings.TrimSpace(a.config.WorkDir)
	}
	return workDir
}

// Run executes an opencode task and returns its final output.
func (a *OpenCodeAdapter) Run(ctx context.Context, req Request) (*Result, error) {
	cmd := exec.CommandContext(ctx, a.config.BinaryPath, a.buildArgs(req)...)
	if dir := a.workDir(req); dir != "" {
		cmd.Dir = dir
	}

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("opencode exec failed (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("opencode exec failed: %w", err)
	}
	return &Result{Content: strings.TrimSpace(string(output))}, nil
}

// Stream executes an opencode task and streams stdout line by line.
func (a *OpenCodeAdapter) Stream(ctx context.Context, req Request) <-chan Event {
	ch := make(chan Event, 100)
	go func() {
		defer close(ch)

		cmd := exec.CommandContext(ctx, a.config.BinaryPath, a.buildArgs(req)...)
		if dir := a.workDir(req); dir != "" {
			cmd.Dir = dir
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- Event{Error: fmt.Errorf("failed to create stdout pipe: %w", err)}
			return
		}
		if err := cmd.Start(); err != nil {
			ch <- Event{Error: fmt.Errorf("failed to start opencode: %w", err)}
			return
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				ch <- Event{Error: ctx.Err(), Done: true}
				return
			default:
			}
			if line := scanner.Text(); line != "" {
				ch <- Event{Content: line}
			}
		}
		if ctx.Err() != nil {
			_ = cmd.Wait()
			ch <- Event{Error: ctx.Err(), Done: true}
			return
		}
		if err := scanner.Err(); err != nil {
			ch <- Event{Error: fmt.Errorf("stream read error: %w", err), Done: true}
		}
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				ch <- Event{Error: fmt.Errorf("opencode exited with code %d: %s", exitErr.ExitCode(), string(exitErr.Stderr)), Done: true}
			}
		} else {
			ch <- Event{Done: true}
		}
	}()
	return ch
}
