package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func handleWithChatCompletions(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, forceAgent bool, rec *state.RequestRecord) {
	ccReq, err := translateToOpenAI(req, config.GetExtraPrompt(normalizeModelName(req.Model)))
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	body, err := json.Marshal(ccReq)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	isAgent := forceAgent || isInitiatorAgent(req.Messages)
	vision := hasVision(req.Messages)
	slog.Info("chat completions backend", "model", ccReq.Model, "stream", ccReq.Stream,
		"initiator", initiatorStr(isAgent), "vision", vision)
	resp, err := service.ProxyChatCompletionEx(r.Context(), body, isAgent, vision)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()
	if req.Stream {
		streamChatToAnthropic(w, resp, ccReq.Model, rec)
	} else {
		nonStreamChatToAnthropic(w, resp, rec)
	}
}

func nonStreamChatToAnthropic(w http.ResponseWriter, resp *http.Response, rec *state.RequestRecord) {
	var ccResp ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&ccResp); err != nil {
		api.ForwardError(w, err)
		return
	}
	if ccResp.Usage != nil {
		rec.InputTokens = int64(ccResp.Usage.PromptTokens)
		rec.OutputTokens = int64(ccResp.Usage.CompletionTokens)
		if ccResp.Usage.PromptTokensDetails != nil {
			rec.CachedTokens = int64(ccResp.Usage.PromptTokensDetails.CachedTokens)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(translateToAnthropic(&ccResp))
}

func streamChatToAnthropic(w http.ResponseWriter, resp *http.Response, model string, rec *state.RequestRecord) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	streamState := NewAnthropicStreamState(model)
	if err := readSSE(resp.Body, func(eventType, data string) error {
		var chunk ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return err
		}
		for _, event := range streamState.TranslateChunk(&chunk) {
			if err := writeSSE(w, flusher, event.Event, event.Data); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		slog.Error("streaming error", "error", err)
		writeSSEError(w, flusher, err.Error())
	}
	input, output, cached := streamState.TokenCounts()
	rec.InputTokens, rec.OutputTokens, rec.CachedTokens = int64(input), int64(output), int64(cached)
}
