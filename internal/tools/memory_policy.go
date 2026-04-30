package tools

import "ok-gobot/internal/memory"

// ApplyMemoryRecallPolicy returns a child registry with memory tools constrained
// to the current user/chat/session recall policy.
func ApplyMemoryRecallPolicy(registry *Registry, policy *memory.RecallPolicy) *Registry {
	if registry == nil || policy == nil {
		return registry
	}

	result := registry.Child()
	for _, tool := range registry.List() {
		switch t := tool.(type) {
		case *MemorySearchTool:
			result.Register(NewScopedMemorySearchTool(t.manager, policy))
		case *MemoryGetTool:
			result.Register(t.WithPolicy(policy))
		default:
			result.Register(tool)
		}
	}
	return result
}
