package handler

import (
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/logger"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// Messages handles POST /v1/messages — the Anthropic-compatible endpoint.
// It routes to one of three backends based on the model's supported_endpoints.
func Messages(w http.ResponseWriter, r *http.Request) {
	defaultHandler.Messages(w, r)
}

func (h *Handler) Messages(w http.ResponseWriter, r *http.Request) {
	tracked := trackRequest(w, r, "messages", h.metrics)
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
	if changed := applySmallModelIfNeeded(&req, betaHeader, h.config); changed {
		slog.Info("routed to small model", "model", req.Model, "reason", "compact/warmup")
		// Otherwise the substitution is invisible outside the server log.
		ww.Header().Set("X-Copilot-Proxy-Routed-Model", req.Model)
	}

	subagent := detectSubagentMarker(req.Messages)
	buildSessionSnapshot(&req, betaHeader, subagent, h.metrics)
	mergeToolResultBlocks(&req)
	filterUnsupportedTools(&req)
	model := h.state.FindModel(req.Model)

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

	if !h.admitBudget(ww, tracked, model, messagesBackend(model)) {
		return
	}
	if subagent != nil {
		r = r.WithContext(service.WithSubagentInteraction(r.Context()))
	}
	h.dispatchMessages(ww, r, &req, model, forceAgent, body, rec)
}

// dispatchMessages sends a parsed Anthropic request to the backend that serves
// its model: native Messages, Responses, or Chat Completions. Split out so the
// Responses endpoint can reuse the router for models without /responses.
func (h *Handler) dispatchMessages(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, model *state.Model, forceAgent bool, body []byte, rec *state.RequestRecord) {
	rec.Backend = messagesBackend(model)
	switch rec.Backend {
	case "messages":
		slog.Info("routing to Messages API", "model", req.Model)
		h.handleWithMessagesAPI(w, r, req, forceAgent, body, rec)
	case "responses":
		slog.Info("routing to Responses API", "model", req.Model)
		h.handleWithResponsesAPI(w, r, req, forceAgent, rec)
	default:
		slog.Info("routing to Chat Completions API", "model", req.Model)
		h.handleWithChatCompletions(w, r, req, forceAgent, rec)
	}
}

func messagesBackend(model *state.Model) string {
	if model != nil && isMessagesSupported(model) {
		return "messages"
	}
	if model != nil && isResponsesSupported(model) {
		return "responses"
	}
	return "chat_completions"
}
