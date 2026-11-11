package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func (h *Handler) handleWithDirectAnthropicAPI(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, rawBody []byte, rec *state.RequestRecord) {
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil || payload == nil {
		api.ForwardError(w, api.InvalidRequest("invalid Anthropic request body", err))
		return
	}
	payload["model"] = req.Model
	stripCacheControlScope(payload)
	if h.forceCacheTTL != "" {
		forceCacheTTL(payload, h.forceCacheTTL)
	} else {
		applyCacheTTL(payload, h.config.Snapshot().EffectiveCacheTTL())
	}
	body, err := json.Marshal(payload)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	slog.Info("messages API (direct Anthropic)", "model", req.Model, "stream", req.Stream)
	rec.UpstreamAttempted = true
	resp, err := h.directAnthropic.ProxyMessages(r.Context(), body, r.Header)
	if err != nil {
		markUpstreamFailure(rec, err)
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()
	copyAnthropicResponseHeaders(w.Header(), resp.Header)
	h.forwardNativeMessagesResponse(w, resp, req.Stream, rec)
}

func copyAnthropicResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		lower := strings.ToLower(key)
		if lower == "request-id" || lower == "x-request-id" || lower == "retry-after" ||
			strings.HasPrefix(lower, "anthropic-ratelimit-") ||
			strings.HasPrefix(lower, "anthropic-fast-") ||
			strings.HasPrefix(lower, "anthropic-priority-") {
			dst[key] = append([]string(nil), values...)
		}
	}
}
