package handler

import (
	"bytes"
	"encoding/json"
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

	// Filter thinking blocks in assistant messages
	filterThinkingBlocksInMap(payload, req)

	// Set up adaptive thinking if supported
	applyAdaptiveThinkingInMap(payload, req)

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

	resp, err := service.ProxyMessages(body, betaHeader, vision, isAgent)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	if req.Stream {
		// Stream passthrough — forward SSE events, sniff usage data
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

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
// in the map representation. Only applies when the model supports adaptive thinking.
func applyAdaptiveThinkingInMap(payload map[string]any, req *AnthropicRequest) {
	model := state.Global.FindModel(req.Model)
	if model == nil || !model.Capabilities.Supports.AdaptiveThinking {
		return
	}

	// Set thinking type to adaptive
	payload["thinking"] = map[string]string{"type": "adaptive"}

	// Set output_config effort
	effort := config.GetReasoningEffort(normalizeModelName(req.Model))
	mapped := mapEffort(effort)
	if mapped != "" {
		payload["output_config"] = map[string]string{"effort": mapped}
	}
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
// Notably, "context-1m-2025-08-07" is consumed by proxy logic (it triggers
// routing to the Copilot -1m model variant) and must not be forwarded —
// Copilot returns 400 "unsupported beta header(s)" otherwise.
func filterBetaHeader(header string) string {
	if header == "" {
		return ""
	}
	drop := map[string]bool{
		"claude-code-20250219":     true,
		"context-1m-2025-08-07":    true,
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

