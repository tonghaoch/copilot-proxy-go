package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/middleware"
)

// Options configures the server behavior.
type Options struct {
	Port             int
	ManualApprove    bool
	RateLimitSeconds int
	RateLimitWait    bool
}

// New creates a new HTTP server with all routes and middleware configured.
func New(opts Options) *http.Server {
	r := chi.NewRouter()

	// Core middleware
	r.Use(chimw.RealIP)
	r.Use(chimw.RequestID)
	r.Use(requestLogger)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
	r.Use(chimw.Recoverer)

	// API key authentication
	r.Use(middleware.Auth)

	// Rate limiting (if configured)
	if opts.RateLimitSeconds > 0 {
		rl := middleware.NewRateLimiter(opts.RateLimitSeconds, opts.RateLimitWait)
		r.Use(rl.Middleware)
		slog.Info("rate limiting enabled", "seconds", opts.RateLimitSeconds, "wait", opts.RateLimitWait)
	}

	// Manual approval (if enabled)
	if opts.ManualApprove {
		r.Use(middleware.ManualApproval)
		slog.Info("manual approval enabled")
	}

	// Routes
	r.Get("/", handler.Health)
	r.Get("/token", handler.Token)
	r.Get("/usage", handler.Usage)
	r.Get("/dashboard", handler.Dashboard)

	// Models
	r.Get("/models", handler.Models)
	r.Get("/v1/models", handler.Models)

	// Chat Completions
	r.Post("/chat/completions", handler.ChatCompletions)
	r.Post("/v1/chat/completions", handler.ChatCompletions)

	// Messages (Anthropic-compatible)
	r.Post("/v1/messages", handler.Messages)
	r.Post("/v1/messages/count_tokens", handler.CountTokens)

	// Responses (OpenAI Responses API)
	r.Post("/responses", handler.Responses)
	r.Post("/v1/responses", handler.Responses)

	// Embeddings
	r.Post("/embeddings", handler.Embeddings)
	r.Post("/v1/embeddings", handler.Embeddings)

	addr := fmt.Sprintf(":%d", opts.Port)
	slog.Info("server starting", "address", addr)

	return &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}
}

// requestLogger is a simple request logging middleware.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration", time.Since(start).String(),
		)
	})
}
