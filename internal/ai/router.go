package ai

import "log"

// RoutingConfig holds per-task-type model routing rules.
// It is populated from config.ModelRoutingConfig by the application bootstrap.
type RoutingConfig struct {
	Vision    string // model for vision/image tasks
	Summarize string // model for summarization tasks
	Reasoning string // model for complex reasoning tasks
	Coding    string // model for code generation/review tasks
	Default   string // fallback before the global ai.model
}

// TaskType classifies a request so the model router can select the best model.
type TaskType string

const (
	// TaskTypeVision handles image analysis and multimodal tasks.
	TaskTypeVision TaskType = "vision"
	// TaskTypeSummarize handles condensing long content into shorter form.
	TaskTypeSummarize TaskType = "summarize"
	// TaskTypeReasoning handles multi-step logic, planning, and analysis.
	TaskTypeReasoning TaskType = "reasoning"
	// TaskTypeCoding handles code generation, review, and debugging.
	TaskTypeCoding TaskType = "coding"
	// TaskTypeDefault is the catch-all when no specific type is matched.
	TaskTypeDefault TaskType = "default"
)

// ParseTaskType converts a string into a TaskType. Unknown strings map to
// TaskTypeDefault.
func ParseTaskType(s string) TaskType {
	switch s {
	case string(TaskTypeVision):
		return TaskTypeVision
	case string(TaskTypeSummarize):
		return TaskTypeSummarize
	case string(TaskTypeReasoning):
		return TaskTypeReasoning
	case string(TaskTypeCoding):
		return TaskTypeCoding
	default:
		return TaskTypeDefault
	}
}

// ModelRouter selects the appropriate model for a given task type.
// It consults the routing config first, then falls back to a global default.
type ModelRouter struct {
	routing       RoutingConfig
	globalDefault string
}

// NewModelRouter creates a ModelRouter from a routing config and the global
// default model (typically ai.model from Config).
func NewModelRouter(routing RoutingConfig, globalDefault string) *ModelRouter {
	return &ModelRouter{
		routing:       routing,
		globalDefault: globalDefault,
	}
}

// Route returns the model identifier and a reason string for the given task
// type. The reason is intended for log messages so operators can see which
// routing rule fired.
//
// Resolution order:
//  1. Per-task-type rule from model_routing config (e.g. model_routing.coding)
//  2. model_routing.default — a catch-all routing override
//  3. Global default (ai.model)
func (r *ModelRouter) Route(taskType TaskType) (model string, reason string) {
	configured := r.configuredModel(taskType)
	if configured != "" {
		log.Printf("[model-router] task_type=%s model=%s reason=routing_rule", taskType, configured)
		return configured, "routing_rule:" + string(taskType)
	}

	if r.routing.Default != "" {
		log.Printf("[model-router] task_type=%s model=%s reason=routing_default", taskType, r.routing.Default)
		return r.routing.Default, "routing_default"
	}

	log.Printf("[model-router] task_type=%s model=%s reason=global_default", taskType, r.globalDefault)
	return r.globalDefault, "global_default"
}

// RouteString is like Route but accepts a raw string task type.
// Unknown strings map to TaskTypeDefault.
func (r *ModelRouter) RouteString(taskType string) (model string, reason string) {
	return r.Route(ParseTaskType(taskType))
}

// configuredModel returns the model configured for taskType, or "" if none.
func (r *ModelRouter) configuredModel(taskType TaskType) string {
	switch taskType {
	case TaskTypeVision:
		return r.routing.Vision
	case TaskTypeSummarize:
		return r.routing.Summarize
	case TaskTypeReasoning:
		return r.routing.Reasoning
	case TaskTypeCoding:
		return r.routing.Coding
	default:
		return ""
	}
}
