package handler

import (
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
	if h.budget != nil {
		r = tracked.trackRetries(r)
	}
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

	if !h.admitBudget(w, tracked, h.state.FindModel(modelName), "chat_completions") {
		return
	}
	if h.budget != nil && isStream {
		body, err = requireChatStreamUsage(body)
		if err != nil {
			api.ForwardError(w, api.InvalidRequest("invalid stream_options", err))
			return
		}
	}
	rec.UpstreamAttempted = true
	resp, err := h.copilot.ProxyChatCompletionEx(r.Context(), body, isAgent, false)
	if err != nil {
		markUpstreamFailure(rec, err)
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

	err = readSSELines(body, func(line string) error {
		if data, ok := strings.CutPrefix(line, "data: "); ok && data != "[DONE]" {
			var chunk ChatCompletionChunk
			if json.Unmarshal([]byte(data), &chunk) == nil {
				captureChatStreamUsage(rec, &chunk)
			}
		}
		if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
			return err
		}
		// Flush after empty lines (SSE event boundary)
		if line == "" {
			flusher.Flush()
		}
		return nil
	})
	if err != nil {
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
	u := usage.toAnthropic()
	rec.InputTokens = int64(u.InputTokens)
	rec.OutputTokens = int64(u.OutputTokens)
	rec.CachedTokens = int64(u.CacheReadInputTokens)
	rec.CacheCreationTokens = int64(u.CacheCreationInputTokens)
	rec.CacheWriteUsageKnown = usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CacheWriteTokens != nil
	rec.InputUsageKnown = usage.inputKnown
	rec.OutputUsageKnown = usage.outputKnown
}

func captureChatStreamUsage(rec *state.RequestRecord, chunk *ChatCompletionChunk) {
	if len(chunk.Choices) > 0 {
		rec.OutputUsageKnown = false
	}
	if chunk.Usage != nil {
		captureChatUsage(rec, chunk.Usage)
		if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason == nil {
			rec.OutputUsageKnown = false
		}
	}
}

func requireChatStreamUsage(body []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	var options map[string]json.RawMessage
	if raw := payload["stream_options"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &options); err != nil {
			return nil, err
		}
	}
	if options == nil {
		options = make(map[string]json.RawMessage)
	}
	options["include_usage"] = json.RawMessage("true")
	rawOptions, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	payload["stream_options"] = rawOptions
	return json.Marshal(payload)
}
