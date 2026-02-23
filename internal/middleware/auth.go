package middleware

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/config"
)

// Auth returns a middleware that checks incoming requests for valid API keys.
// If no API keys are configured, authentication is disabled.
// GET / and OPTIONS requests always bypass authentication.
func Auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always allow health check and CORS preflight
		if r.URL.Path == "/" || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		keys := config.GetAPIKeys()
		if len(keys) == 0 {
			// Auth disabled
			next.ServeHTTP(w, r)
			return
		}

		// Extract key from request
		apiKey := extractAPIKey(r)
		if apiKey == "" {
			unauthorized(w)
			return
		}

		// Check against configured keys
		valid := false
		for _, k := range keys {
			if k == apiKey {
				valid = true
				break
			}
		}

		if !valid {
			unauthorized(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// extractAPIKey gets the API key from x-api-key header or Authorization Bearer.
func extractAPIKey(r *http.Request) string {
	// Try x-api-key first
	if key := r.Header.Get("x-api-key"); key != "" {
		return key
	}

	// Try Authorization: Bearer
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}

	return ""
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="copilot-proxy-go"`)
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": "Unauthorized",
			"type":    "authentication_error",
		},
	})
}
