package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	for i, provider := range s.providers {
		result, err := provider.Execute(ctx, args...)
		if err == nil {
			if i > 0 {
				log.Printf("[search] answered by fallback provider %d/%d (%s)", i+1, len(s.providers), provider.Description())
			}
			return result, nil
		}
		if _, denied := IsToolDenial(err); denied {
			return "", err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		log.Printf("[search] provider %d/%d failed (%s): %v", i+1, len(s.providers), provider.Description(), err)
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

	baseURL := braveSearchBaseURL
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

	// Brave's free tier allows one request per second, so a model that issues two
	// searches in the same turn rate-limits itself. One spaced retry turns that
	// self-inflicted 429 into a normal result instead of a failed tool call.
	if resp.StatusCode == http.StatusTooManyRequests {
		_ = resp.Body.Close()
		if err := sleepWithContext(ctx, braveRateLimitBackoff); err != nil {
			return "", err
		}
		retryReq := req.Clone(ctx)
		resp, err = client.Do(retryReq)
		if err != nil {
			return "", err
		}
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

	baseURL := exaSearchBaseURL
	// Exa returns id/title/url only unless contents are requested explicitly, so
	// without this block every result renders with an empty snippet. Highlights
	// are the query-relevant excerpt (what a search snippet should be); text is
	// the page opening, kept as a fallback for pages Exa cannot highlight. Both
	// are covered by the per-search price — no extra cost for asking.
	payload := map[string]interface{}{
		"query":         query,
		"numResults":    5,
		"useAutoprompt": true,
		"contents": map[string]interface{}{
			"highlights": map[string]interface{}{
				"numSentences":     3,
				"highlightsPerUrl": 1,
			},
			"text": map[string]interface{}{"maxCharacters": 500},
		},
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
			Title      string   `json:"title"`
			URL        string   `json:"url"`
			Summary    string   `json:"summary"`
			Highlights []string `json:"highlights"`
			Text       string   `json:"text"`
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
			i+1, r.Title, r.URL, exaSnippet(r.Highlights, r.Summary, r.Text))
	}

	return output, nil
}

// exaSnippet picks the most useful excerpt Exa returned for one result.
// Highlights are query-relevant, a summary is model-written, text is the raw
// page opening — in that order of preference.
func exaSnippet(highlights []string, summary, text string) string {
	for _, h := range highlights {
		if h = strings.TrimSpace(h); h != "" {
			return collapseSearchSnippet(h)
		}
	}
	if s := strings.TrimSpace(summary); s != "" {
		return collapseSearchSnippet(s)
	}
	return collapseSearchSnippet(strings.TrimSpace(text))
}

// collapseSearchSnippet flattens an excerpt onto one line so the numbered list
// stays readable, and caps it so five results cannot swamp the model's context.
func collapseSearchSnippet(s string) string {
	const maxSnippet = 400
	s = strings.Join(strings.Fields(s), " ")
	// Count runes, not bytes: a byte slice would cut Cyrillic mid-character.
	if runes := []rune(s); len(runes) > maxSnippet {
		s = strings.TrimSpace(string(runes[:maxSnippet])) + "…"
	}
	return s
}

// Provider endpoints, kept as vars so tests can point them at a local server.
var (
	braveSearchBaseURL = "https://api.search.brave.com/res/v1/web/search"
	exaSearchBaseURL   = "https://api.exa.ai/search"
)

// braveRateLimitBackoff clears Brave's one-request-per-second free-tier window.
const braveRateLimitBackoff = 1200 * time.Millisecond

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func searchHTTPClient(ctx context.Context) *http.Client {
	policy := NetworkPolicyFromContext(ctx)
	allowInternal := policy != nil && policy.AllowInternalNetworks
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: SSRFSafeTransport(allowInternal),
	}
}

// Schema + named params for both the concrete tool and the fallback chain
// (tool-schema disease, fixed fleet-wide 2026-08-21).
func searchToolSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Web search query",
			},
		},
		"required": []string{"query"},
	}
}

func searchQueryFromParams(params map[string]string) (string, error) {
	q := firstNonEmpty(params["query"], params["q"], params["input"], params["term"])
	if q == "" {
		return "", fmt.Errorf("search: 'query' is required")
	}
	return q, nil
}

func (s *SearchTool) GetSchema() map[string]interface{} { return searchToolSchema() }

func (s *SearchTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	q, err := searchQueryFromParams(params)
	if err != nil {
		return "", err
	}
	return s.Execute(ctx, q)
}

func (s *FallbackSearchTool) GetSchema() map[string]interface{} { return searchToolSchema() }

func (s *FallbackSearchTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	q, err := searchQueryFromParams(params)
	if err != nil {
		return "", err
	}
	return s.Execute(ctx, q)
}
