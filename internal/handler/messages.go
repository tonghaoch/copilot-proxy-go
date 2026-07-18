package handler

import (
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/logger"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// Messages handles POST /v1/messages — the Anthropic-compatible endpoint.
// It routes to one of three backends based on the model's supported_endpoints.
func Messages(w http.ResponseWriter, r *http.Request) {
	tracked := trackRequest(w, r, "messages")
	defer tracked.Finish()
	ww := tracked.Writer
	rec := tracked.Record
	var req AnthropicRequest
	body, err := decodeRequestBody(ww, r, &req)
	if err != nil {
		api.ForwardError(ww, err)
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
	rec.Model = originalModel
	rec.RoutedModel = req.Model
	rec.RequestType = reqType
	rec.Initiator = initiatorStr(isAgent)
	rec.HasVision = hasVision(req.Messages)
	rec.Streaming = req.Stream
	rec.ToolCount = len(req.Tools)
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

}
