package handler

import (
	"bytes"
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
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		api.ForwardError(w, api.InvalidRequest("invalid request body", err))
		return
	}

	modelID, _ := payload["model"].(string)
	if resolved := ResolveCopilotModel(modelID); resolved != modelID {
		modelID = resolved
		payload["model"] = resolved
	}

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

	if tools, ok := payload["tools"].([]any); ok {
		if config.Get().UseFunctionApplyPatch {
			payload["tools"] = convertApplyPatchTools(tools)
			tools = payload["tools"].([]any)
		}
		payload["tools"] = convertLocalShellTools(tools)
		payload["tools"] = removeWebSearchTools(payload["tools"].([]any))
	}
	payload["service_tier"] = nil

	isStream, _ := payload["stream"].(bool)
	vision := detectVisionInResponses(payload)
	isAgent := detectAgentInResponses(payload)
	logger.For("responses").Log("model=%s stream=%v initiator=%s vision=%v", modelID, isStream, initiatorStr(isAgent), vision)
	slog.Info("responses passthrough", "model", modelID, "stream", isStream,
		"initiator", initiatorStr(isAgent), "vision", vision)

	body, err = json.Marshal(payload)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	resp, err := service.ProxyResponses(r.Context(), body, isAgent, vision)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	rec := state.RequestRecord{
		Timestamp: start, Endpoint: "responses", Model: modelID, RoutedModel: modelID,
		Backend: "responses", RequestType: "normal", Initiator: initiatorStr(isAgent),
		HasVision: vision, Streaming: isStream, StatusCode: resp.StatusCode,
	}
	if isStream {
		streamResponsesPassthrough(w, resp, &rec)
	} else {
		var captured bytes.Buffer
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.TeeReader(resp.Body, &captured))
		var result ResponsesResult
		if json.Unmarshal(captured.Bytes(), &result) == nil {
			captureResponsesUsage(&rec, result.Usage)
		}
	}
	rec.LatencyMs = time.Since(start).Milliseconds()
	state.Metrics.RecordRequest(rec)
}
