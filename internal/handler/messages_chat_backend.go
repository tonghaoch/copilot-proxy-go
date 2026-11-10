package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

var errFinalChatUsage = errors.New("final chat usage received")

const finalChatUsageWait = 2 * time.Second

func (h *Handler) handleWithChatCompletions(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, forceAgent bool, rec *state.RequestRecord) {
	ccReq, err := h.anthropic.ToChat(req, h.config.ExtraPrompt(normalizeModelName(req.Model)))
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
	resp, err := h.copilot.ProxyChatCompletionEx(r.Context(), body, isAgent, vision)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()
	if req.Stream {
		h.streamChatToAnthropic(w, resp, ccReq.Model, rec)
	} else {
		h.nonStreamChatToAnthropic(w, resp, rec)
	}
}

func (h *Handler) nonStreamChatToAnthropic(w http.ResponseWriter, resp *http.Response, rec *state.RequestRecord) {
	var ccResp ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&ccResp); err != nil {
		api.ForwardError(w, err)
		return
	}
	captureChatUsage(rec, ccResp.Usage)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.anthropic.FromChat(&ccResp))
}

func (h *Handler) streamChatToAnthropic(w http.ResponseWriter, resp *http.Response, model string, rec *state.RequestRecord) {
	flusher, err := beginSSE(w)
	if err != nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	streamState := NewAnthropicStreamState(model)
	var terminal []SSEEvent
	var usageTimer *time.Timer
	var usageTimedOut atomic.Bool
	finalUsage := false
	writeEvents := func(events []SSEEvent) error {
		for _, event := range events {
			if event.Event == "message_delta" || event.Event == "message_stop" {
				terminal = append(terminal, event)
				continue
			}
			if err := writeSSE(w, flusher, event.Event, event.Data); err != nil {
				return err
			}
		}
		return nil
	}
	streamErr := h.streams.Read(resp.Body, func(eventType, data string) error {
		var chunk ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return err
		}
		if len(chunk.Choices) > 0 {
			finalUsage = false
		}
		if chunk.Usage != nil && (len(chunk.Choices) == 0 || chunk.Choices[0].FinishReason != nil) {
			finalUsage = true
		}
		if err := writeEvents(streamState.TranslateChunk(&chunk)); err != nil {
			return err
		}
		if streamState.IsComplete() {
			if finalUsage {
				return errFinalChatUsage
			}
			if usageTimer == nil {
				usageTimer = time.AfterFunc(finalChatUsageWait, func() {
					usageTimedOut.Store(true)
					resp.Body.Close()
				})
			}
		}
		return nil
	})
	if usageTimer != nil {
		usageTimer.Stop()
	}
	if errors.Is(streamErr, errFinalChatUsage) {
		streamErr = nil
	} else if usageTimedOut.Load() {
		slog.Warn("chat stream ended without final usage after finish_reason")
		streamErr = nil
	}
	if streamErr != nil {
		rec.Error = streamErr.Error()
		slog.Error("streaming error", "error", streamErr)
		writeSSEError(w, flusher, streamErr.Error())
	}
	// Upstream can stop before finish_reason; end the message ourselves.
	if !streamState.IsComplete() {
		if err := writeEvents(streamState.Finish("end_turn")); err != nil {
			rec.Error = err.Error()
		}
	}
	for _, event := range terminal {
		if delta, ok := event.Data.(MessageDeltaEvent); ok {
			delta.Usage = streamState.deltaUsage()
			event.Data = delta
		}
		if err := writeSSE(w, flusher, event.Event, event.Data); err != nil {
			rec.Error = err.Error()
			break
		}
	}
	input, output, cached := streamState.TokenCounts()
	rec.InputTokens, rec.OutputTokens, rec.CachedTokens = int64(input), int64(output), int64(cached)
}
