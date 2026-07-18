package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/logger"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// Messages handles POST /v1/messages — the Anthropic-compatible endpoint.
// It routes to one of three backends based on the model's supported_endpoints.
func Messages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
	r.Body = http.MaxBytesReader(ww, r.Body, maxRequestBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.ForwardError(ww, err)
		return
	}

	var req AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		api.ForwardError(ww, api.InvalidRequest("invalid request body", err))
		return
	}
	betaHeader := r.Header.Get("Anthropic-Beta")
	originalModel := req.Model
	if newBody, resolved := RewriteModelInBody(body); resolved != "" {
		body = newBody
		req.Model = resolved
	}
	logger.For("messages").Log("model=%s stream=%v initiator=%s", req.Model, req.Stream, initiatorStr(isInitiatorAgent(req.Messages)))

	reqType := "normal"
	if isCompactRequest(&req) {
		reqType = "compact"
	} else if isWarmupRequest(&req, betaHeader) {
		reqType = "warmup"
	}
	if changed := applySmallModelIfNeeded(&req, betaHeader); changed {
		slog.Info("routed to small model", "model", req.Model, "reason", "compact/warmup")
	}

	subagent := detectSubagentMarker(req.Messages)
	buildSessionSnapshot(&req, betaHeader, subagent)
	mergeToolResultBlocks(&req)
	filterUnsupportedTools(&req)
	model := state.Global.FindModel(req.Model)

	forceAgent := false
	if subagent != nil {
		slog.Debug("subagent detected", "agent_id", subagent.AgentID, "agent_type", subagent.AgentType)
		forceAgent = true
	}
	isAgent := forceAgent || isInitiatorAgent(req.Messages)
	rec := &state.RequestRecord{
		Timestamp: start, Endpoint: "messages", Model: originalModel, RoutedModel: req.Model,
		RequestType: reqType, Initiator: initiatorStr(isAgent), HasVision: hasVision(req.Messages),
		Streaming: req.Stream, ToolCount: len(req.Tools),
	}
	if req.Thinking != nil {
		rec.ThinkingBudget = req.Thinking.BudgetTokens
	}

	if model != nil && isMessagesSupported(model) {
		slog.Info("routing to Messages API", "model", req.Model)
		rec.Backend = "messages"
		handleWithMessagesAPI(ww, r, &req, forceAgent, body, rec)
	} else if model != nil && isResponsesSupported(model) {
		slog.Info("routing to Responses API", "model", req.Model)
		rec.Backend = "responses"
		handleWithResponsesAPI(ww, r, &req, forceAgent, rec)
	} else {
		slog.Info("routing to Chat Completions API", "model", req.Model)
		rec.Backend = "chat_completions"
		handleWithChatCompletions(ww, r, &req, forceAgent, rec)
	}

	rec.LatencyMs = time.Since(start).Milliseconds()
	rec.StatusCode = ww.Status()
	if rec.StatusCode == 0 {
		rec.StatusCode = http.StatusOK
	}
	state.Metrics.RecordRequest(*rec)
}
