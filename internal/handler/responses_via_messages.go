package handler

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// responsesViaMessages serves a Responses request for a model that only
// supports Messages or Chat Completions. The request is converted to Anthropic
// form, routed like any /v1/messages call, and the reply is converted back.
func (h *Handler) responsesViaMessages(w http.ResponseWriter, r *http.Request, payload map[string]any, modelID string, model *state.Model, rec *state.RequestRecord) {
	req, customTools, err := responsesToAnthropic(payload, modelID)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	body, err := json.Marshal(req)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	rec.Backend = "responses_via_messages"
	slog.Info("responses request routed through Anthropic", "model", modelID, "stream", req.Stream)

	// The Messages backends write Anthropic-format output, so capture it and convert.
	capture := &anthropicCaptureWriter{ResponseWriter: w, header: http.Header{}}
	h.dispatchMessages(capture, r, req, model, false, body, rec)

	if capture.status >= 400 {
		w.Header().Set("Content-Type", capture.header.Get("Content-Type"))
		w.WriteHeader(capture.status)
		w.Write(capture.buf.Bytes())
		return
	}
	if req.Stream {
		writeResponsesStreamFromAnthropic(w, capture.buf.Bytes(), modelID, customTools)
		return
	}
	var resp AnthropicResponse
	if err := json.Unmarshal(capture.buf.Bytes(), &resp); err != nil {
		api.ForwardError(w, err)
		return
	}
	result := anthropicToResponsesResult(&resp, customTools)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// writeResponsesStreamFromAnthropic replays a captured Anthropic SSE stream as
// Responses SSE events.
func writeResponsesStreamFromAnthropic(w http.ResponseWriter, raw []byte, modelID string, customTools map[string]bool) {
	flusher, err := beginSSE(w)
	if err != nil {
		return
	}
	emit := func(eventType string, data any) error {
		return writeSSE(w, flusher, eventType, data)
	}
	stream := newResponsesStreamWriter(emit, modelID, customTools)
	if err := readSSE(bytes.NewReader(raw), stream.Handle); err != nil {
		slog.Error("responses stream translation failed", "error", err)
	}
}

// anthropicCaptureWriter buffers the Messages backend output so it can be
// converted. Status and headers are recorded; body bytes are kept in buf.
type anthropicCaptureWriter struct {
	http.ResponseWriter
	header http.Header
	status int
	buf    bytes.Buffer
}

func (c *anthropicCaptureWriter) Header() http.Header { return c.header }

func (c *anthropicCaptureWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *anthropicCaptureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.buf.Write(p)
}

// Flush exists so the Messages streaming backends, which type-assert for a
// Flusher, still take the streaming path.
func (c *anthropicCaptureWriter) Flush() {}
