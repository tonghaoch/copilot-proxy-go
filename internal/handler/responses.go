package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/logger"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// Responses handles POST /responses and /v1/responses — OpenAI Responses API passthrough.
func Responses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		api.ForwardError(w, &api.HTTPError{
			Message:    "invalid request body",
			StatusCode: http.StatusBadRequest,
		})
		return
	}

	// Get model and validate support
	modelID, _ := payload["model"].(string)
	model := state.Global.FindModel(modelID)
	if model == nil || !isResponsesSupported(model) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"message": "This model does not support the responses endpoint",
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// apply_patch tool conversion: custom → function (if enabled in config)
	if tools, ok := payload["tools"].([]any); ok {
		if config.Get().UseFunctionApplyPatch {
			payload["tools"] = convertApplyPatchTools(tools)
		}
		// Remove web_search tools
		payload["tools"] = removeWebSearchTools(payload["tools"].([]any))
	}

	// Nullify service_tier
	payload["service_tier"] = nil

	// Detect vision and initiator
	isStream, _ := payload["stream"].(bool)
	vision := detectVisionInResponses(payload)
	isAgent := detectAgentInResponses(payload)

	logger.For("responses").Log("model=%s stream=%v initiator=%s vision=%v", modelID, isStream, initiatorStr(isAgent), vision)
	slog.Info("responses passthrough", "model", modelID, "stream", isStream,
		"initiator", initiatorStr(isAgent), "vision", vision)

	// Re-marshal
	body, err = json.Marshal(payload)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	resp, err := service.ProxyResponses(body, isAgent, vision)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	if isStream {
		streamResponsesPassthrough(w, resp)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}

	// Record metrics
	state.Metrics.RecordRequest(state.RequestRecord{
		Timestamp:   start,
		Endpoint:    "responses",
		Model:       modelID,
		RoutedModel: modelID,
		Backend:     "responses",
		RequestType: "normal",
		Initiator:   initiatorStr(isAgent),
		HasVision:   vision,
		Streaming:   isStream,
		LatencyMs:   time.Since(start).Milliseconds(),
		StatusCode:  resp.StatusCode,
	})
}

// streamResponsesPassthrough forwards Responses SSE events, applying stream
// ID synchronization to fix @ai-sdk/openai crashes.
func streamResponsesPassthrough(w http.ResponseWriter, resp *http.Response) {
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

	readSSE(resp.Body, func(eventType, data string) error {
		// Apply stream ID synchronization
		data = sync.Process(eventType, data)

		if eventType != "" {
			io.WriteString(w, "event: "+eventType+"\n")
		}
		io.WriteString(w, "data: "+data+"\n\n")
		flusher.Flush()
		return nil
	})
}

// convertApplyPatchTools converts apply_patch custom tools to function tools.
func convertApplyPatchTools(tools []any) []any {
	result := make([]any, 0, len(tools))
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			result = append(result, t)
			continue
		}
		toolType, _ := tool["type"].(string)
		toolName, _ := tool["name"].(string)

		if toolType == "custom" && toolName == "apply_patch" {
			result = append(result, map[string]any{
				"type": "function",
				"name": "apply_patch",
				"description": tool["description"],
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"input": map[string]string{
							"type":        "string",
							"description": "The entire contents of the apply_patch command",
						},
					},
					"required": []string{"input"},
				},
				"strict": false,
			})
		} else {
			result = append(result, t)
		}
	}
	return result
}

// removeWebSearchTools filters out web_search tools.
func removeWebSearchTools(tools []any) []any {
	result := make([]any, 0, len(tools))
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			result = append(result, t)
			continue
		}
		if toolType, _ := tool["type"].(string); toolType == "web_search" {
			continue
		}
		result = append(result, t)
	}
	return result
}

// detectVisionInResponses checks input items for image content.
func detectVisionInResponses(payload map[string]any) bool {
	input, ok := payload["input"].([]any)
	if !ok {
		return false
	}
	return containsImageRecursive(input)
}

func containsImageRecursive(items []any) bool {
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "input_image" {
			return true
		}
		// Check nested content
		if content, ok := m["content"].([]any); ok {
			if containsImageRecursive(content) {
				return true
			}
		}
	}
	return false
}

// detectAgentInResponses checks the last input item's role.
func detectAgentInResponses(payload map[string]any) bool {
	input, ok := payload["input"].([]any)
	if !ok || len(input) == 0 {
		return false
	}
	last, ok := input[len(input)-1].(map[string]any)
	if !ok {
		return false
	}
	role, _ := last["role"].(string)
	return role == "assistant" || role == ""
}
