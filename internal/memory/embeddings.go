package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// EmbeddingClient handles communication with embedding API
type EmbeddingClient struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewEmbeddingClient creates a new embedding client
func NewEmbeddingClient(baseURL, apiKey, model string) *EmbeddingClient {
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &EmbeddingClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// embeddingRequest represents the API request body
type embeddingRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

// embeddingResponse represents the API response
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// GetEmbedding returns the embedding vector for the given text
func (c *EmbeddingClient) GetEmbedding(ctx context.Context, text string) ([]float32, error) {
	reqBody := embeddingRequest{
		Input: text,
		Model: c.model,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		c.baseURL+"/embeddings",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result embeddingResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("API error: %s", result.Error.Message)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned from API")
	}

	return result.Data[0].Embedding, nil
}
