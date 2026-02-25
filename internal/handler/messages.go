package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/logger"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// Messages handles POST /v1/messages — the Anthropic-compatible endpoint.
// It routes to one of three backends based on the model's supported_endpoints.
func Messages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

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

	betaHeader := r.Header.Get("Anthropic-Beta")

	// Capture original model before routing
	originalModel := req.Model

	logger.For("messages").Log("model=%s stream=%v initiator=%s", req.Model, req.Stream, initiatorStr(isInitiatorAgent(req.Messages)))

	// Determine request type
	reqType := "normal"
	if isCompactRequest(&req) {
		reqType = "compact"
	} else if isWarmupRequest(&req, betaHeader) {
		reqType = "warmup"
	}

	// Quota optimizations: compact/warmup → small model
	if changed := applySmallModelIfNeeded(&req, betaHeader); changed {
		slog.Info("routed to small model", "model", req.Model, "reason", "compact/warmup")
	}

	// Subagent marker detection → force agent initiator
	subagent := detectSubagentMarker(req.Messages)

	// Build session snapshot
	buildSessionSnapshot(&req, betaHeader, subagent)

	// Tool result + text block merging
	mergeToolResultBlocks(&req)

	// Look up the model
	model := state.Global.FindModel(req.Model)

	// Subagent marker → force agent initiator
	forceAgent := false
	if subagent != nil {
		slog.Debug("subagent detected", "agent_id", subagent.AgentID, "agent_type", subagent.AgentType)
		forceAgent = true
	}

	// Build base record for metrics
	isAgent := forceAgent || isInitiatorAgent(req.Messages)
	rec := &state.RequestRecord{
		Timestamp:   start,
		Endpoint:    "messages",
		Model:       originalModel,
		RoutedModel: req.Model,
		RequestType: reqType,
		Initiator:   initiatorStr(isAgent),
		HasVision:   hasVision(req.Messages),
		Streaming:   req.Stream,
		ToolCount:   len(req.Tools),
	}
	if req.Thinking != nil {
		rec.ThinkingBudget = req.Thinking.BudgetTokens
	}

	// Determine backend routing
	if model != nil && isMessagesSupported(model) {
		slog.Info("routing to Messages API", "model", req.Model)
		rec.Backend = "messages"
		handleWithMessagesAPI(w, r, &req, forceAgent, body, rec)
	} else if model != nil && isResponsesSupported(model) {
		slog.Info("routing to Responses API", "model", req.Model)
		rec.Backend = "responses"
		handleWithResponsesAPI(w, r, &req, forceAgent, rec)
	} else {
		slog.Info("routing to Chat Completions API", "model", req.Model)
		rec.Backend = "chat_completions"
		handleWithChatCompletions(w, r, &req, forceAgent, rec)
	}

	// Record request metrics
	rec.LatencyMs = time.Since(start).Milliseconds()
	rec.StatusCode = 200
	state.Metrics.RecordRequest(*rec)
}

// buildSessionSnapshot extracts session intelligence from the request and
// updates the global metrics session.
func buildSessionSnapshot(req *AnthropicRequest, betaHeader string, subagent *SubagentInfo) {
	systemText := ParseSystemPrompt(req.System)

	snap := state.SessionSnapshot{
		ClaudeMDFiles: extractClaudeMDFiles(systemText),
		BetaFeatures:  betaHeader,
		LastSeen:      time.Now(),
	}

	// Extract tool names
	for _, t := range req.Tools {
		if strings.HasPrefix(t.Name, "mcp__") {
			snap.MCPTools = append(snap.MCPTools, t.Name)
		} else {
			snap.Tools = append(snap.Tools, t.Name)
		}
	}

	// Thinking config
	if req.Thinking != nil {
		snap.ThinkingEnabled = req.Thinking.BudgetTokens > 0 || req.Thinking.Type != "disabled"
		snap.ThinkingBudget = req.Thinking.BudgetTokens
		snap.ThinkingType = req.Thinking.Type
	}

	// Subagent info
	if subagent != nil {
		snap.SubagentInfo = &state.SubagentInfoSnapshot{
			SessionID: subagent.SessionID,
			AgentID:   subagent.AgentID,
			AgentType: subagent.AgentType,
		}
	}

	// User ID from metadata
	if req.Metadata != nil {
		snap.UserID = req.Metadata.UserID
	}

	state.Metrics.UpdateSession(snap)
}

