package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// Messages handles POST /v1/messages — the Anthropic-compatible endpoint.
// It routes to one of three backends based on the model's supported_endpoints.
func Messages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	var req AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		api.ForwardError(w, &api.HTTPError{
			Message:    "invalid request body",
			StatusCode: http.StatusBadRequest,
		})
		return
	}

	// Look up the model
	model := state.Global.FindModel(req.Model)

	// Determine backend routing
	if model != nil && isMessagesSupported(model) {
		slog.Info("routing to Messages API", "model", req.Model)
		handleWithMessagesAPI(w, r, &req)
	} else if model != nil && isResponsesSupported(model) {
		slog.Info("routing to Responses API", "model", req.Model)
		handleWithResponsesAPI(w, r, &req)
	} else {
		slog.Info("routing to Chat Completions API", "model", req.Model)
		handleWithChatCompletions(w, r, &req)
	}
}

// handleWithChatCompletions translates Anthropic → OpenAI Chat Completions,
// proxies the request, and translates the response back.
func handleWithChatCompletions(w http.ResponseWriter, r *http.Request, req *AnthropicRequest) {
	// TODO: Phase 3 will add extraPrompt from config
	extraPrompt := ""

	ccReq, err := translateToOpenAI(req, extraPrompt)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	body, err := json.Marshal(ccReq)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	isAgent := isInitiatorAgent(req.Messages)
	vision := hasVision(req.Messages)

	slog.Info("chat completions backend", "model", ccReq.Model, "stream", ccReq.Stream,
		"initiator", initiatorStr(isAgent), "vision", vision)

	resp, err := service.ProxyChatCompletionEx(body, isAgent, vision)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	if req.Stream {
		streamChatToAnthropic(w, resp, ccReq.Model)
	} else {
		nonStreamChatToAnthropic(w, resp)
	}
}

// nonStreamChatToAnthropic translates a non-streaming Chat Completion response
// to Anthropic format.
func nonStreamChatToAnthropic(w http.ResponseWriter, resp *http.Response) {
	var ccResp ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&ccResp); err != nil {
		api.ForwardError(w, err)
		return
	}

	result := translateToAnthropic(&ccResp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// streamChatToAnthropic translates streaming Chat Completion chunks to
// Anthropic SSE events.
func streamChatToAnthropic(w http.ResponseWriter, resp *http.Response, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	streamState := NewAnthropicStreamState(model)

	err := readSSE(resp.Body, func(eventType, data string) error {
		var chunk ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return err
		}

		events := streamState.TranslateChunk(&chunk)
		for _, evt := range events {
			if err := writeSSE(w, flusher, evt.Event, evt.Data); err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		slog.Error("streaming error", "error", err)
		writeSSEError(w, flusher, err.Error())
	}
}

// handleWithResponsesAPI translates Anthropic → Responses API, proxies the
// request, and translates the response back.
func handleWithResponsesAPI(w http.ResponseWriter, r *http.Request, req *AnthropicRequest) {
	// TODO: Phase 3 will add extraPrompt from config and reasoning effort
	extraPrompt := ""

	payload, err := translateToResponses(req, extraPrompt)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	isAgent := isInitiatorAgent(req.Messages)
	vision := hasVision(req.Messages)

	slog.Info("responses API backend", "model", payload.Model, "stream", payload.Stream,
		"initiator", initiatorStr(isAgent), "vision", vision)

	resp, err := service.ProxyResponses(body, isAgent, vision)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	if req.Stream {
		streamResponsesToAnthropic(w, resp, payload.Model)
	} else {
		nonStreamResponsesToAnthropic(w, resp)
	}
}

// nonStreamResponsesToAnthropic translates a non-streaming Responses result
// to Anthropic format.
func nonStreamResponsesToAnthropic(w http.ResponseWriter, resp *http.Response) {
	var result ResponsesResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		api.ForwardError(w, err)
		return
	}

	translated := translateResponsesResultToAnthropic(&result)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(translated)
}

// streamResponsesToAnthropic translates streaming Responses events to
// Anthropic SSE events.
func streamResponsesToAnthropic(w http.ResponseWriter, resp *http.Response, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	streamState := NewResponsesStreamState(model)

	err := readSSE(resp.Body, func(eventType, data string) error {
		events, err := streamState.TranslateEvent(eventType, data)
		if err != nil {
			return err
		}
		for _, evt := range events {
			if err := writeSSE(w, flusher, evt.Event, evt.Data); err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		slog.Error("responses streaming error", "error", err)
		writeSSEError(w, flusher, err.Error())
	}

	// If stream ended without completion, send error
	if !streamState.IsComplete() {
		writeSSEError(w, flusher, "Stream ended unexpectedly without completion event")
	}
}

// initiatorStr is defined in messages_utils.go
