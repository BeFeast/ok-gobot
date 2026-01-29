package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ok-gobot/internal/config"
)

func TestHealthEndpoint(t *testing.T) {
	cfg := config.APIConfig{
		Enabled: true,
		Port:    8080,
		APIKey:  "test-key",
	}

	server := NewAPIServer(cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	server.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %v", response["status"])
	}

	if _, ok := response["uptime"]; !ok {
		t.Error("Expected uptime field in response")
	}
}

func TestHealthEndpointNoAuth(t *testing.T) {
	// Health endpoint should work without API key
	cfg := config.APIConfig{
		Enabled: true,
		Port:    8080,
		APIKey:  "test-key",
	}

	server := NewAPIServer(cfg, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", server.handleHealth)
	handler := authMiddleware(cfg.APIKey)(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestAuthMiddleware(t *testing.T) {
	cfg := config.APIConfig{
		APIKey: "secret-key",
	}

	handler := authMiddleware(cfg.APIKey)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	tests := []struct {
		name           string
		path           string
		headers        map[string]string
		expectedStatus int
	}{
		{
			name:           "Valid X-API-Key",
			path:           "/api/status",
			headers:        map[string]string{"X-API-Key": "secret-key"},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Valid Bearer token",
			path:           "/api/status",
			headers:        map[string]string{"Authorization": "Bearer secret-key"},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Invalid API key",
			path:           "/api/status",
			headers:        map[string]string{"X-API-Key": "wrong-key"},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "Missing API key",
			path:           "/api/status",
			headers:        map[string]string{},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "Health endpoint without key",
			path:           "/api/health",
			headers:        map[string]string{},
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestSendEndpoint(t *testing.T) {
	cfg := config.APIConfig{
		Enabled: true,
		Port:    8080,
		APIKey:  "test-key",
	}

	// Create a mock bot (this would need actual bot setup in real tests)
	server := NewAPIServer(cfg, nil)

	tests := []struct {
		name           string
		body           interface{}
		expectedStatus int
	}{
		{
			name: "Valid send request",
			body: SendRequest{
				ChatID: 12345,
				Text:   "Hello",
			},
			expectedStatus: http.StatusInternalServerError, // Will fail without real bot
		},
		{
			name: "Missing chat_id",
			body: SendRequest{
				Text: "Hello",
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "Missing text",
			body: SendRequest{
				ChatID: 12345,
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "Invalid JSON",
			body:           "invalid",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodyBytes, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			server.handleSend(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestWebhookEndpoint(t *testing.T) {
	cfg := config.APIConfig{
		Enabled:     true,
		Port:        8080,
		APIKey:      "test-key",
		WebhookChat: 0, // Not configured
	}

	server := NewAPIServer(cfg, nil)

	tests := []struct {
		name           string
		body           interface{}
		expectedStatus int
	}{
		{
			name: "Missing event",
			body: WebhookRequest{
				Data: map[string]interface{}{"key": "value"},
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "Valid webhook but no chat configured",
			body: WebhookRequest{
				Event: "test_event",
				Data:  map[string]interface{}{"key": "value"},
			},
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodyBytes, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/api/webhook", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			server.handleWebhook(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestCORSMiddleware(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test OPTIONS request
	req := httptest.NewRequest(http.MethodOptions, "/api/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for OPTIONS, got %d", w.Code)
	}

	// Test CORS headers
	req = httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("Expected CORS origin header")
	}
}

func TestServerStartStop(t *testing.T) {
	cfg := config.APIConfig{
		Enabled: true,
		Port:    18080, // Use non-standard port for testing
		APIKey:  "test-key",
	}

	server := NewAPIServer(cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())

	// Start server
	go server.Start(ctx)

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Test that server is running
	resp, err := http.Get("http://localhost:18080/api/health")
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Stop server
	cancel()

	// Give server time to stop
	time.Sleep(100 * time.Millisecond)
}
