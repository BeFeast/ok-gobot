package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client defines the interface for AI providers
type Client interface {
	Complete(ctx context.Context, messages []Message) (string, error)
}

// ProviderConfig holds configuration for an AI provider
type ProviderConfig struct {
	Name    string
	APIKey  string
	BaseURL string
	Model   string
}

// OpenAICompatibleClient implements Client for OpenAI-compatible APIs
// Works with: OpenAI, OpenRouter, Anyscale, Together, etc.
type OpenAICompatibleClient struct {
	config     ProviderConfig
	httpClient *http.Client
}

// NewClient creates a new AI client from provider configuration
func NewClient(config ProviderConfig) (*OpenAICompatibleClient, error) {
	// Set defaults
	if config.BaseURL == "" {
		switch config.Name {
		case "openai":
			config.BaseURL = "https://api.openai.com/v1"
		case "openrouter":
			config.BaseURL = "https://openrouter.ai/api/v1"
		default:
			return nil, fmt.Errorf("unknown provider: %s (specify BaseURL)", config.Name)
		}
	}

	if config.Model == "" {
		config.Model = "gpt-4o"
	}

	return &OpenAICompatibleClient{
		config: config,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}, nil
}

// chatCompletionRequest represents the API request body
type chatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// chatCompletionResponse represents the API response
type chatCompletionResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete sends messages and returns the response
func (c *OpenAICompatibleClient) Complete(ctx context.Context, messages []Message) (string, error) {
	reqBody := chatCompletionRequest{
		Model:    c.config.Model,
		Messages: messages,
		Stream:   false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		c.config.BaseURL+"/chat/completions",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	// OpenRouter-specific headers
	if c.config.Name == "openrouter" {
		req.Header.Set("HTTP-Referer", "https://github.com/moltbot/moltbot")
		req.Header.Set("X-Title", "Moltbot")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result chatCompletionResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("API error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no response from model")
	}

	return result.Choices[0].Message.Content, nil
}

// AvailableModels returns common models for each provider
func AvailableModels() map[string][]string {
	return map[string][]string{
		"openrouter": {
			"moonshotai/kimi-k2.5",          // Kimi K2.5
			"anthropic/claude-3.5-sonnet",   // Claude 3.5 Sonnet
			"anthropic/claude-3-opus",       // Claude 3 Opus
			"openai/gpt-4o",                 // GPT-4o
			"openai/gpt-4o-mini",            // GPT-4o Mini
			"google/gemini-pro-1.5",         // Gemini Pro 1.5
			"meta-llama/llama-3.1-70b",      // Llama 3.1 70B
			"mistralai/mistral-large",       // Mistral Large
			"nvidia/llama-3.1-nemotron-70b", // Nemotron 70B
		},
		"openai": {
			"gpt-4o",
			"gpt-4o-mini",
			"gpt-4-turbo",
			"gpt-3.5-turbo",
		},
	}
}
