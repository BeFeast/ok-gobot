package tools

import (
	"context"
	"testing"
)

// Every tool the registry exposes must speak structured JSON params: the
// positional fallback destroys multi-word/multi-line arguments and map
// iteration order makes it nondeterministic. This regression test keeps new
// tools from reintroducing the disease (obsidian failed 13/13 on 2026-08-21).
func TestAllRegisteredToolsSpeakJSON(t *testing.T) {
	tools := []Tool{
		&ObsidianTool{},
		&PatchTool{},
		&WebFetchTool{},
		&TTSTool{},
		&SearchTool{},
		&FallbackSearchTool{},
		&CronTool{},
		&MemoryGetTool{},
		&MemorySearchTool{},
		&RecommendRolesTool{},
		&SearchFileTool{},
	}
	for _, tool := range tools {
		if _, ok := tool.(interface{ GetSchema() map[string]interface{} }); !ok {
			t.Errorf("%T: no GetSchema — model only sees the lossy generic input param", tool)
		}
		if _, ok := tool.(interface {
			ExecuteJSON(ctx context.Context, params map[string]string) (string, error)
		}); !ok {
			t.Errorf("%T: no ExecuteJSON — named params go through positional mangling", tool)
		}
	}
}
