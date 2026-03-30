// Package batch implements parallel task fan-out: decompose a task into
// subtasks, execute each subtask in its own git worktree, and consolidate
// results into a single branch ready for a PR.
package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ok-gobot/internal/ai"
)

// Subtask is one unit of work produced by decomposing a larger task.
type Subtask struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

const decompositionPrompt = `You are a software engineering task planner. Your job is to decompose a large task into %d or fewer independent subtasks that can be executed in parallel by separate AI coding agents.

Rules:
- Each subtask must be self-contained and independently executable.
- Subtasks must not have dependencies on each other (they run concurrently).
- Each subtask should target a different set of files or modules.
- Keep titles short (≤ 10 words) and descriptions specific.
- Return ONLY a JSON array of objects with "title" and "description" fields.
- Do not include any explanation outside the JSON array.

Task to decompose:
%s`

// DecomposeTask asks the AI client to split task into at most maxSubtasks
// independent subtasks. Returns at least 1 subtask (the original task).
func DecomposeTask(ctx context.Context, client ai.Client, task string, maxSubtasks int) ([]Subtask, error) {
	if maxSubtasks <= 0 {
		maxSubtasks = 5
	}

	prompt := fmt.Sprintf(decompositionPrompt, maxSubtasks, task)
	messages := []ai.Message{
		{Role: "user", Content: prompt},
	}

	response, err := client.Complete(ctx, messages)
	if err != nil {
		// Fallback: treat the whole task as one subtask
		return []Subtask{{Title: "Execute task", Description: task}}, nil
	}

	subtasks, err := parseSubtasks(response)
	if err != nil || len(subtasks) == 0 {
		// Fallback: treat the whole task as one subtask
		return []Subtask{{Title: "Execute task", Description: task}}, nil
	}

	if len(subtasks) > maxSubtasks {
		subtasks = subtasks[:maxSubtasks]
	}

	return subtasks, nil
}

// parseSubtasks extracts a []Subtask from an AI response that may contain
// markdown code fences or other surrounding text.
func parseSubtasks(response string) ([]Subtask, error) {
	// Strip markdown code fences if present.
	cleaned := strings.TrimSpace(response)
	if idx := strings.Index(cleaned, "```"); idx != -1 {
		// Find the opening fence end.
		start := strings.Index(cleaned[idx:], "\n")
		if start != -1 {
			cleaned = cleaned[idx+start+1:]
		}
		// Strip closing fence.
		if end := strings.LastIndex(cleaned, "```"); end != -1 {
			cleaned = cleaned[:end]
		}
		cleaned = strings.TrimSpace(cleaned)
	}

	// Find JSON array boundaries.
	arrayStart := strings.Index(cleaned, "[")
	arrayEnd := strings.LastIndex(cleaned, "]")
	if arrayStart == -1 || arrayEnd == -1 || arrayEnd <= arrayStart {
		return nil, fmt.Errorf("no JSON array found in response")
	}
	cleaned = cleaned[arrayStart : arrayEnd+1]

	var subtasks []Subtask
	if err := json.Unmarshal([]byte(cleaned), &subtasks); err != nil {
		return nil, fmt.Errorf("failed to parse subtasks JSON: %w", err)
	}

	// Filter out empty entries.
	result := subtasks[:0]
	for _, st := range subtasks {
		st.Title = strings.TrimSpace(st.Title)
		st.Description = strings.TrimSpace(st.Description)
		if st.Title != "" && st.Description != "" {
			result = append(result, st)
		}
	}

	return result, nil
}
