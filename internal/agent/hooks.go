package agent

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HookEvent names the lifecycle point at which a hook fires.
type HookEvent string

const (
	// HookSessionStart fires once at the beginning of ProcessRequestWithContent,
	// before the first LLM call. Use it to load project context, set env vars,
	// or apply project-specific rules.
	HookSessionStart HookEvent = "session_start"

	// HookPreToolUse fires immediately before each tool call.
	// Receives the tool name and its raw JSON arguments.
	// Use it to log commands, validate inputs, or apply security checks.
	HookPreToolUse HookEvent = "pre_tool_use"

	// HookPostToolUse fires immediately after each tool call completes.
	// Receives the tool name, its raw JSON arguments, and the result/error.
	// Use it to verify results, collect metrics, or trigger follow-up actions.
	HookPostToolUse HookEvent = "post_tool_use"

	// HookSessionEnd fires once when ProcessRequestWithContent returns, whether
	// successfully or after an error. Use it for cleanup or audit logging.
	HookSessionEnd HookEvent = "session_end"
)

// hookScriptName maps lifecycle events to their script filenames.
var hookScriptName = map[HookEvent]string{
	HookSessionStart: "session_start.sh",
	HookPreToolUse:   "pre_tool_use.sh",
	HookPostToolUse:  "post_tool_use.sh",
	HookSessionEnd:   "session_end.sh",
}

// HookRunner discovers and executes bash hook scripts for agent lifecycle events.
//
// Hook scripts live in a directory (default ~/.ok-gobot/hooks/) and are named
// after their lifecycle event:
//
//	session_start.sh   — fired before the first LLM call
//	pre_tool_use.sh    — fired before each tool execution
//	post_tool_use.sh   — fired after each tool execution
//	session_end.sh     — fired when the session completes
//
// Each script receives event data via environment variables (see RunHook).
// Scripts that exit non-zero are logged but do not abort the agent run.
// Missing scripts are silently skipped.
type HookRunner struct {
	dir string // absolute path to hooks directory
}

// NewHookRunner returns a HookRunner that looks for scripts in dir.
// If dir is empty it resolves to ~/.ok-gobot/hooks/.
// Returns nil (no-op) if the directory does not exist.
func NewHookRunner(dir string) *HookRunner {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("[hooks] cannot resolve home dir: %v", err)
			return nil
		}
		dir = filepath.Join(home, ".ok-gobot", "hooks")
	}

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		// No hooks directory — silently return a no-op runner.
		return nil
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		log.Printf("[hooks] cannot resolve hooks dir %q: %v", dir, err)
		return nil
	}

	return &HookRunner{dir: abs}
}

// RunSessionStart executes the session_start hook if present.
//
// Environment variables:
//
//	HOOK_EVENT=session_start
//	HOOK_MESSAGE=<user message (truncated to 4096 bytes)>
func (h *HookRunner) RunSessionStart(userMessage string) {
	if h == nil {
		return
	}
	env := []string{
		"HOOK_EVENT=session_start",
		"HOOK_MESSAGE=" + truncate(userMessage, 4096),
	}
	h.run(HookSessionStart, env)
}

// RunPreToolUse executes the pre_tool_use hook if present.
//
// Environment variables:
//
//	HOOK_EVENT=pre_tool_use
//	HOOK_TOOL_NAME=<tool name>
//	HOOK_TOOL_INPUT=<raw JSON arguments>
func (h *HookRunner) RunPreToolUse(toolName, argsJSON string) {
	if h == nil {
		return
	}
	env := []string{
		"HOOK_EVENT=pre_tool_use",
		"HOOK_TOOL_NAME=" + toolName,
		"HOOK_TOOL_INPUT=" + argsJSON,
	}
	h.run(HookPreToolUse, env)
}

// RunPostToolUse executes the post_tool_use hook if present.
//
// Environment variables:
//
//	HOOK_EVENT=post_tool_use
//	HOOK_TOOL_NAME=<tool name>
//	HOOK_TOOL_INPUT=<raw JSON arguments>
//	HOOK_TOOL_OUTPUT=<result text (truncated to 4096 bytes)>
//	HOOK_TOOL_ERROR=<error message, empty if no error>
func (h *HookRunner) RunPostToolUse(toolName, argsJSON, output string, toolErr error) {
	if h == nil {
		return
	}
	errMsg := ""
	if toolErr != nil {
		errMsg = toolErr.Error()
	}
	env := []string{
		"HOOK_EVENT=post_tool_use",
		"HOOK_TOOL_NAME=" + toolName,
		"HOOK_TOOL_INPUT=" + argsJSON,
		"HOOK_TOOL_OUTPUT=" + truncate(output, 4096),
		"HOOK_TOOL_ERROR=" + errMsg,
	}
	h.run(HookPostToolUse, env)
}

// RunSessionEnd executes the session_end hook if present.
//
// Environment variables:
//
//	HOOK_EVENT=session_end
//	HOOK_RESPONSE=<final response text (truncated to 4096 bytes)>
//	HOOK_TOOLS_USED=<comma-separated tool names, empty if none>
func (h *HookRunner) RunSessionEnd(finalResponse string, toolsUsed []string) {
	if h == nil {
		return
	}
	env := []string{
		"HOOK_EVENT=session_end",
		"HOOK_RESPONSE=" + truncate(finalResponse, 4096),
		"HOOK_TOOLS_USED=" + strings.Join(toolsUsed, ","),
	}
	h.run(HookSessionEnd, env)
}

// run executes the script for event, merging env into the process environment.
// Missing scripts are silently skipped. Non-zero exits are logged but do not
// propagate — hooks must not abort agent execution.
func (h *HookRunner) run(event HookEvent, env []string) {
	scriptName, ok := hookScriptName[event]
	if !ok {
		return
	}
	scriptPath := filepath.Join(h.dir, scriptName)

	info, err := os.Stat(scriptPath)
	if err != nil {
		// Script does not exist — skip silently.
		return
	}
	// Require the script to be a regular file.
	if !info.Mode().IsRegular() {
		log.Printf("[hooks] %s: not a regular file, skipping", scriptPath)
		return
	}

	cmd := exec.Command("bash", scriptPath)
	// Inherit the current process environment, then layer hook-specific vars on top.
	cmd.Env = append(os.Environ(), env...)

	log.Printf("[hooks] executing %s", scriptPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[hooks] %s exited with error: %v\n%s", scriptPath, err, string(out))
		return
	}
	if len(out) > 0 {
		log.Printf("[hooks] %s output:\n%s", scriptPath, string(out))
	}
}

// truncate returns s truncated to at most maxBytes UTF-8 bytes.
func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes]
}
