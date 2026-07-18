package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func (h *Handler) handleWithResponsesAPI(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, forceAgent bool, rec *state.RequestRecord) {
	payload, err := h.anthropic.ToResponses(req, config.GetExtraPrompt(normalizeModelName(req.Model)))
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	isAgent := forceAgent || isInitiatorAgent(req.Messages)
	vision := hasVision(req.Messages)
	slog.Info("responses API backend", "model", payload.Model, "stream", payload.Stream,
		"initiator", initiatorStr(isAgent), "vision", vision)
	resp, err := h.copilot.ProxyResponses(r.Context(), body, isAgent, vision)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()
	if req.Stream {
		h.streamResponsesToAnthropic(w, resp, payload.Model, rec)
	} else {
		h.nonStreamResponsesToAnthropic(w, resp, rec)
	}
}

func (h *Handler) nonStreamResponsesToAnthropic(w http.ResponseWriter, resp *http.Response, rec *state.RequestRecord) {
	var result ResponsesResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		api.ForwardError(w, err)
		return
	}
	if result.Usage != nil {
		rec.InputTokens = int64(result.Usage.InputTokens)
		rec.OutputTokens = int64(result.Usage.OutputTokens)
		if result.Usage.InputTokensDetails != nil {
			rec.CachedTokens = int64(result.Usage.InputTokensDetails.CachedTokens)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.anthropic.FromResponses(&result))
}

func (h *Handler) streamResponsesToAnthropic(w http.ResponseWriter, resp *http.Response, model string, rec *state.RequestRecord) {
	flusher, err := beginSSE(w)
	if err != nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	streamState := NewResponsesStreamState(model)
	if err := h.streams.Read(resp.Body, func(eventType, data string) error {
		events, err := streamState.TranslateEvent(eventType, data)
		if err != nil {
			return err
		}
		for _, event := range events {
			if err := writeSSE(w, flusher, event.Event, event.Data); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		slog.Error("responses streaming error", "error", err)
		writeSSEError(w, flusher, err.Error())
	}
	if !streamState.IsComplete() {
		writeSSEError(w, flusher, "Stream ended unexpectedly without completion event")
	}
	input, output, cached := streamState.TokenCounts()
	rec.InputTokens, rec.OutputTokens, rec.CachedTokens = int64(input), int64(output), int64(cached)
}
