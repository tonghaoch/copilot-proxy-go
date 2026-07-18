package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// handleWithMessagesAPI forwards an Anthropic request to Copilot's native
// Messages API, applying necessary filtering and header adjustments.
// rawBody is the original request bytes to preserve unknown fields.
func handleWithMessagesAPI(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, forceAgent bool, rawBody []byte, rec *state.RequestRecord) {
	// Parse into map to preserve unknown fields
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		api.ForwardError(w, err)
		return
	}

	// Strip unsupported "scope" from cache_control (Claude Code 2.1.89+)
	stripCacheControlScope(payload)

	// Strip tools Copilot rejects (e.g. image_generation)
	stripUnsupportedToolsInMap(payload)

	// Filter thinking blocks in assistant messages
	filterThinkingBlocksInMap(payload, req)

	// Set up adaptive thinking if supported. Returns the effort the user asked
	// for (post-clamp); used by the 400-retry path to pick a fallback when
	// Copilot rejects it.
	requestedEffort := applyAdaptiveThinkingInMap(payload, req)

	// Marshal the modified payload
	body, err := json.Marshal(payload)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	// Build headers
	betaHeader := r.Header.Get("Anthropic-Beta")
	betaHeader = filterBetaHeader(betaHeader)

	// Auto-inject thinking beta if needed
	if betaHeader == "" && req.Thinking != nil && req.Thinking.BudgetTokens > 0 {
		betaHeader = "interleaved-thinking-2025-05-14"
	}

	// Vision detection
	vision := hasVision(req.Messages)

	// Initiator detection
	isAgent := forceAgent || isInitiatorAgent(req.Messages)

	slog.Info("messages API (native)", "model", req.Model, "stream", req.Stream, "vision", vision)

	resp, err := service.ProxyMessages(r.Context(), body, betaHeader, vision, isAgent)
	if err != nil {
		// If Copilot rejected our effort, cache the supported list, rewrite
		// the payload, and retry exactly once. Anything else propagates.
		if retried, retryResp, retryErr := maybeRetryWithFallbackEffort(r.Context(), err, payload, req, requestedEffort, betaHeader, vision, isAgent); retried {
			if retryErr != nil {
				api.ForwardError(w, retryErr)
				return
			}
			resp = retryResp
		} else {
			api.ForwardError(w, err)
			return
		}
	}
	defer resp.Body.Close()

	if req.Stream {
		// Stream passthrough — forward SSE events, sniff usage data
		flusher, err := beginSSE(w)
		if err != nil {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		if err := readSSE(resp.Body, func(eventType, data string) error {
			// Sniff token counts from native Anthropic events
			captureNativeTokens(eventType, data, rec)

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
			slog.Error("native messages streaming error", "error", err)
		}
	} else {
		// Non-streaming passthrough — tee body to capture usage
		var buf bytes.Buffer
		tee := io.TeeReader(resp.Body, &buf)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, tee)

		// Parse usage from the buffered copy
		var anthResp AnthropicResponse
		if json.Unmarshal(buf.Bytes(), &anthResp) == nil {
			rec.InputTokens = int64(anthResp.Usage.InputTokens)
			rec.OutputTokens = int64(anthResp.Usage.OutputTokens)
			rec.CachedTokens = int64(anthResp.Usage.CacheReadInputTokens)
		}
	}
}

// captureNativeTokens extracts token counts from native Anthropic SSE events
// (message_start for input tokens, message_delta for output tokens).
func captureNativeTokens(eventType, data string, rec *state.RequestRecord) {
	switch eventType {
	case "message_start":
		var evt MessageStartEvent
		if json.Unmarshal([]byte(data), &evt) == nil {
			rec.InputTokens = int64(evt.Message.Usage.InputTokens)
			rec.CachedTokens = int64(evt.Message.Usage.CacheReadInputTokens)
		}
	case "message_delta":
		var evt MessageDeltaEvent
		if json.Unmarshal([]byte(data), &evt) == nil {
			rec.OutputTokens = int64(evt.Usage.OutputTokens)
		}
	}
}

// filterThinkingBlocksInMap filters thinking blocks in assistant messages
// directly in the map representation to preserve unknown fields.
func filterThinkingBlocksInMap(payload map[string]any, req *AnthropicRequest) {
	messages, ok := payload["messages"].([]any)
	if !ok {
		return
	}

	for i, msgAny := range messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}

		// Get the parsed blocks from the structured request
		if i >= len(req.Messages) {
			continue
		}
		blocks := ParseMessageContent(req.Messages[i].Content)
		var filtered []ContentBlock
		for _, b := range blocks {
			if b.Type == "thinking" {
				if b.Thinking == "" || b.Thinking == "Thinking..." {
					continue
				}
				if b.Signature == "" {
					continue
				}
				if strings.Contains(b.Signature, "@") {
					continue
				}
			}
			filtered = append(filtered, b)
		}

		if len(filtered) == 0 {
			filtered = []ContentBlock{{Type: "text", Text: ""}}
		}

		msg["content"] = filtered
	}
}

