package handler

import (
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// CountTokensResponse is the response for the count_tokens endpoint.
type CountTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}

// CountTokens handles POST /v1/messages/count_tokens.
// It translates the Anthropic payload to OpenAI format, then estimates
// the token count using a simple heuristic (chars/4 approximation)
// since full tiktoken support requires a separate Go library.
func CountTokens(w http.ResponseWriter, r *http.Request) {
	var req AnthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CountTokensResponse{InputTokens: 1})
		return
	}

	model := state.Global.FindModel(req.Model)

	// Translate to OpenAI format to count
	ccReq, err := translateToOpenAI(&req, "")
	if err != nil {
		slog.Warn("count_tokens translation failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CountTokensResponse{InputTokens: 1})
		return
	}

	anthropicBeta := r.Header.Get("Anthropic-Beta")
	count := estimateTokens(ccReq, model, req.Model, req.Tools, anthropicBeta)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CountTokensResponse{InputTokens: count})
}

// estimateTokens estimates the total token count for a chat completion request.
func estimateTokens(req *ChatCompletionRequest, model *state.Model, modelID string, anthropicTools []AnthropicTool, anthropicBeta string) int {
	total := 0

	// Count message tokens
	for _, msg := range req.Messages {
		total += 4 // message overhead (role, formatting)
		total += countContentTokens(msg.Content)

		if msg.ToolCallID != "" {
			total += countStringTokens(msg.ToolCallID)
		}
		for _, tc := range msg.ToolCalls {
			total += countStringTokens(tc.Function.Name)
			total += countStringTokens(tc.Function.Arguments)
			total += 3 // tool call overhead
		}
		// Skip reasoning_opaque
		if msg.ReasoningText != nil {
			total += countStringTokens(*msg.ReasoningText)
		}
	}

	// Count tool definitions
	if len(req.Tools) > 0 {
		for _, tool := range req.Tools {
			total += countStringTokens(tool.Function.Name)
			total += countStringTokens(tool.Function.Description)
			if tool.Function.Parameters != nil {
				paramJSON, _ := json.Marshal(tool.Function.Parameters)
				total += countStringTokens(string(paramJSON))
			}
			total += 5 // tool definition overhead
		}

		// Tool system prompt adjustment: only when anthropic-beta header is present
		if isClaude(modelID) {
			if anthropicBeta != "" && !isToolOnly(anthropicTools) {
				total += 346
			}
		} else if strings.Contains(strings.ToLower(modelID), "grok") {
			total += 120
		}
	}

	// Image estimation: 85 tokens per image
	for _, msg := range req.Messages {
		total += countImageTokens(msg.Content)
	}

	// Claude 15% inflation
	if isClaude(modelID) {
		total = int(math.Round(float64(total) * 1.15))
	}

	if total < 1 {
		total = 1
	}

	return total
}

// countContentTokens estimates tokens for message content (string or parts array).
func countContentTokens(content any) int {
	switch v := content.(type) {
	case string:
		return countStringTokens(v)
	case []OpenAIContentPart:
		total := 0
		for _, p := range v {
			if p.Text != "" {
				total += countStringTokens(p.Text)
			}
		}
		return total
	case []any:
		// Generic array handling
		data, _ := json.Marshal(v)
		return countStringTokens(string(data))
	default:
		if v == nil {
			return 0
		}
		data, _ := json.Marshal(v)
		return countStringTokens(string(data))
	}
}

// countStringTokens approximates token count using chars/4 heuristic.
// This is a reasonable approximation for most tokenizers.
// A full implementation would use tiktoken-go.
func countStringTokens(s string) int {
	if s == "" {
		return 0
	}
	// ~4 chars per token is a reasonable approximation
	return (len(s) + 3) / 4
}

// countImageTokens counts images in content and returns 85 tokens per image.
func countImageTokens(content any) int {
	parts, ok := content.([]OpenAIContentPart)
	if !ok {
		return 0
	}
	count := 0
	for _, p := range parts {
		if p.Type == "image_url" {
			count += 85
		}
	}
	return count
}

// isToolOnly checks if the tools list consists only of MCP tools (prefixed with
// "mcp__") or a single "Skill" tool. In these cases the standard tool system
// prompt is not injected.
func isToolOnly(tools []AnthropicTool) bool {
	if len(tools) == 0 {
		return false
	}
	// Single "Skill" tool
	if len(tools) == 1 && tools[0].Name == "Skill" {
		return true
	}
	// All tools are MCP tools
	for _, t := range tools {
		if !strings.HasPrefix(t.Name, "mcp__") {
			return false
		}
	}
	return true
}
