package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// streamResponsesPassthrough forwards Responses SSE events, applying stream
// ID synchronization to fix @ai-sdk/openai crashes.
func streamResponsesPassthrough(w http.ResponseWriter, resp *http.Response, rec *state.RequestRecord) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	sync := NewStreamIDSync()
	if err := readSSE(resp.Body, func(eventType, data string) error {
		captureResponsesStreamUsage(data, rec)
		data = sync.Process(eventType, data)
		if eventType != "" {
			if _, err := io.WriteString(w, "event: "+eventType+"\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "data: "+data+"\n\n"); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}); err != nil {
		slog.Error("responses passthrough streaming error", "error", err)
	}
}

func captureResponsesStreamUsage(data string, rec *state.RequestRecord) {
	var event ResponsesStreamEvent
	if json.Unmarshal([]byte(data), &event) != nil || len(event.Response) == 0 {
		return
	}
	var response ResponsesResult
	if json.Unmarshal(event.Response, &response) == nil {
		captureResponsesUsage(rec, response.Usage)
	}
}

func captureResponsesUsage(rec *state.RequestRecord, usage *ResponsesUsage) {
	if usage == nil {
		return
	}
	rec.InputTokens = int64(usage.InputTokens)
	rec.OutputTokens = int64(usage.OutputTokens)
	if usage.InputTokensDetails != nil {
		rec.CachedTokens = int64(usage.InputTokensDetails.CachedTokens)
	}
}