// handleWithChatCompletions translates Anthropic → OpenAI Chat Completions,
// proxies the request, and translates the response back.
func handleWithChatCompletions(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, forceAgent bool, rec *state.RequestRecord) {
	extraPrompt := config.GetExtraPrompt(normalizeModelName(req.Model))

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

	isAgent := forceAgent || isInitiatorAgent(req.Messages)
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
		streamChatToAnthropic(w, resp, ccReq.Model, rec)
	} else {
		nonStreamChatToAnthropic(w, resp, rec)
	}
}

// nonStreamChatToAnthropic translates a non-streaming Chat Completion response
// to Anthropic format.
func nonStreamChatToAnthropic(w http.ResponseWriter, resp *http.Response, rec *state.RequestRecord) {
	var ccResp ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&ccResp); err != nil {
		api.ForwardError(w, err)
		return
	}

	// Capture token counts
	if ccResp.Usage != nil {
		rec.InputTokens = int64(ccResp.Usage.PromptTokens)
		rec.OutputTokens = int64(ccResp.Usage.CompletionTokens)
		if ccResp.Usage.PromptTokensDetails != nil {
			rec.CachedTokens = int64(ccResp.Usage.PromptTokensDetails.CachedTokens)
		}
	}

	result := translateToAnthropic(&ccResp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// streamChatToAnthropic translates streaming Chat Completion chunks to
// Anthropic SSE events.
func streamChatToAnthropic(w http.ResponseWriter, resp *http.Response, model string, rec *state.RequestRecord) {
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

	// Capture token counts from stream state
	input, output, cached := streamState.TokenCounts()
	rec.InputTokens = int64(input)
	rec.OutputTokens = int64(output)
	rec.CachedTokens = int64(cached)
}

// handleWithResponsesAPI translates Anthropic → Responses API, proxies the
// request, and translates the response back.
func handleWithResponsesAPI(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, forceAgent bool, rec *state.RequestRecord) {
	extraPrompt := config.GetExtraPrompt(normalizeModelName(req.Model))

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

	isAgent := forceAgent || isInitiatorAgent(req.Messages)
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
		streamResponsesToAnthropic(w, resp, payload.Model, rec)
	} else {
		nonStreamResponsesToAnthropic(w, resp, rec)
	}
}

// nonStreamResponsesToAnthropic translates a non-streaming Responses result
// to Anthropic format.
func nonStreamResponsesToAnthropic(w http.ResponseWriter, resp *http.Response, rec *state.RequestRecord) {
	var result ResponsesResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		api.ForwardError(w, err)
		return
	}

	// Capture token counts
	if result.Usage != nil {
		rec.InputTokens = int64(result.Usage.InputTokens)
		rec.OutputTokens = int64(result.Usage.OutputTokens)
		if result.Usage.InputTokensDetails != nil {
			rec.CachedTokens = int64(result.Usage.InputTokensDetails.CachedTokens)
		}
	}

	translated := translateResponsesResultToAnthropic(&result)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(translated)
}

// streamResponsesToAnthropic translates streaming Responses events to
// Anthropic SSE events.
func streamResponsesToAnthropic(w http.ResponseWriter, resp *http.Response, model string, rec *state.RequestRecord) {
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

	// Capture token counts from stream state
	input, output, cached := streamState.TokenCounts()
	rec.InputTokens = int64(input)
	rec.OutputTokens = int64(output)
	rec.CachedTokens = int64(cached)
}

// initiatorStr is defined in messages_utils.go
