package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	if len(args) == 0 {
		return "", fmt.Errorf("search query required")
	}

	searchQuery := args[0]

	switch s.Engine {
	case "brave":
		return s.searchBrave(searchQuery)
	case "exa":
		return s.searchExa(searchQuery)
	default:
		return "", fmt.Errorf("unsupported search engine: %s", s.Engine)
	}
}

// searchBrave performs a search using Brave Search API
func (s *SearchTool) searchBrave(query string) (string, error) {
	if s.APIKey == "" {
		return "", fmt.Errorf("Brave Search API key not configured")
	}

	baseURL := "https://api.search.brave.com/res/v1/web/search"
	params := url.Values{}
	params.Add("q", query)
	params.Add("count", "5")

	req, err := http.NewRequest("GET", baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", s.APIKey)

	client := &http.Client{Timeout: 10 * time.Second}
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
func (s *SearchTool) searchExa(query string) (string, error) {
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
	req, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.APIKey)

	client := &http.Client{Timeout: 10 * time.Second}
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
