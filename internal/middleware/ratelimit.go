package middleware

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// RateLimiter enforces a minimum interval between requests.
type RateLimiter struct {
	mu           sync.Mutex
	seconds      int
	wait         bool
	lastRequest  time.Time
}

// NewRateLimiter creates a rate limiter with the given interval in seconds.
// If wait is true, requests will sleep instead of being rejected with 429.
func NewRateLimiter(seconds int, wait bool) *RateLimiter {
	return &RateLimiter{
		seconds: seconds,
		wait:    wait,
	}
}

// Middleware returns an HTTP middleware that enforces the rate limit.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rl.mu.Lock()

		if rl.lastRequest.IsZero() {
			// First request: always pass through
			rl.lastRequest = time.Now()
			rl.mu.Unlock()
			next.ServeHTTP(w, r)
			return
		}

		elapsed := time.Since(rl.lastRequest)
		cooldown := time.Duration(rl.seconds) * time.Second

		if elapsed < cooldown {
			remaining := cooldown - elapsed

			if rl.wait {
				rl.mu.Unlock()
				time.Sleep(remaining)
				rl.mu.Lock()
				rl.lastRequest = time.Now()
				rl.mu.Unlock()
				next.ServeHTTP(w, r)
				return
			}

			rl.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", remaining.String())
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{
					"message": "Rate limit exceeded",
					"type":    "rate_limit_error",
				},
			})
			return
		}

		rl.lastRequest = time.Now()
		rl.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}
