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
	Host             string
	Port             int
	ManualApprove    bool
	RateLimitSeconds int
	RateLimitWait    bool
}

// New creates a new HTTP server with all routes and middleware configured.
func New(opts Options) *http.Server {
	return NewWithHandler(opts, handler.New(handler.Dependencies{}))
}

// NewWithHandler creates a server with explicit endpoint dependencies.
func NewWithHandler(opts Options, endpoints *handler.Handler) *http.Server {
	r := chi.NewRouter()

	// Core middleware
	r.Use(chimw.RequestID)
	r.Use(requestIDHeader)
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
		slog.Info(fmt.Sprintf("rate limiting enabled: %ds (wait=%v)", opts.RateLimitSeconds, opts.RateLimitWait))
	}

	// Manual approval (if enabled)
	if opts.ManualApprove {
		r.Use(middleware.ManualApproval)
		slog.Info("manual approval enabled")
	}

	// Routes
	r.Get("/", endpoints.Health)
	r.Get("/token", endpoints.Token)
	r.Get("/usage", endpoints.Usage)
	r.Get("/dashboard", endpoints.Dashboard)
	r.Get("/api/stats", endpoints.Stats)

	// Models
	r.Get("/models", endpoints.Models)
	r.Get("/v1/models", endpoints.Models)

	// Chat Completions
	r.Post("/chat/completions", endpoints.ChatCompletions)
	r.Post("/v1/chat/completions", endpoints.ChatCompletions)

	// Messages (Anthropic-compatible)
	r.Post("/v1/messages", endpoints.Messages)
	r.Post("/v1/messages/count_tokens", endpoints.CountTokens)

	// Responses (OpenAI Responses API)
	r.Post("/responses", endpoints.Responses)
	r.Post("/v1/responses", endpoints.Responses)

	// Embeddings
	r.Post("/embeddings", endpoints.Embeddings)
	r.Post("/v1/embeddings", endpoints.Embeddings)

	host := opts.Host
	if host == "" {
		host = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", host, opts.Port)

	return &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       5 * time.Minute,
		// Streaming responses may legitimately outlive a fixed write deadline.
		// Shutdown still bounds server termination at the application layer.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
}

func requestIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestID := chimw.GetReqID(r.Context()); requestID != "" {
			w.Header().Set("X-Request-ID", requestID)
		}
		next.ServeHTTP(w, r)
	})
}

// requestLogger is a simple request logging middleware.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request completed",
			"request_id", chimw.GetReqID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
