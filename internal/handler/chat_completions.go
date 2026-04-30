package handler

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"encoding/json"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/logger"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// ChatCompletions handles POST /chat/completions and /v1/chat/completions.
// It proxies requests to the Copilot API, supporting both streaming and
// non-streaming modes.
func ChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

	// Read the raw body up front so we can translate Claude Code-style model
	// names (e.g. "claude-opus-4-7") to Copilot IDs (e.g. "claude-opus-4.7"
	// or the -1m variant when the context-1m-2025-08-07 beta is set) before
	// service-layer patching kicks in (max_tokens auto-fill needs the
	// resolved name to look the model up).
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	betaHeader := r.Header.Get("Anthropic-Beta")
	raw, _ = RewriteModelInBody(raw, betaHeader)

	body, isStream, isAgent, err := service.ParseAndPatchChatCompletion(bytes.NewReader(raw))
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	logger.For("chat-completions").Log("stream=%v initiator=%s", isStream, initiatorStr(isAgent))

	// Parse model name for metrics
	var parsed struct {
		Model    string `json:"model"`
		Messages []any  `json:"messages"`
	}
	modelName := ""
	if json.Unmarshal(body, &parsed) == nil {
		modelName = parsed.Model
		inputTokens := countStringTokens(string(body))
		slog.Info("chat completion request", "model", parsed.Model,
			"stream", isStream, "initiator", initiatorStr(isAgent),
			"est_input_tokens", inputTokens)
	} else {
		slog.Info("chat completion request", "stream", isStream, "initiator", initiatorStr(isAgent))
	}

	resp, err := service.ProxyChatCompletion(body, isAgent)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	if isStream {
		streamSSE(w, resp.Body)
	} else {
		forwardJSON(w, resp)
	}

	// Record metrics
	state.Metrics.RecordRequest(state.RequestRecord{
		Timestamp:   start,
		Endpoint:    "chat_completions",
		Model:       modelName,
		RoutedModel: modelName,
		Backend:     "chat_completions",
		RequestType: "normal",
		Initiator:   initiatorStr(isAgent),
		Streaming:   isStream,
		LatencyMs:   time.Since(start).Milliseconds(),
		StatusCode:  resp.StatusCode,
	})
}

// streamSSE proxies an SSE stream from the Copilot API to the client.
func streamSSE(w http.ResponseWriter, body io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	scanner := bufio.NewScanner(body)
	// Increase buffer size for large SSE events
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		// Flush after empty lines (SSE event boundary)
		if line == "" {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("SSE stream error", "error", err)
	}
}

// forwardJSON forwards a non-streaming JSON response.
func forwardJSON(w http.ResponseWriter, resp *http.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
