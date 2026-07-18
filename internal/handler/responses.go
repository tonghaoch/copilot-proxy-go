package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/logger"
)

// Responses handles POST /responses and /v1/responses — OpenAI Responses API passthrough.
func Responses(w http.ResponseWriter, r *http.Request) {
	defaultHandler.Responses(w, r)
}

func (h *Handler) Responses(w http.ResponseWriter, r *http.Request) {
	tracked := trackRequest(w, r, "responses", h.metrics)
	defer tracked.Finish()
	w = tracked.Writer
	rec := tracked.Record
	rec.Backend = "responses"
	rec.RequestType = "normal"
	var payload map[string]any
	body, err := decodeRequestBody(w, r, &payload)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	modelID, _ := payload["model"].(string)
	if resolved := ResolveCopilotModel(modelID); resolved != modelID {
		modelID = resolved
		payload["model"] = resolved
	}
	rec.Model = modelID
	rec.RoutedModel = modelID

	model := h.state.FindModel(modelID)
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
		if h.config.Snapshot().UseFunctionApplyPatch {
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
	rec.Initiator = initiatorStr(isAgent)
	rec.HasVision = vision
	rec.Streaming = isStream
	logger.For("responses").Log("model=%s stream=%v initiator=%s vision=%v", modelID, isStream, initiatorStr(isAgent), vision)
	slog.Info("responses passthrough", "model", modelID, "stream", isStream,
		"initiator", initiatorStr(isAgent), "vision", vision)

	body, err = json.Marshal(payload)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	resp, err := h.copilot.ProxyResponses(r.Context(), body, isAgent, vision)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	if isStream {
		h.streamResponsesPassthrough(w, resp, rec)
	} else {
		var captured bytes.Buffer
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.TeeReader(resp.Body, &captured))
		var result ResponsesResult
		if json.Unmarshal(captured.Bytes(), &result) == nil {
			captureResponsesUsage(rec, result.Usage)
		}
	}
}
