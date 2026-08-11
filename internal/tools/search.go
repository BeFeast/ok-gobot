package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SearchTool provides web search capabilities
type SearchTool struct {
	APIKey string
	Engine string // "exa", "brave", "serper", etc.
}

// NewSearchTool creates a new search tool
func NewSearchTool(apiKey, engine string) *SearchTool {
	if engine == "" {
		engine = "brave"
	}
	return &SearchTool{
		APIKey: apiKey,
		Engine: engine,
	}
}

// FallbackSearchTool tries configured search providers in order. This keeps
// search independent from browser/CDP and lets a secondary API provider cover
// transient quota or availability failures in the primary provider.
type FallbackSearchTool struct {
	providers []Tool
}

// NewSearchToolChain builds a Brave/Exa search chain. Brave is preferred by
// default; preferred="exa" reverses the order. Providers without a key are
// omitted, and nil is returned when search is not configured.
func NewSearchToolChain(braveAPIKey, exaAPIKey, preferred string) Tool {
	braveAPIKey = strings.TrimSpace(braveAPIKey)
	exaAPIKey = strings.TrimSpace(exaAPIKey)
	preferred = strings.ToLower(strings.TrimSpace(preferred))

	providers := make([]Tool, 0, 2)
	addBrave := func() {
		if braveAPIKey != "" {
			providers = append(providers, NewSearchTool(braveAPIKey, "brave"))
		}
	}
	addExa := func() {
		if exaAPIKey != "" {
			providers = append(providers, NewSearchTool(exaAPIKey, "exa"))
		}
	}

	if preferred == "exa" {
		addExa()
		addBrave()
	} else {
		addBrave()
		addExa()
	}
	if len(providers) == 0 {
		return nil
	}
	return &FallbackSearchTool{providers: providers}
}

func (s *FallbackSearchTool) Name() string {
	return "search"
}

func (s *FallbackSearchTool) Description() string {
	engines := make([]string, 0, len(s.providers))
	for _, provider := range s.providers {
		if search, ok := provider.(*SearchTool); ok {
			engines = append(engines, search.Engine)
		}
	}
	if len(engines) == 0 {
		return "Search the web"
	}
	return "Search the web using " + strings.Join(engines, " with ")
}

func (s *FallbackSearchTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("search query required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	failures := make([]string, 0, len(s.providers))
	for _, provider := range s.providers {
		result, err := provider.Execute(ctx, args...)
		if err == nil {
			return result, nil
		}
		if _, denied := IsToolDenial(err); denied {
			return "", err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		failures = append(failures, fmt.Sprintf("%s: %v", provider.Description(), err))
	}
	return "", fmt.Errorf("all search providers failed: %s", strings.Join(failures, "; "))
}

func (s *SearchTool) Name() string {
	return "search"
}

func (s *SearchTool) Description() string {
	return fmt.Sprintf("Search the web using %s", s.Engine)
}

// SearchResult represents a single search result
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// Execute performs a web search
func (s *SearchTool) Execute(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if policy := NetworkPolicyFromContext(ctx); policy != nil && len(policy.NetworkAllowlist) > 0 {
		return "", searchAllowlistDenial()
	}

	if len(args) == 0 {
		return "", fmt.Errorf("search query required")
	}

	searchQuery := args[0]

	switch s.Engine {
	case "brave":
		return s.searchBrave(ctx, searchQuery)
	case "exa":
		return s.searchExa(ctx, searchQuery)
	default:
		return "", fmt.Errorf("unsupported search engine: %s", s.Engine)
	}
}

// searchBrave performs a search using Brave Search API
func (s *SearchTool) searchBrave(ctx context.Context, query string) (string, error) {
	if s.APIKey == "" {
		return "", fmt.Errorf("Brave Search API key not configured")
	}

	baseURL := "https://api.search.brave.com/res/v1/web/search"
	params := url.Values{}
	params.Add("q", query)
	params.Add("count", "5")

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", s.APIKey)

	client := searchHTTPClient(ctx)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search API returned status %d", resp.StatusCode)
	}

	var result struct {
		Web struct {
			Results []struct {
				Title string `json:"title"`
				URL   string `json:"url"`
				Desc  string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if len(result.Web.Results) == 0 {
		return "No results found.", nil
	}

	var output string
	output = fmt.Sprintf("Search results for '%s':\n\n", query)
	for i, r := range result.Web.Results {
		output += fmt.Sprintf("%d. **%s**\n   %s\n   %s\n\n",
			i+1, r.Title, r.URL, r.Desc)
	}

	return output, nil
}

// searchExa performs a search using Exa API
func (s *SearchTool) searchExa(ctx context.Context, query string) (string, error) {
	if s.APIKey == "" {
		return "", fmt.Errorf("Exa API key not configured")
	}

	baseURL := "https://api.exa.ai/search"
	payload := map[string]interface{}{
		"query":         query,
		"numResults":    5,
		"useAutoprompt": true,
	}

	jsonPayload, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.APIKey)

	client := searchHTTPClient(ctx)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Exa API returned status %d", resp.StatusCode)
	}

	var result struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Summary string `json:"summary"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if len(result.Results) == 0 {
		return "No results found.", nil
	}

	var output string
	output = fmt.Sprintf("Search results for '%s':\n\n", query)
	for i, r := range result.Results {
		output += fmt.Sprintf("%d. **%s**\n   %s\n   %s\n\n",
			i+1, r.Title, r.URL, r.Summary)
	}

	return output, nil
}

func searchHTTPClient(ctx context.Context) *http.Client {
	policy := NetworkPolicyFromContext(ctx)
	allowInternal := policy != nil && policy.AllowInternalNetworks
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: SSRFSafeTransport(allowInternal),
	}
}