// applyAdaptiveThinkingInMap modifies the thinking config and output_config
// in the map representation. Only applies when the model supports adaptive
// thinking. Returns the effort actually written to payload (after consulting
// the session effort cache); "" when no effort was set.
func applyAdaptiveThinkingInMap(payload map[string]any, req *AnthropicRequest) string {
	model := state.Global.FindModel(req.Model)
	if model == nil || !model.Capabilities.Supports.AdaptiveThinking {
		return ""
	}

	// Set thinking type to adaptive
	payload["thinking"] = map[string]string{"type": "adaptive"}

	// Set output_config effort, clamping against any cached restrictions for
	// this model. If Copilot has previously rejected our requested effort for
	// this model in the current session, clampEffort downgrades to the
	// closest supported value.
	requested := mapEffort(config.GetReasoningEffort(normalizeModelName(req.Model)))
	if requested == "" {
		return ""
	}
	effective := clampEffort(req.Model, requested)
	if effective != requested {
		slog.Debug("effort clamped from cache", "model", req.Model, "requested", requested, "using", effective)
	}
	setOutputConfigEffort(payload, effective)
	return effective
}

// setOutputConfigEffort writes payload["output_config"]["effort"] = effort,
// preserving any other keys already present in output_config. Both the
// create and update paths use map[string]any so a second call (e.g. during
// retry) can still read what the first call wrote.
func setOutputConfigEffort(payload map[string]any, effort string) {
	if effort == "" {
		return
	}
	existing, ok := payload["output_config"].(map[string]any)
	if !ok {
		existing = map[string]any{}
		payload["output_config"] = existing
	}
	existing["effort"] = effort
}

// maybeRetryWithFallbackEffort inspects an error from ProxyMessages. If it's
// a 400 effort-rejection, it caches the supported list, rewrites payload with
// the closest supported effort, and retries once. Returns:
//
//	retried = false          → caller should forward the original error
//	retried = true, err = nil → retryResp is the new response, use it
//	retried = true, err != nil → retry itself failed; caller should forward err
func maybeRetryWithFallbackEffort(ctx context.Context, origErr error, payload map[string]any, req *AnthropicRequest, requestedEffort, betaHeader string, vision, isAgent bool) (retried bool, retryResp *http.Response, retryErr error) {
	if requestedEffort == "" {
		return false, nil, nil
	}
	var httpErr *api.HTTPError
	if !errors.As(origErr, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		return false, nil, nil
	}
	rejectedModel, supported := parseEffortError(httpErr.Body)
	if rejectedModel == "" || len(supported) == 0 {
		return false, nil, nil
	}
	// Cache under both the inbound model name (what clampEffort sees on the
	// next request from this client) and the Copilot-side name returned in
	// the error (the authoritative ID, in case clients send other aliases).
	effortSupportCache.Set(req.Model, supported)
	if rejectedModel != req.Model {
		effortSupportCache.Set(rejectedModel, supported)
	}

	fallback := pickClosestEffort(requestedEffort, supported)
	if fallback == "" || fallback == requestedEffort {
		// No usable alternative — propagate original error.
		return false, nil, nil
	}
	slog.Warn("effort rejected, falling back",
		"model", req.Model,
		"copilot_model", rejectedModel,
		"requested", requestedEffort,
		"supported", supported,
		"using", fallback,
	)

	setOutputConfigEffort(payload, fallback)
	body, err := json.Marshal(payload)
	if err != nil {
		return true, nil, err
	}
	resp, err := service.ProxyMessages(ctx, body, betaHeader, vision, isAgent)
	return true, resp, err
}

// mapEffort maps config reasoning effort values to Anthropic output_config effort.
func mapEffort(effort string) string {
	switch effort {
	case "xhigh":
		return "max"
	case "none", "minimal":
		return "low"
	default:
		return effort
	}
}

// filterBetaHeader strips beta tokens that Copilot's upstream rejects.
// Notably, "context-1m-2025-08-07" must be dropped — Copilot returns 400
// "unsupported beta header(s)" if it is forwarded. (It no longer affects
// model selection: Copilot ships context size as a per-model capability
// rather than a separate -1m model variant.)
func filterBetaHeader(header string) string {
	if header == "" {
		return ""
	}
	drop := map[string]bool{
		"claude-code-20250219":  true,
		"context-1m-2025-08-07": true,
	}
	parts := strings.Split(header, ",")
	var filtered []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || drop[p] {
			continue
		}
		filtered = append(filtered, p)
	}
	return strings.Join(filtered, ",")
}
