package router

import (
	"fmt"
	"strings"
)

// Action is the router decision for one inbound user message.
type Action string

const (
	ActionReply     Action = "reply"
	ActionClarify   Action = "clarify"
	ActionLaunchJob Action = "launch_job"
)

var heavyKeywords = []string{
	"implement", "fix", "refactor", "build", "research", "investigate",
	"analyze", "scan", "monitor", "watch", "compare", "summarize",
	"write code", "search repo", "browse", "create issue", "run tests",
}

// Decision is the normalized router output.
type Decision struct {
	Action        Action `json:"action"`
	WorkerBackend string `json:"worker_backend,omitempty"`
	Reason        string `json:"reason"`
	Summary       string `json:"summary"`
}

// Router is a rules-first classifier for fast reply vs background job launch.
type Router struct {
	defaultWorker string
}

// New creates a router using the given default worker backend.
func New(defaultWorker string) *Router {
	return &Router{defaultWorker: defaultWorker}
}

// Decide returns the action for a user message.
func (r *Router) Decide(content string) Decision {
	text := strings.TrimSpace(content)
	if text == "" {
		return Decision{
			Action:  ActionClarify,
			Reason:  "empty message",
			Summary: "Please send a message with enough detail to answer or launch a job.",
		}
	}

	lower := strings.ToLower(text)
	for _, keyword := range heavyKeywords {
		if strings.Contains(lower, keyword) {
			return Decision{
				Action:        ActionLaunchJob,
				WorkerBackend: r.defaultWorker,
				Reason:        fmt.Sprintf("matched heavy-work keyword %q", keyword),
				Summary:       summarize(text),
			}
		}
	}

	if len(text) > 280 || strings.Count(text, "\n") >= 3 {
		return Decision{
			Action:        ActionLaunchJob,
			WorkerBackend: r.defaultWorker,
			Reason:        "message is long or multi-step",
			Summary:       summarize(text),
		}
	}

	return Decision{
		Action:  ActionReply,
		Reason:  "short conversational turn",
		Summary: summarize(text),
	}
}

func summarize(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if len(text) <= 120 {
		return text
	}
	return strings.TrimSpace(text[:117]) + "..."
}
