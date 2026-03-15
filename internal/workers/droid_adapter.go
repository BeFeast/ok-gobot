package workers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// DroidConfig contains settings for the droid CLI worker.
type DroidConfig struct {
	BinaryPath string
	AutoLevel  string
	WorkDir    string
}

// DroidEvent is one JSON event emitted by `droid exec -o stream-json`.
type DroidEvent struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	Text      string          `json:"text,omitempty"`
	Result    string          `json:"result,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// DroidAdapter runs tasks through `droid exec`.
type DroidAdapter struct {
	cfg DroidConfig
}

// DroidJSONResult is the parsed completion payload from `droid exec -o json`.
type DroidJSONResult struct {
	Output    string
	SessionID string
}

// NewDroidAdapter creates a droid worker adapter.
func NewDroidAdapter(cfg DroidConfig) *DroidAdapter {
	if strings.TrimSpace(cfg.BinaryPath) == "" {
		cfg.BinaryPath = "droid"
	}
	return &DroidAdapter{cfg: cfg}
}

func (a *DroidAdapter) Name() string {
	return "droid_cli"
}

func (a *DroidAdapter) Description() string {
	return "Runs bounded tasks through `droid exec`."
}

func (a *DroidAdapter) Binary() string {
	return a.cfg.BinaryPath
}

// BuildArgs constructs the droid exec command arguments.
func (a *DroidAdapter) BuildArgs(prompt, outputFmt, model, workingDir string) []string {
	args := []string{"exec"}

	if strings.TrimSpace(model) != "" {
		args = append(args, "-m", model)
	}

	args = append(args, "-o", outputFmt)

	if strings.TrimSpace(a.cfg.AutoLevel) != "" {
		args = append(args, "--auto", a.cfg.AutoLevel)
	}

	cwd := strings.TrimSpace(workingDir)
	if cwd == "" {
		cwd = strings.TrimSpace(a.cfg.WorkDir)
	}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}

	args = append(args, prompt)
	return args
}

// RunJSON executes droid in JSON mode and returns the completion payload.
func (a *DroidAdapter) RunJSON(ctx context.Context, prompt, model, workingDir string) (DroidJSONResult, error) {
	args := a.BuildArgs(prompt, "json", model, workingDir)
	cmd := exec.CommandContext(ctx, a.cfg.BinaryPath, args...)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return DroidJSONResult{}, fmt.Errorf("droid exec failed (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return DroidJSONResult{}, fmt.Errorf("droid exec failed: %w", err)
	}

	var event DroidEvent
	if err := json.Unmarshal(output, &event); err != nil {
		return DroidJSONResult{Output: strings.TrimSpace(string(output))}, nil
	}
	if event.IsError {
		return DroidJSONResult{}, fmt.Errorf("droid error: %s", event.Result)
	}

	return DroidJSONResult{
		Output:    strings.TrimSpace(event.Result),
		SessionID: event.SessionID,
	}, nil
}

// Stream executes droid in stream-json mode and emits progress updates.
func (a *DroidAdapter) Stream(ctx context.Context, prompt, model, workingDir string, emit func(RunUpdate)) (RunResult, error) {
	args := a.BuildArgs(prompt, "stream-json", model, workingDir)
	cmd := exec.CommandContext(ctx, a.cfg.BinaryPath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("failed to start droid: %w", err)
	}

	var output strings.Builder
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		default:
		}

		var event DroidEvent
		line := scanner.Bytes()
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		switch event.Type {
		case "message":
			chunk := event.Content
			if chunk == "" {
				chunk = event.Text
			}
			output.WriteString(chunk)
			if emit != nil && chunk != "" {
				emit(RunUpdate{Kind: "output", Message: chunk})
			}
		case "tool":
			if emit != nil {
				emit(RunUpdate{Kind: "tool", Message: event.ToolName})
			}
		case "error":
			msg := event.Content
			if msg == "" {
				msg = event.Result
			}
			return RunResult{}, fmt.Errorf("droid error: %s", msg)
		case "completion":
			if strings.TrimSpace(event.Result) != "" && output.Len() == 0 {
				output.WriteString(event.Result)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return RunResult{}, fmt.Errorf("failed to read droid output: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return RunResult{}, fmt.Errorf("droid exec failed (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return RunResult{}, fmt.Errorf("droid exec failed: %w", err)
	}

	return RunResult{Output: strings.TrimSpace(output.String())}, nil
}

// Run executes the task and emits output chunks as progress updates.
func (a *DroidAdapter) Run(ctx context.Context, req RunRequest, emit func(RunUpdate)) (RunResult, error) {
	return a.Stream(ctx, req.Prompt, req.Model, req.WorkingDir, emit)
}
