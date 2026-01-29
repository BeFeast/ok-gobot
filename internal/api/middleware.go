package api

import (
	"net/http"
	"strings"
)

// authMiddleware creates a middleware that validates API key
func authMiddleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth for health endpoint
			if r.URL.Path == "/api/health" {
				next.ServeHTTP(w, r)
				return
			}

			// Check API key from X-API-Key header
			providedKey := r.Header.Get("X-API-Key")

			// Also check Authorization: Bearer <key>
			if providedKey == "" {
				authHeader := r.Header.Get("Authorization")
				if strings.HasPrefix(authHeader, "Bearer ") {
					providedKey = strings.TrimPrefix(authHeader, "Bearer ")
				}
			}

			// Validate API key
			if providedKey != apiKey {
				writeJSONError(w, "Unauthorized: invalid or missing API key", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// loggingMiddleware logs HTTP requests
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simple request logging
		// log.Printf("[API] %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware adds CORS headers
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
