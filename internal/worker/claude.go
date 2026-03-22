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

// ClaudeConfig holds Claude CLI-specific configuration.
type ClaudeConfig struct {
	BinaryPath string `mapstructure:"binary_path"` // Path to claude binary (default: "claude")
	WorkDir    string `mapstructure:"work_dir"`    // Working directory for claude execution
}

// ClaudeAdapter executes worker tasks through the Anthropic Claude CLI.
type ClaudeAdapter struct {
	config ClaudeConfig
}

var _ Adapter = (*ClaudeAdapter)(nil)

// NewClaudeAdapter creates a Claude CLI-backed worker adapter.
func NewClaudeAdapter(cfg ClaudeConfig) *ClaudeAdapter {
	if cfg.BinaryPath == "" {
		cfg.BinaryPath = "claude"
	}
	return &ClaudeAdapter{config: cfg}
}

// claudeResult is the JSON envelope returned by `claude -p --output-format json`.
type claudeResult struct {
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	SessionID string `json:"session_id"`
}

// claudeStreamEvent is one JSONL line from `claude -p --output-format stream-json`.
type claudeStreamEvent struct {
	Type    string `json:"type"`              // "assistant", "result", "system", "error"
	Subtype string `json:"subtype,omitempty"` // "text", "success", "error"
	Content string `json:"content,omitempty"`
	Result  string `json:"result,omitempty"`

	SessionID string `json:"session_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

func (a *ClaudeAdapter) buildArgs(req Request, outputFmt string) []string {
	args := []string{"-p", "--output-format", outputFmt}

	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	args = append(args, req.Task)
	return args
}

func (a *ClaudeAdapter) workDir(req Request) string {
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		workDir = strings.TrimSpace(a.config.WorkDir)
	}
	return workDir
}

// Run executes a claude task and returns its final output.
func (a *ClaudeAdapter) Run(ctx context.Context, req Request) (*Result, error) {
	args := a.buildArgs(req, "json")
	cmd := exec.CommandContext(ctx, a.config.BinaryPath, args...)
	if dir := a.workDir(req); dir != "" {
		cmd.Dir = dir
	}

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("claude exec failed (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("claude exec failed: %w", err)
	}

	var result claudeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return &Result{Content: strings.TrimSpace(string(output))}, nil
	}

	if result.IsError {
		return nil, fmt.Errorf("claude error: %s", result.Result)
	}

	return &Result{
		Content:   result.Result,
		SessionID: result.SessionID,
	}, nil
}

// Stream executes a claude task in stream-json mode.
func (a *ClaudeAdapter) Stream(ctx context.Context, req Request) <-chan Event {
	ch := make(chan Event, 100)

	go func() {
		defer close(ch)

		args := a.buildArgs(req, "stream-json")
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
			ch <- Event{Error: fmt.Errorf("failed to start claude: %w", err)}
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

			var evt claudeStreamEvent
			if err := json.Unmarshal([]byte(line), &evt); err != nil {
				continue
			}

			switch evt.Type {
			case "assistant":
				text := evt.Content
				if text != "" {
					ch <- Event{Content: text}
				}

			case "result":
				if evt.IsError {
					errMsg := evt.Result
					if errMsg == "" {
						errMsg = "unknown claude error"
					}
					ch <- Event{Error: fmt.Errorf("claude: %s", errMsg), Done: true}
					_ = cmd.Wait()
					return
				}
				text := evt.Result
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
					errMsg = "unknown claude error"
				}
				ch <- Event{Error: fmt.Errorf("claude: %s", errMsg), Done: true}
				_ = cmd.Wait()
				return

			default:
				logger.Debugf("Claude stream event: type=%s subtype=%s", evt.Type, evt.Subtype)
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- Event{Error: fmt.Errorf("stream read error: %w", err), Done: true}
		}

		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				ch <- Event{
					Error: fmt.Errorf("claude exited with code %d: %s",
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
