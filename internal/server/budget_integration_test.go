package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/server"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type budgetCopilot struct {
	calls              int
	missingUsage       bool
	missingStreamUsage bool
	err                error
	chatBody           []byte
}

func (*budgetCopilot) FetchModels(context.Context) ([]state.Model, error) { return nil, nil }
func (*budgetCopilot) ProxyCountTokens(context.Context, []byte, string) (*http.Response, error) {
	panic("unexpected token-count request")
}

func (c *budgetCopilot) ProxyMessages(_ context.Context, body []byte, _ string, _, _ bool) (*http.Response, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req)
	if c.missingUsage {
		return budgetResponse(`{"id":"m","type":"message","role":"assistant","content":[],"stop_reason":"end_turn"}`), nil
	}
	if req.Stream {
		return budgetResponse(strings.Join([]string{
			`event: message_start`, `data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"test-model","content":[],"usage":{"input_tokens":10,"output_tokens":0,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":4}}}}`, "",
			`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`, "",
			`event: message_stop`, `data: {"type":"message_stop"}`, "",
		}, "\n") + "\n"), nil
	}
	return budgetResponse(`{"id":"m","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":4}}}`), nil
}

func (c *budgetCopilot) ProxyChatCompletionEx(_ context.Context, body []byte, _, _ bool) (*http.Response, error) {
	c.calls++
	c.chatBody = append([]byte(nil), body...)
	if c.err != nil {
		return nil, c.err
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req)
	if req.Stream {
		events := []string{
			`data: {"id":"c","model":"test-model","choices":[{"index":0,"delta":{"content":"ok"}}]}`, "",
			`data: {"id":"c","model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, "",
		}
		if !c.missingStreamUsage {
			events = append(events, `data: {"id":"c","model":"test-model","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":2,"cache_write_tokens":3}}}`, "")
		}
		events = append(events, `data: [DONE]`, "")
		return budgetResponse(strings.Join(events, "\n") + "\n"), nil
	}
	return budgetResponse(`{"id":"c","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":2,"cache_write_tokens":3}}}`), nil
}

func (c *budgetCopilot) ProxyResponses(_ context.Context, body []byte, _, _ bool) (*http.Response, error) {
	c.calls++
	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req)
	if req.Stream {
		return budgetResponse(strings.Join([]string{
			`event: response.created`, `data: {"type":"response.created","response":{"id":"r","model":"test-model"}}`, "",
			`event: response.completed`, `data: {"type":"response.completed","response":{"id":"r","model":"test-model","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3}}}}`, "",
		}, "\n") + "\n"), nil
	}
	return budgetResponse(`{"id":"r","model":"test-model","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3}}}`), nil
}

func (c *budgetCopilot) ProxyEmbeddings(context.Context, []byte) (*http.Response, error) {
	c.calls++
	return budgetResponse(`{"data":[],"usage":{"prompt_tokens":10,"total_tokens":10}}`), nil
}

func budgetResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func pricedBudgetModel(t *testing.T, endpoints []string) state.Model {
	t.Helper()
	var model state.Model
	err := json.Unmarshal([]byte(`{"id":"test-model","billing":{"token_prices":{"batch_size":1000,"default":{"max_prompt_tokens":1000,"input_price":100,"output_price":200,"cache_read_price":10,"cache_write_price":125,"cache_write_1h_price":200}}}}`), &model)
	if err != nil {
		t.Fatal(err)
	}
	model.SupportedEndpoints = endpoints
	return model
}

func budgetServer(t *testing.T, model state.Model, upstream *budgetCopilot) (*http.Server, *state.TokenBudget) {
	t.Helper()
	models := &state.State{}
	models.SetModels([]state.Model{model})
	budget, err := state.NewTokenBudget(0.001)
	if err != nil {
		t.Fatal(err)
	}
	h := handler.New(handler.Dependencies{State: models, Metrics: &contractMetrics{}, Copilot: upstream, Budget: budget})
	return server.NewWithHandler(server.Options{}, h), budget
}

