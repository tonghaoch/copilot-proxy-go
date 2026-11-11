package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

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
	if h.budget != nil {
		r = tracked.trackRetries(r)
	}
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
	var preferredModel *state.Model
	if h.directAnthropic != nil {
		preferredModel = h.directAnthropic.FindModel(ResolveAnthropicModel(req.Model))
	}
	reqType := "normal"
	if isCompactRequest(&req) {
		reqType = "compact"
	} else if isWarmupRequest(&req, betaHeader) && (preferredModel == nil || req.MaxTokens == 1) {
		reqType = "warmup"
	}
	routedSmall := reqType != "normal" && applySmallModelIfNeeded(&req, betaHeader, h.config)
	if routedSmall {
		slog.Info("routed to small model", "model", req.Model, "reason", "compact/warmup")
	}

	model := preferredModel
	if routedSmall && h.directAnthropic != nil {
		model = h.directAnthropic.FindModel(ResolveAnthropicModel(req.Model))
	}
	direct := model != nil
	copilotName := req.Model
	if direct {
		req.Model = model.ID
		ww.Header().Set("X-Copilot-Proxy-Upstream", "anthropic")
		if req.Model != originalModel {
			ww.Header().Set("X-Copilot-Proxy-Routed-Model", req.Model)
		}
	} else {
		req.Model = ResolveCopilotModel(req.Model)
	}
	if routedSmall {
		ww.Header().Set("X-Copilot-Proxy-Routed-Model", req.Model)
	}
	logger.For("messages").Log("model=%s stream=%v initiator=%s", req.Model, req.Stream, initiatorStr(isInitiatorAgent(req.Messages)))
	if !direct {
		model = h.state.FindModel(req.Model)
		if model == nil {
			if resolved := resolveAvailableClaudeMajor(req.Model, h.state.GetModels()); resolved != "" {
				slog.Info("resolved Claude model alias", "requested", req.Model, "model", resolved)
				req.Model = resolved
				model = h.state.FindModel(resolved)
				ww.Header().Set("X-Copilot-Proxy-Routed-Model", resolved)
			}
		}
	}

	subagent := detectSubagentMarker(req.Messages)
	buildSessionSnapshot(&req, betaHeader, subagent, h.metrics)
	mergeToolResultBlocks(&req)
	if !direct {
		filterUnsupportedTools(&req)
	}

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

	if direct || messagesBackend(model) == "messages" {
		var options struct {
			Speed             string          `json:"speed"`
			InferenceGeo      string          `json:"inference_geo"`
			Compaction        json.RawMessage `json:"compaction"`
			Fallbacks         json.RawMessage `json:"fallbacks"`
			ContextManagement struct {
				Edits []struct {
					Type string `json:"type"`
				} `json:"edits"`
			} `json:"context_management"`
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &options); err != nil {
			api.ForwardError(ww, api.InvalidRequest("invalid Anthropic request options", err))
			return
		}
		hasValue := func(raw json.RawMessage) bool {
			return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
		}
		rec.RequireIterations = hasValue(options.Compaction)
		rec.MultiIterationRisk = rec.RequireIterations
		for _, edit := range options.ContextManagement.Edits {
			if strings.HasPrefix(edit.Type, "compact_") {
				rec.MultiIterationRisk = true
			}
		}
		for _, tool := range options.Tools {
			if tool.Type != "" && tool.Type != "custom" {
				rec.MultiIterationRisk = true
			}
			if h.budget != nil && strings.HasPrefix(tool.Type, "advisor_") {
				api.ForwardError(ww, api.InvalidRequest("Anthropic advisor tools cannot be budgeted safely", nil))
				return
			}
		}
		if h.budget != nil && hasValue(options.Fallbacks) {
			api.ForwardError(ww, api.InvalidRequest("Anthropic server-side fallbacks cannot be budgeted safely", nil))
			return
		}
		if direct {
			if !h.admitAnthropicBudget(ww, tracked, model, options.Speed, options.InferenceGeo) {
				return
			}
		} else if !h.admitBudget(ww, tracked, model, "messages") {
			return
		}
	} else if !h.admitBudget(ww, tracked, model, messagesBackend(model)) {
		return
	}
	if subagent != nil {
		r = r.WithContext(service.WithSubagentInteraction(r.Context()))
	}
	var fallback func(error) bool
	if direct {
		fallback = func(err error) bool {
			return h.fallbackToCopilot(ww, r, tracked, &req, copilotName, forceAgent, body, err)
		}
	}
	h.dispatchMessages(ww, r, &req, model, forceAgent, body, rec, direct, fallback)
}

