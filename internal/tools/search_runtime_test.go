package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubSearchProvider struct {
	description string
	result      string
	err         error
	calls       int
}

func (s *stubSearchProvider) Name() string        { return "search" }
func (s *stubSearchProvider) Description() string { return s.description }
func (s *stubSearchProvider) Execute(context.Context, ...string) (string, error) {
	s.calls++
	return s.result, s.err
}

func TestNewSearchToolChainOrdersConfiguredProviders(t *testing.T) {
	tests := []struct {
		name      string
		braveKey  string
		exaKey    string
		preferred string
		want      []string
	}{
		{name: "brave default with exa fallback", braveKey: "brave-key", exaKey: "exa-key", want: []string{"brave", "exa"}},
		{name: "exa preferred with brave fallback", braveKey: "brave-key", exaKey: "exa-key", preferred: "exa", want: []string{"exa", "brave"}},
		{name: "exa only", exaKey: "exa-key", want: []string{"exa"}},
		{name: "brave only despite exa preference", braveKey: "brave-key", preferred: "exa", want: []string{"brave"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewSearchToolChain(tt.braveKey, tt.exaKey, tt.preferred)
			chain, ok := tool.(*FallbackSearchTool)
			if !ok {
				t.Fatalf("NewSearchToolChain() type = %T, want *FallbackSearchTool", tool)
			}
			if len(chain.providers) != len(tt.want) {
				t.Fatalf("provider count = %d, want %d", len(chain.providers), len(tt.want))
			}
			for i, wantEngine := range tt.want {
				provider, ok := chain.providers[i].(*SearchTool)
				if !ok {
					t.Fatalf("provider[%d] type = %T, want *SearchTool", i, chain.providers[i])
				}
				if provider.Engine != wantEngine {
					t.Errorf("provider[%d].Engine = %q, want %q", i, provider.Engine, wantEngine)
				}
			}
		})
	}
}

func TestNewSearchToolChainReturnsNilWithoutKeys(t *testing.T) {
	if tool := NewSearchToolChain("", "", ""); tool != nil {
		t.Fatalf("NewSearchToolChain() = %T, want nil", tool)
	}
}

func TestFallbackSearchToolUsesSecondaryProvider(t *testing.T) {
	primary := &stubSearchProvider{description: "primary", err: errors.New("quota exhausted")}
	secondary := &stubSearchProvider{description: "secondary", result: "secondary results"}
	tool := &FallbackSearchTool{providers: []Tool{primary, secondary}}

	got, err := tool.Execute(context.Background(), "query")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got != "secondary results" {
		t.Fatalf("Execute() = %q, want secondary results", got)
	}
	if primary.calls != 1 || secondary.calls != 1 {
		t.Fatalf("calls = primary:%d secondary:%d, want 1 each", primary.calls, secondary.calls)
	}
}

func TestLoadFromConfigRegistersEnvironmentSearchChainAndKeepsWebFetch(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "brave-env-key")
	t.Setenv("EXA_API_KEY", "exa-env-key")
	t.Setenv("OKGOBOT_SEARCH_ENGINE", "exa")

	registry, err := LoadFromConfigWithOptions(t.TempDir(), &ToolsConfig{})
	if err != nil {
		t.Fatalf("LoadFromConfigWithOptions() error = %v", err)
	}

	search, ok := registry.Get("search")
	if !ok {
		t.Fatal("search tool is not registered from environment configuration")
	}
	chain, ok := search.(*FallbackSearchTool)
	if !ok {
		t.Fatalf("search tool type = %T, want *FallbackSearchTool", search)
	}
	if got := chain.Description(); !strings.Contains(got, "exa with brave") {
		t.Fatalf("search provider order = %q, want exa with brave", got)
	}
	if _, ok := registry.Get("web_fetch"); !ok {
		t.Fatal("web_fetch must remain registered alongside search")
	}
}

func TestLoadFromConfigExplicitSearchOptionsOverrideEnvironment(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "brave-env-key")
	t.Setenv("EXA_API_KEY", "exa-env-key")
	t.Setenv("OKGOBOT_SEARCH_ENGINE", "exa")

	registry, err := LoadFromConfigWithOptions(t.TempDir(), &ToolsConfig{
		BraveAPIKey:  "brave-explicit-key",
		SearchEngine: "brave",
	})
	if err != nil {
		t.Fatalf("LoadFromConfigWithOptions() error = %v", err)
	}
	search, ok := registry.Get("search")
	if !ok {
		t.Fatal("search tool is not registered")
	}
	chain := search.(*FallbackSearchTool)
	providers := chain.providers
	if providers[0].(*SearchTool).Engine != "brave" {
		t.Fatalf("primary engine = %q, want brave", providers[0].(*SearchTool).Engine)
	}
	if providers[0].(*SearchTool).APIKey != "brave-explicit-key" {
		t.Fatal("explicit Brave API key did not override environment")
	}
}
