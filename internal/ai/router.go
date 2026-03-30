package ai

import (
	"log"
	"strings"
)

// TaskType represents a routing category for model selection.
type TaskType string

const (
	// TaskTypeVision routes image/vision tasks to a model with vision support.
	TaskTypeVision TaskType = "vision"
	// TaskTypeSummarize routes summarization to a cheap/fast model.
	TaskTypeSummarize TaskType = "summarize"
	// TaskTypeReasoning routes complex reasoning tasks to a capable model.
	TaskTypeReasoning TaskType = "reasoning"
	// TaskTypeCoding routes code generation/review to a coding-optimized model.
	TaskTypeCoding TaskType = "coding"
	// TaskTypeDefault is the fallback when no specific task type is detected.
	TaskTypeDefault TaskType = "default"
)

// knownTaskTypes lists all recognized task types for tag detection.
var knownTaskTypes = []TaskType{
	TaskTypeVision,
	TaskTypeSummarize,
	TaskTypeReasoning,
	TaskTypeCoding,
	TaskTypeDefault,
}

// Router selects AI models based on task type.
// It consults a routes map (task type → model) and falls back to a default model
// when no specific route is configured for the requested task type.
type Router struct {
	routes       map[string]string
	defaultModel string
}

// NewRouter creates a Router from a routes map and a fallback default model.
// Keys in routes are task type names (case-insensitive); values are model identifiers.
// An empty or nil routes map means the router always returns defaultModel.
func NewRouter(routes map[string]string, defaultModel string) *Router {
	r := &Router{
		routes:       make(map[string]string, len(routes)),
		defaultModel: defaultModel,
	}
	for k, v := range routes {
		r.routes[strings.ToLower(k)] = v
	}
	return r
}

// Route returns the model identifier and a human-readable reason for the given
// task type. The reason is suitable for structured log output.
//
// Resolution order:
//  1. Exact match in routes for taskType.
//  2. "default" entry in routes.
//  3. The global defaultModel supplied to NewRouter.
func (r *Router) Route(taskType TaskType) (model string, reason string) {
	if r == nil {
		return "", "router is nil"
	}
	if len(r.routes) == 0 {
		log.Printf("[router] task_type=%s -> model=%s (reason: no routing configured, using global default)", taskType, r.defaultModel)
		return r.defaultModel, "no routing configured, using global default"
	}

	key := strings.ToLower(string(taskType))

	// Exact task-type match.
	if m, ok := r.routes[key]; ok && m != "" {
		reason = "routing config matched task_type=" + string(taskType)
		log.Printf("[router] task_type=%s -> model=%s (reason: %s)", taskType, m, reason)
		return m, reason
	}

	// Fall back to the "default" route entry if present.
	if m, ok := r.routes["default"]; ok && m != "" {
		reason = "task_type=" + string(taskType) + " not in routes, using routing default"
		log.Printf("[router] task_type=%s -> model=%s (reason: %s)", taskType, m, reason)
		return m, reason
	}

	// Last resort: global default.
	reason = "no routing match for task_type=" + string(taskType) + ", using global default"
	log.Printf("[router] task_type=%s -> model=%s (reason: %s)", taskType, r.defaultModel, reason)
	return r.defaultModel, reason
}

// HasRoutes reports whether the router has any per-task-type routes configured.
func (r *Router) HasRoutes() bool {
	return r != nil && len(r.routes) > 0
}

// DetectTaskType inspects a message and returns the most likely TaskType.
//
// Detection order:
//  1. Explicit tag in the message body: [task:vision], [task:coding], etc.
//  2. Returns TaskTypeDefault when no tag is found.
//
// Callers that want content-based heuristics should implement them on top of
// this function; explicit tags are the only detection mechanism here to keep
// behaviour predictable.
func DetectTaskType(message string) TaskType {
	lower := strings.ToLower(message)
	for _, tt := range knownTaskTypes {
		tag := "[task:" + string(tt) + "]"
		if strings.Contains(lower, tag) {
			return tt
		}
	}
	return TaskTypeDefault
}

// StripTaskTag removes all explicit [task:...] routing tags from a message,
// returning the cleaned string. Useful when the tag should not be forwarded
// to the AI model.
func StripTaskTag(message string) string {
	lower := strings.ToLower(message)
	result := message
	for _, tt := range knownTaskTypes {
		tag := "[task:" + string(tt) + "]"
		// Remove case-insensitively by scanning the lower-cased string.
		for {
			idx := strings.Index(lower, tag)
			if idx < 0 {
				break
			}
			result = result[:idx] + result[idx+len(tag):]
			lower = lower[:idx] + lower[idx+len(tag):]
		}
	}
	return strings.TrimSpace(result)
}
