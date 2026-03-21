package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"ok-gobot/internal/bot"
	"ok-gobot/internal/config"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

// APIServer handles HTTP API requests
type APIServer struct {
	config     config.APIConfig
	bot        *bot.Bot
	store      *storage.Store
	workerHub runtime.WorkerObserver
	routeLog   *runtime.RouteLog
	jobService *runtime.JobService
	server     *http.Server
	uptime     time.Time
}

// NewAPIServer creates a new API server instance
func NewAPIServer(cfg config.APIConfig, b *bot.Bot) *APIServer {
	return &APIServer{
		config: cfg,
		bot:    b,
		uptime: time.Now(),
	}
}

// SetStore sets the storage backend for job queries.
func (s *APIServer) SetStore(store *storage.Store) {
	s.store = store
}

// SetWorkerHub sets the worker observer for worker queries.
func (s *APIServer) SetWorkerHub(hub runtime.WorkerObserver) {
	s.workerHub = hub
}

// SetRouteLog sets the route decision log for route queries.
func (s *APIServer) SetRouteLog(rl *runtime.RouteLog) {
	s.routeLog = rl
}

// SetJobService sets the job service for cancel/retry operations.
func (s *APIServer) SetJobService(js *runtime.JobService) {
	s.jobService = js
}

// Start initializes and starts the HTTP server
func (s *APIServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Register routes
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/send", s.handleSend)
	mux.HandleFunc("/api/webhook", s.handleWebhook)

	// Control-plane routes: jobs, workers, router decisions
	mux.HandleFunc("/api/jobs", s.handleJobs)
	mux.HandleFunc("/api/jobs/", s.handleJobByID) // /api/jobs/{id}[/events|/artifacts|/cancel]
	mux.HandleFunc("/api/workers", s.handleWorkers)
	mux.HandleFunc("/api/routes", s.handleRoutes)

	// Apply middleware
	handler := loggingMiddleware(mux)
	handler = corsMiddleware(handler)
	handler = authMiddleware(s.config.APIKey)(handler)

	// Create HTTP server — default to loopback to avoid exposing the API
	// on the wider network. Override via api.bind_addr config field.
	bindAddr := s.config.BindAddr
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", bindAddr, s.config.Port),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Printf("🌐 API server starting on port %d", s.config.Port)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("API server error: %v", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()
	return s.Stop(context.Background())
}

// Stop gracefully shuts down the HTTP server
func (s *APIServer) Stop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}

	log.Println("Stopping API server...")
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return s.server.Shutdown(shutdownCtx)
}

// handleHealth returns service health status
func (s *APIServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	uptime := time.Since(s.uptime).Round(time.Second).String()
	writeJSON(w, map[string]interface{}{
		"status": "ok",
		"uptime": uptime,
	})
}

// handleStatus returns bot status information
func (s *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if bot is available
	if s.bot == nil {
		writeJSONError(w, "Bot not available", http.StatusInternalServerError)
		return
	}

	status := s.bot.GetStatus()
	writeJSON(w, status)
}

// SendRequest represents a message send request
type SendRequest struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

// handleSend sends a message to a specific chat
func (s *APIServer) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.ChatID == 0 {
		writeJSONError(w, "chat_id is required", http.StatusBadRequest)
		return
	}

	if req.Text == "" {
		writeJSONError(w, "text is required", http.StatusBadRequest)
		return
	}

	// Check if bot is available
	if s.bot == nil {
		writeJSONError(w, "Bot not available", http.StatusInternalServerError)
		return
	}

	// Send message through bot
	if err := s.bot.SendMessage(req.ChatID, req.Text); err != nil {
		writeJSONError(w, fmt.Sprintf("Failed to send message: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"success": true,
		"message": "Message sent successfully",
	})
}

// WebhookRequest represents a generic webhook event
type WebhookRequest struct {
	Event string                 `json:"event"`
	Data  map[string]interface{} `json:"data"`
}

// handleWebhook processes generic webhook events
func (s *APIServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req WebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Event == "" {
		writeJSONError(w, "event is required", http.StatusBadRequest)
		return
	}

	// Check if webhook chat is configured
	if s.config.WebhookChat == 0 {
		writeJSONError(w, "Webhook chat not configured", http.StatusInternalServerError)
		return
	}

	// Check if bot is available
	if s.bot == nil {
		writeJSONError(w, "Bot not available", http.StatusInternalServerError)
		return
	}

	// Format webhook message
	dataJSON, _ := json.MarshalIndent(req.Data, "", "  ")
	message := fmt.Sprintf("🔔 Webhook Event: %s\n\n```json\n%s\n```", req.Event, string(dataJSON))

	// Send to configured chat
	if err := s.bot.SendMessage(s.config.WebhookChat, message); err != nil {
		writeJSONError(w, fmt.Sprintf("Failed to send webhook: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"success": true,
		"message": "Webhook processed successfully",
	})
}

// writeJSON writes a JSON response
func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(data)
}

// writeJSONError writes a JSON error response
func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": message,
	})
}
