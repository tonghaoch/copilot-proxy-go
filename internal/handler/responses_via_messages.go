package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// responsesViaMessages serves a Responses request for a model that only
// supports Messages or Chat Completions. The request is converted to Anthropic
// form, routed like any /v1/messages call, and the reply is converted back.
func (h *Handler) responsesViaMessages(w http.ResponseWriter, r *http.Request, payload map[string]any, modelID string, model *state.Model, rec *state.RequestRecord) {
	if previous, ok := payload["previous_response_id"]; ok && previous != nil {
		api.ForwardError(w, api.InvalidRequest("previous_response_id is not supported by this model's translated backend", nil))
		return
	}
	req, customTools, err := responsesToAnthropic(payload, modelID)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	if limit := model.Capabilities.Limits.MaxOutputTokens; limit > 0 && req.MaxTokens > limit {
		req.MaxTokens = limit
	}
	if limit := model.Capabilities.Limits.MaxNonStreamingOutput; !req.Stream && limit > 0 && req.MaxTokens > limit {
		req.MaxTokens = limit
	}
	filterUnsupportedTools(req)
	body, err := json.Marshal(req)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	body = addCacheBreakpoints(body)
	rec.Backend = "responses_via_messages"
	forceAgent := detectAgentInResponses(payload)
	rec.Initiator = initiatorStr(forceAgent)
	rec.HasVision = hasVision(req.Messages)
	rec.Streaming = req.Stream
	slog.Info("responses request routed through Anthropic", "model", modelID, "stream", req.Stream)

	capture := &anthropicCaptureWriter{ResponseWriter: w, header: http.Header{}}
	if req.Stream {
		h.streamResponsesViaMessages(w, r, req, model, forceAgent, body, rec, capture, customTools)
		return
	}
	h.dispatchMessages(capture, r, req, model, forceAgent, body, rec)

	if capture.status >= 400 {
		w.WriteHeader(capture.status)
		w.Write(capture.buf.Bytes())
		return
	}
	var resp AnthropicResponse
	if err := json.Unmarshal(capture.buf.Bytes(), &resp); err != nil {
		api.ForwardError(w, err)
		return
	}
	result, err := anthropicToResponsesResult(&resp, customTools)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) streamResponsesViaMessages(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, model *state.Model, forceAgent bool, body []byte, rec *state.RequestRecord, capture *anthropicCaptureWriter, customTools responseTools) {
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	reader, writer := io.Pipe()
	capture.stream = writer
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var flusher http.Flusher
	emit := func(eventType string, data any) error {
		if flusher == nil {
			var err error
			flusher, err = beginSSE(w)
			if err != nil {
				return err
			}
		}
		return writeSSE(w, flusher, eventType, data)
	}
	stream := newResponsesStreamWriter(emit, req.Model, customTools)
	done := make(chan error, 1)
	go func() {
		err := readSSE(reader, stream.Handle)
		reader.CloseWithError(err)
		if err != nil {
			cancel()
		}
		done <- err
	}()
	h.dispatchMessages(capture, r.WithContext(ctx), req, model, forceAgent, body, rec)
	writer.Close()
	err := <-done
	if capture.status >= 400 {
		w.WriteHeader(capture.status)
		w.Write(capture.buf.Bytes())
		return
	}
	if err != nil {
		rec.Error = err.Error()
		slog.Error("responses stream translation failed", "error", err)
	}
	if stream.finished {
		return
	}
	if err == nil {
		err = errors.New("upstream stream ended without a terminal event")
		rec.Error = err.Error()
	}
	if flusher == nil {
		api.ForwardError(w, &api.HTTPError{StatusCode: http.StatusBadGateway, Message: err.Error()})
		return
	}
	if sendErr := stream.send("error", map[string]any{"code": "stream_error", "message": err.Error()}); sendErr != nil {
		slog.Error("responses stream error could not be sent", "error", sendErr)
	}
}

// anthropicCaptureWriter buffers non-streaming output and forwards SSE to a pipe.
type anthropicCaptureWriter struct {
	http.ResponseWriter
	header http.Header
	status int
	buf    bytes.Buffer
	stream io.Writer
}

func (c *anthropicCaptureWriter) Header() http.Header { return c.header }

func (c *anthropicCaptureWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
		for key, values := range c.header {
			c.ResponseWriter.Header()[key] = append([]string(nil), values...)
		}
	}
}

func (c *anthropicCaptureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
	}
	if c.stream != nil && c.status < 400 {
		return c.stream.Write(p)
	}
	return c.buf.Write(p)
}

func (c *anthropicCaptureWriter) Flush() {}

// addCacheBreakpoints marks the system prompt, the last tool and the last
// message with cache_control, as Claude Code does. Responses clients rely on
// automatic prefix caching and send no breakpoints, and Anthropic caches
// nothing without them, so every converted turn would pay full input price.
// Only the native Messages backend reads the raw body; the others ignore it.
func addCacheBreakpoints(body []byte) []byte {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	ephemeral := map[string]any{"type": "ephemeral"}
	if s, ok := payload["system"].(string); ok && s != "" {
		payload["system"] = []any{map[string]any{"type": "text", "text": s, "cache_control": ephemeral}}
	}
	if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
		if last, ok := tools[len(tools)-1].(map[string]any); ok {
			last["cache_control"] = ephemeral
		}
	}
	if msgs, ok := payload["messages"].([]any); ok && len(msgs) > 0 {
		if last, ok := msgs[len(msgs)-1].(map[string]any); ok {
			if blocks, ok := last["content"].([]any); ok && len(blocks) > 0 {
				if block, ok := blocks[len(blocks)-1].(map[string]any); ok {
					block["cache_control"] = ephemeral
				}
			}
		}
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}
