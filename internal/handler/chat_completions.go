package handler

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

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
	defaultHandler.ChatCompletions(w, r)
}

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	tracked := trackRequest(w, r, "chat_completions", h.metrics)
	defer tracked.Finish()
	w = tracked.Writer
	rec := tracked.Record
	rec.Backend = "chat_completions"
	rec.RequestType = "normal"

	// Read the raw body up front so we can translate Claude Code-style model
	// names (e.g. "claude-opus-4-7") to Copilot IDs (e.g. "claude-opus-4.7")
	// before service-layer patching kicks in (max_tokens auto-fill needs the
	// resolved name to look the model up).
	raw, err := readRequestBody(w, r)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	raw, _ = RewriteModelInBody(raw)

	body, isStream, isAgent, err := service.ParseAndPatchChatCompletionWithModels(bytes.NewReader(raw), h.state)
	if err != nil {
		api.ForwardError(w, api.InvalidRequest("invalid request body", err))
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
	rec.Model = modelName
	rec.RoutedModel = modelName
	rec.Initiator = initiatorStr(isAgent)
	rec.Streaming = isStream

	resp, err := h.copilot.ProxyChatCompletionEx(r.Context(), body, isAgent, false)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	if isStream {
		streamSSE(w, resp.Body, rec)
	} else {
		forwardJSON(w, resp, rec)
	}
}

// streamSSE proxies an SSE stream from the Copilot API to the client.
func streamSSE(w http.ResponseWriter, body io.Reader, rec *state.RequestRecord) {
	flusher, err := beginSSE(w)
	if err != nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	scanner := bufio.NewScanner(body)
	// Increase buffer size for large SSE events
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if data, ok := strings.CutPrefix(line, "data: "); ok && data != "[DONE]" {
			var chunk ChatCompletionChunk
			if json.Unmarshal([]byte(data), &chunk) == nil {
				captureChatUsage(rec, chunk.Usage)
			}
		}
		fmt.Fprintf(w, "%s\n", line)
		// Flush after empty lines (SSE event boundary)
		if line == "" {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		rec.Error = err.Error()
		slog.Error("SSE stream error", "error", err)
	}
}

// forwardJSON forwards a non-streaming JSON response.
func forwardJSON(w http.ResponseWriter, resp *http.Response, rec *state.RequestRecord) {
	var captured bytes.Buffer
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, io.TeeReader(resp.Body, &captured))
	var result ChatCompletionResponse
	if json.Unmarshal(captured.Bytes(), &result) == nil {
		captureChatUsage(rec, result.Usage)
	}
}

func captureChatUsage(rec *state.RequestRecord, usage *ChatCompletionUsage) {
	if usage == nil {
		return
	}
	rec.InputTokens = int64(usage.PromptTokens)
	rec.OutputTokens = int64(usage.CompletionTokens)
	if usage.PromptTokensDetails != nil {
		rec.CachedTokens = int64(usage.PromptTokensDetails.CachedTokens)
	}
}
