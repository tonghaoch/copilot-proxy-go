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
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

var errNativeMessageStop = errors.New("native stream terminal event")

// handleWithMessagesAPI forwards an Anthropic request to Copilot's native
// Messages API, applying necessary filtering and header adjustments.
// rawBody is the original request bytes to preserve unknown fields.
func (h *Handler) handleWithMessagesAPI(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, forceAgent bool, rawBody []byte, rec *state.RequestRecord) {
	// Parse into map to preserve unknown fields
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		api.ForwardError(w, err)
		return
	}

	// Small-model routing only updated req.Model; rawBody still has the original.
	if req.Model != "" {
		payload["model"] = req.Model
	}

	// Strip tools Copilot rejects (e.g. image_generation)
	stripUnsupportedToolsInMap(payload)

	// Filter thinking blocks in assistant messages
	filterThinkingBlocksInMap(payload)

	// Strip unsupported "scope" from cache_control (Claude Code 2.1.89+)
	stripCacheControlScope(payload)
	if h.forceCacheTTL != "" {
		forceCacheTTL(payload, h.forceCacheTTL)
	} else {
		applyCacheTTL(payload, h.config.Snapshot().EffectiveCacheTTL())
	}

	// Set up adaptive thinking if supported. Returns the effort the user asked
	// for (post-clamp); used by the 400-retry path to pick a fallback when
	// Copilot rejects it.
	requestedEffort := applyAdaptiveThinkingInMap(payload, req, h.state, h.config)
	if v := thinkingRewriteHeader(req, payload); v != "" {
		w.Header().Set("X-Copilot-Proxy-Thinking", v)
	}

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

	rec.UpstreamAttempted = true
	resp, err := h.copilot.ProxyMessages(r.Context(), body, betaHeader, vision, isAgent)
	if err != nil && strings.HasPrefix(strings.ToLower(r.UserAgent()), "claude-cli/") && rejectsSafeguards(err, payload) {
		delete(payload, "safeguards")
		betaHeader = removeBetaHeader(betaHeader, "dangerous-tool-use-2026-09-03")
		body, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			api.ForwardError(w, marshalErr)
			return
		}
		slog.Warn("Copilot rejected server-side safeguards; Claude Code must use its own classifier")
		w.Header().Set("X-Copilot-Proxy-Safeguards", "client-classifier")
		resp, err = h.copilot.ProxyMessages(r.Context(), body, betaHeader, vision, isAgent)
	}
	if err != nil {
		// If Copilot rejected our effort, cache the supported list, rewrite
		// the payload, and retry exactly once. Anything else propagates.
		if retried, retryResp, retryErr := maybeRetryWithFallbackEffort(r.Context(), h.copilot, err, payload, req, requestedEffort, betaHeader, vision, isAgent); retried {
			if retryErr != nil {
				markUpstreamFailure(rec, retryErr)
				api.ForwardError(w, retryErr)
				return
			}
			resp = retryResp
		} else {
			markUpstreamFailure(rec, err)
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

		if err := h.streams.Read(resp.Body, func(eventType, data string) error {
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
			if eventType == "message_stop" || eventType == "error" {
				return errNativeMessageStop
			}
			return nil
		}); err != nil && !errors.Is(err, errNativeMessageStop) {
			rec.Error = err.Error()
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
		var anthResp struct {
			Usage *nativeTokenUsage `json:"usage"`
		}
		if json.Unmarshal(buf.Bytes(), &anthResp) == nil {
			captureNativeUsage(rec, anthResp.Usage, true)
		}
	}
}

func rejectsSafeguards(err error, payload map[string]any) bool {
	if _, ok := payload["safeguards"]; !ok {
		return false
	}
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(httpErr.Body)
	return strings.Contains(body, "safeguards") && strings.Contains(body, "extra inputs are not permitted")
}

func removeBetaHeader(header, removed string) string {
	var kept []string
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part != "" && part != removed {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, ",")
}

type nativeTokenUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5m *int `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h *int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

func captureNativeUsage(rec *state.RequestRecord, usage *nativeTokenUsage, final bool) {
	if usage == nil {
		return
	}
	if usage.InputTokens != nil {
		rec.InputTokens = int64(*usage.InputTokens)
		rec.InputUsageKnown = true
	}
	if usage.OutputTokens != nil {
		rec.OutputTokens = int64(*usage.OutputTokens)
		if final {
			rec.OutputUsageKnown = true
		}
	} else if final {
		rec.OutputUsageKnown = false
	}
	if usage.CacheReadInputTokens != nil {
		rec.CachedTokens = int64(*usage.CacheReadInputTokens)
	}
	if usage.CacheCreationInputTokens != nil {
		if rec.CacheCreationTokens != int64(*usage.CacheCreationInputTokens) {
			rec.CacheWriteDetailsKnown = false
		}
		rec.CacheCreationTokens = int64(*usage.CacheCreationInputTokens)
	}
	if usage.CacheCreation != nil && usage.CacheCreation.Ephemeral5m != nil && usage.CacheCreation.Ephemeral1h != nil {
		rec.CacheWrite5mTokens = int64(*usage.CacheCreation.Ephemeral5m)
		rec.CacheWrite1hTokens = int64(*usage.CacheCreation.Ephemeral1h)
		rec.CacheWriteDetailsKnown = true
	}
}

// captureNativeTokens extracts token counts from native Anthropic SSE events
// (message_start for input tokens, message_delta for output tokens).
func captureNativeTokens(eventType, data string, rec *state.RequestRecord) {
	switch eventType {
	case "message_start":
		var evt struct {
			Message struct {
				Usage *nativeTokenUsage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(data), &evt) == nil {
			captureNativeUsage(rec, evt.Message.Usage, false)
		}
	case "message_delta":
		var evt struct {
			Usage *nativeTokenUsage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &evt) == nil {
			captureNativeUsage(rec, evt.Usage, true)
		}
	case "content_block_delta":
		rec.OutputUsageKnown = false
	}
}

// filterThinkingBlocksInMap drops thinking blocks Copilot rejects from assistant
// messages, editing the decoded map so fields ContentBlock omits survive.
func filterThinkingBlocksInMap(payload map[string]any) {
	messages, ok := payload["messages"].([]any)
	if !ok {
		return
	}

	for _, msgAny := range messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role != "assistant" {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}

		kept := make([]any, 0, len(blocks))
		for _, blockAny := range blocks {
			block, ok := blockAny.(map[string]any)
			if !ok {
				kept = append(kept, blockAny)
				continue
			}
			if blockType, _ := block["type"].(string); blockType == "thinking" && !isForwardableThinking(block) {
				continue
			}
			kept = append(kept, blockAny)
		}

		if len(kept) == 0 {
			kept = []any{map[string]any{"type": "text", "text": ""}}
		}
		msg["content"] = kept
	}
}

// isForwardableThinking reports whether Copilot will accept a thinking block.
func isForwardableThinking(block map[string]any) bool {
	thinking, _ := block["thinking"].(string)
	if thinking == "" || thinking == "Thinking..." {
		return false
	}
	signature, _ := block["signature"].(string)
	if signature == "" || strings.Contains(signature, "@") {
		return false
	}
	return true
}

// applyAdaptiveThinkingInMap modifies the thinking config and output_config
// in the map representation. Only applies when the model supports adaptive
// thinking. Returns the effort actually written to payload (after consulting
// the session effort cache); "" when no effort was set.
//
// The configured effort is only a default; see clientRequestedEffort.
func applyAdaptiveThinkingInMap(payload map[string]any, req *AnthropicRequest, models ModelStore, cfg RuntimeConfig) string {
	model := models.FindModel(req.Model)
	if model == nil || !model.Capabilities.Supports.AdaptiveThinking {
		return ""
	}

	// Explicitly disabled — forward the client's request untouched.
	if req.Thinking != nil && req.Thinking.Type == "disabled" {
		return ""
	}

	payload["thinking"] = map[string]string{"type": "adaptive"}

	requested := clientRequestedEffort(req)
	if requested == "" {
		requested = cfg.ReasoningEffort(normalizeModelName(req.Model))
	}
	if requested == "" {
		return ""
	}

	// Downgrade if Copilot already rejected this effort earlier in the session.
	effective := clampEffort(req.Model, requested)
	if effective != requested {
		slog.Debug("effort clamped from cache", "model", req.Model, "requested", requested, "using", effective)
	}
	setOutputConfigEffort(payload, effective)
	return effective
}

// thinkingRewriteHeader describes a thinking.type=enabled request that was
// rewritten to adaptive, so the client can tell its budget was not used.
// Copilot rejects enabled for some models and accepts adaptive. Returns "" when
// no rewrite happened.
func thinkingRewriteHeader(req *AnthropicRequest, payload map[string]any) string {
	if req.Thinking == nil || req.Thinking.Type != "enabled" {
		return ""
	}
	t, ok := payload["thinking"].(map[string]string)
	if !ok || t["type"] != "adaptive" {
		return ""
	}
	return "enabled-rewritten-to-adaptive"
}

// clientRequestedEffort reads the effort the caller asked for, or "" if unset.
func clientRequestedEffort(req *AnthropicRequest) string {
	if req.OutputConfig != nil && req.OutputConfig.Effort != "" {
		return req.OutputConfig.Effort
	}
	if req.Thinking != nil && req.Thinking.BudgetTokens > 0 {
		return effortFromThinkingBudget(req.Thinking.BudgetTokens)
	}
	return ""
}

// effortFromThinkingBudget maps a budget onto an effort, tracking Claude Code's
// think tiers (~4k, ~10k, ~32k). Only older clients send budgets.
func effortFromThinkingBudget(budget int) string {
	switch {
	case budget >= 32000:
		return "max"
	case budget >= 10000:
		return "high"
	case budget >= 4000:
		return "medium"
	default:
		return "low"
	}
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
func maybeRetryWithFallbackEffort(ctx context.Context, client CopilotClient, origErr error, payload map[string]any, req *AnthropicRequest, requestedEffort, betaHeader string, vision, isAgent bool) (retried bool, retryResp *http.Response, retryErr error) {
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
	resp, err := client.ProxyMessages(ctx, body, betaHeader, vision, isAgent)
	return true, resp, err
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
		// Copilot honours cache_control ttl natively and rejects this beta.
		"extended-cache-ttl-2025-04-11": true,
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