// fallbackToCopilot retries a direct Anthropic failure through Copilot. It
// reports false, leaving the error to the caller, when fallback is disabled,
// the error is the client's fault, or Copilot does not serve the model.
func (h *Handler) fallbackToCopilot(w http.ResponseWriter, r *http.Request, tracked *trackedRequest, req *AnthropicRequest, name string, forceAgent bool, body []byte, err error) bool {
	reason, status, ok := anthropicFallbackReason(err)
	if !h.anthropicFallback || !ok || r.Context().Err() != nil {
		return false
	}
	name = ResolveCopilotModel(name)
	model := h.state.FindModel(name)
	if model == nil {
		if resolved := resolveAvailableClaudeMajor(name, h.state.GetModels()); resolved != "" {
			name, model = resolved, h.state.FindModel(resolved)
		}
	}
	if model == nil {
		return false
	}
	slog.Warn("direct Anthropic request failed; falling back to Copilot", "model", req.Model, "copilot_model", name, "reason", reason)

	rec := tracked.Record
	attempt := *rec
	attempt.StatusCode = status
	attempt.Error = "anthropic: " + reason
	attempt.LatencyMs = time.Since(tracked.start).Milliseconds()
	// An error status from Anthropic carries no usage and is not billed.
	attempt.UpstreamRejected = true
	attempt.UsageIterations = nil
	if tracked.admission != nil {
		tracked.budget.Complete(tracked.admission, attempt)
		tracked.admission = nil
	}
	tracked.metrics.RecordRequest(attempt)

	*rec = state.RequestRecord{
		Timestamp: rec.Timestamp, Endpoint: rec.Endpoint, Model: rec.Model, RoutedModel: name,
		RequestType: rec.RequestType, Initiator: rec.Initiator, HasVision: rec.HasVision,
		Streaming: rec.Streaming, ToolCount: rec.ToolCount, ThinkingBudget: rec.ThinkingBudget,
		MultiIterationRisk: rec.MultiIterationRisk, RequireIterations: rec.RequireIterations,
		Fallback: "anthropic: " + reason,
	}
	req.Model = name
	filterUnsupportedTools(req)
	w.Header().Set("X-Copilot-Proxy-Fallback", "anthropic; "+reason)
	w.Header().Set("X-Copilot-Proxy-Routed-Model", name)
	if !h.admitBudget(w, tracked, model, messagesBackend(model)) {
		return true
	}
	h.dispatchMessages(w, r, req, model, forceAgent, body, rec, false, nil)
	return true
}

// anthropicFallbackReason classifies errors Copilot may succeed on: account,
// credential, rate-limit and server problems. Malformed requests and transport
// failures (which may already have been billed) are not retried.
func anthropicFallbackReason(err error) (string, int, bool) {
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		return "", 0, false
	}
	switch status := httpErr.StatusCode; {
	case status == http.StatusBadRequest && strings.Contains(strings.ToLower(httpErr.Body+httpErr.Message), "credit balance"):
		return "insufficient credit", status, true
	case status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden:
		return fmt.Sprintf("auth %d", status), status, true
	case status == http.StatusTooManyRequests:
		return "rate limited", status, true
	case status >= 500:
		return fmt.Sprintf("upstream %d", status), status, true
	}
	return "", 0, false
}

// dispatchMessages sends a parsed Anthropic request to the backend that serves
// its model: native Messages, Responses, or Chat Completions. Split out so the
// Responses endpoint can reuse the router for models without /responses.
func (h *Handler) dispatchMessages(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, model *state.Model, forceAgent bool, body []byte, rec *state.RequestRecord, direct bool, fallback func(error) bool) {
	if direct {
		rec.Backend = "anthropic"
		w.Header().Set("X-Copilot-Proxy-Upstream", "anthropic")
		h.handleWithDirectAnthropicAPI(w, r, req, body, rec, fallback)
		return
	}
	w.Header().Set("X-Copilot-Proxy-Upstream", "copilot")
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
