package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/logger"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// Responses handles POST /responses and /v1/responses — OpenAI Responses API passthrough.
func Responses(w http.ResponseWriter, r *http.Request) {
	defaultHandler.Responses(w, r)
}

func (h *Handler) Responses(w http.ResponseWriter, r *http.Request) {
	tracked := trackRequest(w, r, "responses", h.metrics)
	defer tracked.Finish()
	if h.budget != nil {
		r = tracked.trackRetries(r)
	}
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
	var model *state.Model
	if h.directAnthropic != nil {
		model = h.directAnthropic.FindModel(ResolveAnthropicModel(modelID))
	}
	direct := model != nil
	if direct {
		modelID = model.ID
		payload["model"] = modelID
		w.Header().Set("X-Copilot-Proxy-Upstream", "anthropic")
	} else {
		if resolved := ResolveCopilotModel(modelID); resolved != modelID {
			modelID = resolved
			payload["model"] = resolved
		}
		model = h.state.FindModel(modelID)
	}
	rec.Model = modelID
	rec.RoutedModel = modelID

	if model == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"message": "unknown model",
				"type":    "invalid_request_error",
			},
		})
		return
	}
	if direct {
		var speed, inferenceGeo string
		for key, dst := range map[string]*string{"speed": &speed, "inference_geo": &inferenceGeo} {
			if value, ok := payload[key]; ok && value != nil {
				text, ok := value.(string)
				if !ok {
					api.ForwardError(w, api.InvalidRequest(key+" must be a string", nil))
					return
				}
				*dst = text
			}
		}
		if !h.admitAnthropicBudget(w, tracked, model, speed, inferenceGeo) {
			return
		}
		h.responsesViaMessages(w, r, payload, modelID, model, rec, true)
		return
	}
	backend := "responses"
	if !isResponsesSupported(model) {
		backend = messagesBackend(model)
	}
	if !h.admitBudget(w, tracked, model, backend) {
		return
	}
	if !isResponsesSupported(model) {
		h.responsesViaMessages(w, r, payload, modelID, model, rec, false)
		return
	}
	w.Header().Set("X-Copilot-Proxy-Upstream", "copilot")

	if tools, ok := payload["tools"].([]any); ok {
		if h.config.Snapshot().UseFunctionApplyPatch {
			payload["tools"] = convertApplyPatchTools(tools)
			tools = payload["tools"].([]any)
		}
		payload["tools"] = convertLocalShellTools(tools)
		payload["tools"] = removeWebSearchTools(payload["tools"].([]any))
	}
	normalizeResponsesToolDescriptions(payload)
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
	rec.UpstreamAttempted = true
	resp, err := h.copilot.ProxyResponses(r.Context(), body, isAgent, vision)
	if err != nil {
		markUpstreamFailure(rec, err)
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