func serveBudgetRequest(srv *http.Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	setTestAuthorization(req)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestBudgetAcrossModelEndpoints(t *testing.T) {
	tests := []struct {
		name, path, body string
		endpoints        []string
		chatStream       bool
	}{
		{"messages", "/v1/messages", `{"model":"test-model","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`, []string{"/v1/messages"}, false},
		{"messages-stream", "/v1/messages", `{"model":"test-model","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hello"}]}`, []string{"/v1/messages"}, false},
		{"messages-via-chat-stream", "/v1/messages", `{"model":"test-model","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hello"}]}`, []string{"/chat/completions"}, true},
		{"messages-via-responses-stream", "/v1/messages", `{"model":"test-model","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hello"}]}`, []string{"/responses"}, false},
		{"responses", "/v1/responses", `{"model":"test-model","input":"hello"}`, []string{"/responses"}, false},
		{"responses-stream", "/v1/responses", `{"model":"test-model","input":"hello","stream":true}`, []string{"/responses"}, false},
		{"responses-via-messages", "/v1/responses", `{"model":"test-model","input":"hello"}`, []string{"/v1/messages"}, false},
		{"responses-via-messages-stream", "/v1/responses", `{"model":"test-model","input":"hello","stream":true}`, []string{"/v1/messages"}, false},
		{"chat", "/v1/chat/completions", `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`, []string{"/chat/completions"}, false},
		{"chat-stream", "/v1/chat/completions", `{"model":"test-model","stream":true,"stream_options":{"include_usage":false},"messages":[{"role":"user","content":"hello"}]}`, []string{"/chat/completions"}, true},
		{"embeddings", "/v1/embeddings", `{"model":"test-model","input":"hello"}`, []string{"/embeddings"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &budgetCopilot{}
			srv, budget := budgetServer(t, pricedBudgetModel(t, tt.endpoints), upstream)
			first := serveBudgetRequest(srv, http.MethodPost, tt.path, tt.body)
			if first.Code != http.StatusOK {
				t.Fatalf("first request: %d %s", first.Code, first.Body.String())
			}
			wantCost := 0.01895
			if tt.endpoints[0] == "/v1/messages" {
				wantCost = 0.028
			} else if tt.name == "embeddings" {
				wantCost = 0.01
			}
			if snapshot := budget.Snapshot(); !snapshot.Blocked || math.Abs(snapshot.EstimatedSpentUSD-wantCost) > 0.000000001 {
				t.Fatalf("request was not accounted: %+v", snapshot)
			}
			second := serveBudgetRequest(srv, http.MethodPost, tt.path, tt.body)
			if second.Code != http.StatusTooManyRequests || upstream.calls != 1 {
				t.Fatalf("second request: %d, upstream calls %d, body %s", second.Code, upstream.calls, second.Body.String())
			}
			if tt.chatStream && !bytes.Contains(upstream.chatBody, []byte(`"include_usage":true`)) {
				t.Fatalf("stream usage was not requested: %s", upstream.chatBody)
			}
			if health := serveBudgetRequest(srv, http.MethodGet, "/", ""); health.Code != http.StatusOK {
				t.Fatalf("health blocked: %d", health.Code)
			}
			stats := serveBudgetRequest(srv, http.MethodGet, "/api/stats", "")
			var parsed struct {
				Budget state.BudgetSnapshot `json:"budget"`
			}
			if stats.Code != http.StatusOK || json.Unmarshal(stats.Body.Bytes(), &parsed) != nil || !parsed.Budget.Blocked {
				t.Fatalf("budget status unavailable: %d %s", stats.Code, stats.Body.String())
			}
		})
	}
}

func TestBudgetStopsWhenUpstreamUsageIsMissing(t *testing.T) {
	upstream := &budgetCopilot{missingUsage: true}
	srv, budget := budgetServer(t, pricedBudgetModel(t, []string{"/v1/messages"}), upstream)
	body := `{"model":"test-model","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`
	if first := serveBudgetRequest(srv, http.MethodPost, "/v1/messages", body); first.Code != http.StatusOK {
		t.Fatalf("first request: %d %s", first.Code, first.Body.String())
	}
	if snapshot := budget.Snapshot(); !snapshot.Blocked || !strings.Contains(snapshot.Reason, "usage") {
		t.Fatalf("missing usage was not reported: %+v", snapshot)
	}
	second := serveBudgetRequest(srv, http.MethodPost, "/v1/messages", body)
	if second.Code != http.StatusServiceUnavailable || upstream.calls != 1 {
		t.Fatalf("paused request: %d, upstream calls %d", second.Code, upstream.calls)
	}
}

func TestBudgetContinuesAfterBoundedMissingUsage(t *testing.T) {
	upstream := &budgetCopilot{missingUsage: true}
	model := pricedBudgetModel(t, []string{"/v1/messages"})
	model.Capabilities.Limits = state.ModelLimits{MaxContextWindowTokens: 1000, MaxOutputTokens: 100}
	models := &state.State{}
	models.SetModels([]state.Model{model})
	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	h := handler.New(handler.Dependencies{State: models, Metrics: &contractMetrics{}, Copilot: upstream, Budget: budget})
	srv := server.NewWithHandler(server.Options{}, h)
	body := `{"model":"test-model","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`
	if first := serveBudgetRequest(srv, http.MethodPost, "/v1/messages", body); first.Code != http.StatusOK {
		t.Fatalf("first request: %d %s", first.Code, first.Body.String())
	}
	snapshot := budget.Snapshot()
	if snapshot.Blocked || snapshot.ConservativeEstimates != 1 || snapshot.EstimatedSpentUSD <= 0 {
		t.Fatalf("bounded missing usage not accounted: %+v", snapshot)
	}
	if second := serveBudgetRequest(srv, http.MethodPost, "/v1/messages", body); second.Code != http.StatusOK || upstream.calls != 2 {
		t.Fatalf("proxy did not continue: %d, upstream calls %d", second.Code, upstream.calls)
	}
}

func TestBudgetResolvesAvailableClaudeMajorAlias(t *testing.T) {
	upstream := &budgetCopilot{}
	model := pricedBudgetModel(t, []string{"/v1/messages"})
	model.ID = "claude-sonnet-5.5"
	models := &state.State{}
	models.SetModels([]state.Model{model})
	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	h := handler.New(handler.Dependencies{State: models, Metrics: &contractMetrics{}, Copilot: upstream, Budget: budget})
	srv := server.NewWithHandler(server.Options{}, h)
	body := `{"model":"claude-sonnet-5","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`
	response := serveBudgetRequest(srv, http.MethodPost, "/v1/messages", body)
	if response.Code != http.StatusOK || response.Header().Get("X-Copilot-Proxy-Routed-Model") != model.ID || upstream.calls != 1 {
		t.Fatalf("alias request: status=%d routed=%q calls=%d body=%s",
			response.Code, response.Header().Get("X-Copilot-Proxy-Routed-Model"), upstream.calls, response.Body.String())
	}
}

func TestBudgetRejectsMissingPricingBeforeUpstream(t *testing.T) {
	model := pricedBudgetModel(t, []string{"/v1/messages"})
	model.Billing.TokenPrices = nil
	upstream := &budgetCopilot{}
	srv, _ := budgetServer(t, model, upstream)
	body := `{"model":"test-model","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`
	response := serveBudgetRequest(srv, http.MethodPost, "/v1/messages", body)
	if response.Code != http.StatusServiceUnavailable || upstream.calls != 0 {
		t.Fatalf("unpriced request: %d, upstream calls %d", response.Code, upstream.calls)
	}
}

func TestBudgetPausesWhenStreamingUsageIsMissing(t *testing.T) {
	upstream := &budgetCopilot{missingStreamUsage: true}
	srv, budget := budgetServer(t, pricedBudgetModel(t, []string{"/chat/completions"}), upstream)
	body := `{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	if first := serveBudgetRequest(srv, http.MethodPost, "/v1/chat/completions", body); first.Code != http.StatusOK {
		t.Fatalf("first request: %d %s", first.Code, first.Body.String())
	}
	if snapshot := budget.Snapshot(); !snapshot.Blocked || !strings.Contains(snapshot.Reason, "usage") {
		t.Fatalf("missing stream usage was not reported: %+v", snapshot)
	}
	second := serveBudgetRequest(srv, http.MethodPost, "/v1/chat/completions", body)
	if second.Code != http.StatusServiceUnavailable || upstream.calls != 1 {
		t.Fatalf("paused request: %d, upstream calls %d", second.Code, upstream.calls)
	}
}

func TestBudgetDistinguishesRejectedAndUncertainUpstreamRequests(t *testing.T) {
	for _, tt := range []struct {
		status     int
		nextStatus int
		calls      int
	}{
		{http.StatusBadRequest, http.StatusBadRequest, 2},
		{http.StatusInternalServerError, http.StatusServiceUnavailable, 1},
	} {
		upstream := &budgetCopilot{err: &api.HTTPError{Message: "upstream error", StatusCode: tt.status}}
		srv, _ := budgetServer(t, pricedBudgetModel(t, []string{"/v1/messages"}), upstream)
		body := `{"model":"test-model","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`
		serveBudgetRequest(srv, http.MethodPost, "/v1/messages", body)
		second := serveBudgetRequest(srv, http.MethodPost, "/v1/messages", body)
		if second.Code != tt.nextStatus || upstream.calls != tt.calls {
			t.Errorf("upstream %d: second=%d calls=%d", tt.status, second.Code, upstream.calls)
		}
	}
}
