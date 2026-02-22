package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
)

// handleWithMessagesAPI forwards an Anthropic request to Copilot's native
// Messages API, applying necessary filtering and header adjustments.
func handleWithMessagesAPI(w http.ResponseWriter, r *http.Request, req *AnthropicRequest) {
	// Filter thinking blocks in assistant messages
	filteredReq := filterThinkingBlocks(req)

	// Set up adaptive thinking if supported
	applyAdaptiveThinking(filteredReq)

	// Marshal the filtered request
	body, err := json.Marshal(filteredReq)
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
	isAgent := isInitiatorAgent(req.Messages)

	slog.Info("messages API (native)", "model", req.Model, "stream", req.Stream, "vision", vision)

	resp, err := service.ProxyMessages(body, betaHeader, vision, isAgent)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	if req.Stream {
		// Stream passthrough — forward SSE events directly
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		readSSE(resp.Body, func(eventType, data string) error {
			if eventType != "" {
				io.WriteString(w, "event: "+eventType+"\n")
			}
			io.WriteString(w, "data: "+data+"\n\n")
			flusher.Flush()
			return nil
		})
	} else {
		// Non-streaming passthrough
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

// filterThinkingBlocks removes invalid thinking blocks from assistant messages
// before forwarding to the Messages API.
func filterThinkingBlocks(req *AnthropicRequest) *AnthropicRequest {
	// Deep copy the request to avoid mutating the original
	copied := *req
	copied.Messages = make([]AnthropicMsg, len(req.Messages))

	for i, msg := range req.Messages {
		if msg.Role != "assistant" {
			copied.Messages[i] = msg
			continue
		}

		blocks := ParseMessageContent(msg.Content)
		var filtered []ContentBlock
		for _, b := range blocks {
			if b.Type == "thinking" {
				// Filter out:
				// - Empty thinking
				// - "Thinking..." placeholder
				// - Signatures containing @ (Responses API encoding)
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

		filteredJSON, _ := json.Marshal(filtered)
		copied.Messages[i] = AnthropicMsg{
			Role:    msg.Role,
			Content: filteredJSON,
		}
	}

	return &copied
}

// applyAdaptiveThinking modifies the request to use adaptive thinking
// if the model supports it.
func applyAdaptiveThinking(req *AnthropicRequest) {
	// For models that support adaptive thinking, convert the thinking config
	// This is handled by setting the thinking type and output_config
	// The actual model lookup and capability check would use state.Global.FindModel
	// For now, we set adaptive thinking when a thinking budget is present
	if req.Thinking != nil && req.Thinking.BudgetTokens > 0 {
		req.Thinking.Type = "adaptive"
	}
}

// filterBetaHeader removes claude-code-20250219 from the anthropic-beta header.
func filterBetaHeader(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.Split(header, ",")
	var filtered []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "claude-code-20250219" && p != "" {
			filtered = append(filtered, p)
		}
	}
	return strings.Join(filtered, ",")
}

// ProxyMessagesPayload is used to forward the filtered request body
// to Copilot's native messages endpoint. This is a convenience that
// re-encodes the body from the AnthropicRequest struct. For a true
// passthrough (preserving unknown fields), we could use json.RawMessage
// based approach.
func buildMessagesBody(req *AnthropicRequest) []byte {
	body, _ := json.Marshal(req)
	return body
}

// forwardWithModifiedBody sends a request with a modified body while
// preserving the original request structure.
func forwardWithModifiedBody(original []byte, modifications map[string]any) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(original, &payload); err != nil {
		return nil, err
	}
	for k, v := range modifications {
		payload[k] = v
	}
	return json.Marshal(payload)
}

// readBody reads and returns the request body, and also returns a new
// reader for the body so it can be read again.
func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
