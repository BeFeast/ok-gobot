package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"ok-gobot/internal/logger"
)

// DroidConfig holds droid-specific configuration.
type DroidConfig struct {
	BinaryPath string `mapstructure:"binary_path"` // Path to droid binary (default: "droid")
	AutoLevel  string `mapstructure:"auto_level"`  // Autonomy level: "", "low", "medium", "high"
	WorkDir    string `mapstructure:"work_dir"`    // Working directory for droid execution
}

// DroidAdapter executes worker tasks through `droid exec`.
type DroidAdapter struct {
	config DroidConfig
}

var _ Adapter = (*DroidAdapter)(nil)

// NewDroidAdapter creates a Droid-backed worker adapter.
func NewDroidAdapter(cfg DroidConfig) *DroidAdapter {
	if cfg.BinaryPath == "" {
		cfg.BinaryPath = "droid"
	}
	return &DroidAdapter{config: cfg}
}

type droidStreamEvent struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Text    string `json:"text,omitempty"`

	Result    string `json:"result,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

type droidResult struct {
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	SessionID string `json:"session_id"`
}

func (a *DroidAdapter) buildArgs(req Request, outputFmt string) []string {
	args := []string{"exec"}

	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}

	args = append(args, "-o", outputFmt)

	if a.config.AutoLevel != "" {
		args = append(args, "--auto", a.config.AutoLevel)
	}

	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		workDir = strings.TrimSpace(a.config.WorkDir)
	}
	if workDir != "" {
		args = append(args, "--cwd", workDir)
	}

	args = append(args, req.Task)
	return args
}

// Run executes a droid task and returns its final output.
func (a *DroidAdapter) Run(ctx context.Context, req Request) (*Result, error) {
	args := a.buildArgs(req, "json")
	cmd := exec.CommandContext(ctx, a.config.BinaryPath, args...)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("droid exec failed (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("droid exec failed: %w", err)
	}

	var result droidResult
	if err := json.Unmarshal(output, &result); err != nil {
		return &Result{Content: strings.TrimSpace(string(output))}, nil
	}

	if result.IsError {
		return nil, fmt.Errorf("droid error: %s", result.Result)
	}

	return &Result{
		Content:   result.Result,
		SessionID: result.SessionID,
	}, nil
}

// Stream executes a droid task in stream-json mode.
func (a *DroidAdapter) Stream(ctx context.Context, req Request) <-chan Event {
	ch := make(chan Event, 100)

	go func() {
		defer close(ch)

		args := a.buildArgs(req, "stream-json")
		cmd := exec.CommandContext(ctx, a.config.BinaryPath, args...)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- Event{Error: fmt.Errorf("failed to create stdout pipe: %w", err)}
			return
		}

		if err := cmd.Start(); err != nil {
			ch <- Event{Error: fmt.Errorf("failed to start droid: %w", err)}
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

			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var evt droidStreamEvent
			if err := json.Unmarshal([]byte(line), &evt); err != nil {
				continue
			}

			switch evt.Type {
			case "message":
				text := evt.Content
				if text == "" {
					text = evt.Text
				}
				if text != "" {
					ch <- Event{Content: text}
				}

			case "completion":
				text := evt.Result
				if text == "" {
					text = evt.Content
				}
				if text != "" {
					ch <- Event{Content: text}
				}
				ch <- Event{Done: true}
				_ = cmd.Wait()
				return

			case "error":
				errMsg := evt.Content
				if errMsg == "" {
					errMsg = evt.Result
				}
				if errMsg == "" {
					errMsg = "unknown droid error"
				}
				ch <- Event{Error: fmt.Errorf("droid: %s", errMsg), Done: true}
				_ = cmd.Wait()
				return

			case "tool_call", "tool_result":
				logger.Debugf("Droid stream tool event: type=%s tool=%s", evt.Type, evt.ToolName)
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- Event{Error: fmt.Errorf("stream read error: %w", err), Done: true}
		}

		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				ch <- Event{
					Error: fmt.Errorf("droid exited with code %d: %s",
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
