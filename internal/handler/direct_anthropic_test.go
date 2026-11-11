package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type directTestClient struct {
	models       []state.Model
	body         []byte
	headers      http.Header
	response     string
	countBody    []byte
	messageCalls int
	countCalls   int
	err          error
}

func (c *directTestClient) Models() []state.Model { return c.models }

func (c *directTestClient) FindModel(id string) *state.Model {
	for _, model := range c.models {
		if model.ID == id {
			copy := model
			return &copy
		}
	}
	return nil
}

func (c *directTestClient) ProxyMessages(_ context.Context, body []byte, headers http.Header) (*http.Response, error) {
	c.messageCalls++
	c.body = append([]byte(nil), body...)
	c.headers = headers.Clone()
	if c.err != nil {
		return nil, c.err
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Request-Id": {"direct-request"}},
		Body: io.NopCloser(strings.NewReader(c.response)),
	}, nil
}

func (c *directTestClient) ProxyCountTokens(_ context.Context, body []byte, _ http.Header) (*http.Response, error) {
	c.countCalls++
	c.countBody = append([]byte(nil), body...)
	if c.err != nil {
		return nil, c.err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"input_tokens":42}`))}, nil
}

func directTestModel(id string) state.Model {
	return state.Model{
		ID: id, Name: id, Vendor: "Anthropic",
		Capabilities: state.ModelCapabilities{
			Supports: state.ModelSupports{AdaptiveThinking: true},
			Limits: state.ModelLimits{
				MaxContextWindowTokens: 1_000_000, MaxOutputTokens: 128_000,
			},
		},
		SupportedEndpoints: []string{"/v1/messages"},
	}
}

type directTestConfig struct {
	responsesTestConfig
	snapshot *config.Config
}

func (c directTestConfig) Snapshot() *config.Config { return c.snapshot }

func directTestHandler(direct *directTestClient, copilot *cacheTestCopilot, budget *state.TokenBudget, metrics *cacheTestMetrics) *Handler {
	models := &state.State{}
	models.SetModels([]state.Model{
		{ID: "claude-opus-5.5", SupportedEndpoints: []string{"/v1/messages"}},
		{ID: "gpt-test", SupportedEndpoints: []string{"/v1/messages"}},
	})
	return New(Dependencies{
		State: models, Metrics: metrics, Budget: budget, Copilot: copilot, DirectAnthropic: direct,
		Config: responsesTestConfig{}, ForceCacheTTL: "1h",
	})
}

func TestDirectAnthropicPreferredAndBudgetedWithNativePayload(t *testing.T) {
	direct := &directTestClient{
		models:   []state.Model{directTestModel("claude-opus-5-5")},
		response: `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":100000,"output_tokens":10000,"cache_read_input_tokens":400000,"cache_creation_input_tokens":100000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":100000}}}`,
	}
	copilot := &cacheTestCopilot{}
	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	metrics := &cacheTestMetrics{}
	h := directTestHandler(direct, copilot, budget, metrics)
	body := `{"model":"claude-opus-5.5[1m]","max_tokens":20,"speed":"fast","inference_geo":"us",
		"system":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral","ttl":"5m","scope":"preserve"}}],
		"tools":[{"name":"web_search","type":"web_search_20260209"}],
		"safeguards":[{"type":"dangerous_tool_use"}],
		"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Anthropic-Beta", "context-1m-2025-08-07,example-beta")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	w := httptest.NewRecorder()
	h.Messages(w, req)
	if w.Code != http.StatusOK || w.Header().Get("X-Copilot-Proxy-Upstream") != "anthropic" || direct.messageCalls != 1 || len(copilot.body) != 0 {
		t.Fatalf("direct routing: status=%d headers=%v calls=%d copilot=%s body=%s", w.Code, w.Header(), direct.messageCalls, copilot.body, w.Body)
	}
	var sent map[string]any
	if err := json.Unmarshal(direct.body, &sent); err != nil {
		t.Fatal(err)
	}
	cc := sent["system"].([]any)[0].(map[string]any)["cache_control"].(map[string]any)
	if sent["model"] != "claude-opus-5-5" || cc["ttl"] != "1h" || cc["scope"] != nil ||
		sent["safeguards"] == nil || sent["tools"].([]any)[0].(map[string]any)["name"] != "web_search" {
		t.Fatalf("direct request lost native fields: %v", sent)
	}
	if direct.headers.Get("Anthropic-Beta") != "context-1m-2025-08-07,example-beta" ||
		w.Header().Get("Request-Id") != "direct-request" || metrics.record.Backend != "anthropic" {
		t.Fatalf("direct headers or metrics missing: upstream=%v response=%v record=%+v", direct.headers, w.Header(), metrics.record)
	}
	if got, want := budget.Snapshot().EstimatedSpentUSD, 3.256; math.Abs(got-want) > 0.000000002 {
		t.Fatalf("direct budget spent = %v, want %v", got, want)
	}
}

func TestDirectAnthropicErrorsDoNotFallBackOrSpendBudget(t *testing.T) {
	direct := &directTestClient{
		models: []state.Model{directTestModel("claude-opus-5-5")},
		err: &api.HTTPError{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"5"}},
			Body: `{"error":{"type":"rate_limit_error","message":"rate limited"}}`},
	}
	copilot := &cacheTestCopilot{}
	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	h := directTestHandler(direct, copilot, budget, &cacheTestMetrics{})
	w := httptest.NewRecorder()
	h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-opus-5-5","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "5" ||
		!strings.Contains(w.Body.String(), "rate limited") || len(copilot.body) != 0 {
		t.Fatalf("direct error was not forwarded: status=%d body=%s copilot=%s", w.Code, w.Body, copilot.body)
	}
	if got := budget.Snapshot().EstimatedSpentUSD; got != 0 {
		t.Fatalf("definite rejection charged %v", got)
	}
}

func TestDirectAnthropicStreamingPreservesEventsAndUsage(t *testing.T) {
	direct := &directTestClient{
		models: []state.Model{directTestModel("claude-opus-5-5")},
		response: "event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"m","usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":30}}}}` + "\n\n" +
			"event: content_block_delta\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
			"event: message_delta\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n" +
			"event: message_stop\n" +
			`data: {"type":"message_stop"}` + "\n\n",
	}
	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	metrics := &cacheTestMetrics{}
	h := directTestHandler(direct, &cacheTestCopilot{}, budget, metrics)
	w := httptest.NewRecorder()
	h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-opus-5-5","stream":true,"max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "event: message_stop") ||
		!strings.Contains(w.Body.String(), `"text":"hello"`) {
		t.Fatalf("direct stream: status=%d body=%s", w.Code, w.Body)
	}
	if got := metrics.record; !got.InputUsageKnown || !got.OutputUsageKnown ||
		got.CacheWrite1hTokens != 30 || got.CachedTokens != 20 || got.Backend != "anthropic" {
		t.Fatalf("direct stream usage = %+v", got)
	}
	if got, want := budget.Snapshot().EstimatedSpentUSD, 0.000384; math.Abs(got-want) > 0.000000002 {
		t.Fatalf("direct stream budget = %v, want %v", got, want)
	}
}

func TestDirectAnthropicUnknownPricingFailsClosed(t *testing.T) {
	direct := &directTestClient{models: []state.Model{directTestModel("claude-unknown-1")}}
	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	h := directTestHandler(direct, &cacheTestCopilot{}, budget, &cacheTestMetrics{})
	w := httptest.NewRecorder()
	h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-unknown-1","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "pricing") || direct.messageCalls != 0 {
		t.Fatalf("unknown pricing was not rejected: status=%d body=%s calls=%d", w.Code, w.Body, direct.messageCalls)
	}
}

func TestDirectAnthropicCountTokensAndCopilotFallback(t *testing.T) {
	direct := &directTestClient{models: []state.Model{directTestModel("claude-opus-5-5")}}
	copilot := &cacheTestCopilot{response: `{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`}
	h := directTestHandler(direct, copilot, nil, &cacheTestMetrics{})

	w := httptest.NewRecorder()
	h.CountTokens(w, httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(
		`{"model":"claude-opus-5.5[1m]","stream":true,"system":[{"type":"text","text":"x","cache_control":{"type":"ephemeral","scope":"local"}}],"messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != http.StatusOK || direct.countCalls != 1 || !strings.Contains(w.Body.String(), `"input_tokens":42`) ||
		!strings.Contains(string(direct.countBody), `"model":"claude-opus-5-5"`) ||
		strings.Contains(string(direct.countBody), `"stream"`) || strings.Contains(string(direct.countBody), `"scope"`) {
		t.Fatalf("direct token count: status=%d body=%s upstream=%s", w.Code, w.Body, direct.countBody)
	}

	w = httptest.NewRecorder()
	h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"gpt-test","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != http.StatusOK || w.Header().Get("X-Copilot-Proxy-Upstream") != "copilot" || len(copilot.body) == 0 || direct.messageCalls != 0 {
		t.Fatalf("non-Anthropic model did not use Copilot: status=%d body=%s", w.Code, w.Body)
	}
}

func TestDirectAnthropicSingleTurnWithBetaIsNotWarmup(t *testing.T) {
	direct := &directTestClient{
		models:   []state.Model{directTestModel("claude-opus-5-5")},
		response: `{"id":"m","type":"message","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	copilot := &cacheTestCopilot{}
	metrics := &cacheTestMetrics{}
	h := directTestHandler(direct, copilot, nil, metrics)
	h.config = directTestConfig{snapshot: &config.Config{SmallModel: "gpt-test"}}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-opus-5-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Anthropic-Beta", "some-beta")
	w := httptest.NewRecorder()
	h.Messages(w, req)
	if w.Code != http.StatusOK || direct.messageCalls != 1 || len(copilot.body) != 0 ||
		w.Header().Get("X-Copilot-Proxy-Upstream") != "anthropic" || metrics.record.RequestType != "normal" {
		t.Fatalf("single-turn request: status=%d headers=%v direct=%d copilot=%s record=%+v",
			w.Code, w.Header(), direct.messageCalls, copilot.body, metrics.record)
	}
}

func TestDirectAnthropicSmallModelRouting(t *testing.T) {
	for _, tt := range []struct {
		name        string
		model       string
		smallModel  string
		system      string
		wantBackend string
	}{
		{"warmup to direct Haiku", "gpt-test", "claude-haiku-5.5", "", "anthropic"},
		{"compact to Copilot", "claude-opus-5-5", "gpt-test", compactPrefix, "copilot"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			direct := &directTestClient{
				models:   []state.Model{directTestModel("claude-opus-5-5"), directTestModel("claude-haiku-5-5")},
				response: `{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
			}
			copilot := &cacheTestCopilot{response: direct.response}
			h := directTestHandler(direct, copilot, nil, &cacheTestMetrics{})
			h.config = directTestConfig{snapshot: &config.Config{SmallModel: tt.smallModel, CompactUseSmallModel: true}}
			body := `{"model":"` + tt.model + `","max_tokens":1,"system":"` + tt.system + `","messages":[{"role":"user","content":"hi"}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			req.Header.Set("Anthropic-Beta", "example-beta")
			w := httptest.NewRecorder()
			h.Messages(w, req)
			if w.Code != http.StatusOK || w.Header().Get("X-Copilot-Proxy-Upstream") != tt.wantBackend ||
				(tt.wantBackend == "anthropic" && direct.messageCalls != 1) ||
				(tt.wantBackend == "copilot" && (len(copilot.body) == 0 || direct.messageCalls != 0)) {
				t.Fatalf("small-model routing: status=%d headers=%v direct=%d copilot=%s", w.Code, w.Header(), direct.messageCalls, copilot.body)
			}
		})
	}
}

func TestResponsesUsesDirectAnthropic(t *testing.T) {
	direct := &directTestClient{
		models:   []state.Model{directTestModel("claude-opus-5-5")},
		response: `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":20}}`,
	}

	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	metrics := &cacheTestMetrics{}
	h := directTestHandler(direct, &cacheTestCopilot{}, budget, metrics)
	w := httptest.NewRecorder()
	h.Responses(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"claude-opus-5-5","input":"hi","speed":"fast","inference_geo":"us","temperature":0.5,"reasoning":{"effort":"high"}}`)))
	if w.Code != http.StatusOK || w.Header().Get("X-Copilot-Proxy-Upstream") != "anthropic" ||
		direct.messageCalls != 1 || metrics.record.Backend != "anthropic" || !strings.Contains(w.Body.String(), "hello") ||
		!strings.Contains(string(direct.body), `"speed":"fast"`) || !strings.Contains(string(direct.body), `"inference_geo":"us"`) ||
		!strings.Contains(string(direct.body), `"temperature":0.5`) || !strings.Contains(string(direct.body), `"thinking":{"type":"adaptive"}`) {
		t.Fatalf("Responses direct route: status=%d body=%s upstream=%s record=%+v", w.Code, w.Body, direct.body, metrics.record)
	}
	if got, want := budget.Snapshot().EstimatedSpentUSD, 0.00176; math.Abs(got-want) > 0.000000002 {
		t.Fatalf("Responses direct budget = %v, want %v", got, want)
	}
}

func TestModelsUseDirectLimitsAndPricingFlavours(t *testing.T) {
	direct := &directTestClient{models: []state.Model{
		directTestModel("claude-opus-5-5"),
		directTestModel("claude-haiku-5-5"),
	}}
	h := directTestHandler(direct, &cacheTestCopilot{}, nil, &cacheTestMetrics{})
	h.state.SetModels([]state.Model{{
		ID: "claude-opus-5.5", Vendor: "Copilot",
		Capabilities: state.ModelCapabilities{Limits: state.ModelLimits{
			MaxContextWindowTokens: 1_000_000, MaxPromptTokens: 300_000,
		}},
	}})
	w := httptest.NewRecorder()
	h.Models(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	var response ModelsListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]ModelEntry, len(response.Data))
	for _, entry := range response.Data {
		entries[entry.ID] = entry
	}
	if len(entries) != 3 || entries["claude-opus-5-5[1m]"].OwnedBy != "Anthropic" ||
		entries["claude-opus-5-5[1m]"].ContextWindow != 1_000_000 ||
		entries["claude-haiku-5-5"].ContextWindow != 100_000 ||
		entries["claude-haiku-5-5[1m]"].ContextWindow != 1_000_000 {
		t.Fatalf("direct models or price tiers are misrepresented: %+v", entries)
	}
}

func TestDirectCompactionCountsEachIterationAtItsOwnTier(t *testing.T) {
	const iterations = `[{"type":"compaction","input_tokens":90000,"output_tokens":1000},{"type":"message","input_tokens":90000,"output_tokens":1000}]`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			response := `{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":90000,"output_tokens":1000,"iterations":` + iterations + `}}`
			if stream {
				response = "event: message_start\n" +
					`data: {"type":"message_start","message":{"usage":{"input_tokens":90000,"output_tokens":0}}}` + "\n\n" +
					"event: message_delta\n" +
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1000,"iterations":` + iterations + `}}` + "\n\n" +
					"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
			}
			direct := &directTestClient{models: []state.Model{directTestModel("claude-haiku-5-5")}, response: response}
			budget, err := state.NewTokenBudget(100)
			if err != nil {
				t.Fatal(err)
			}
			metrics := &cacheTestMetrics{}
			h := directTestHandler(direct, &cacheTestCopilot{}, budget, metrics)
			body := fmt.Sprintf(`{"model":"claude-haiku-5-5","stream":%v,"max_tokens":5000,"context_management":{"edits":[{"type":"compact_20260112"}]},"messages":[{"role":"user","content":"hi"}]}`, stream)
			w := httptest.NewRecorder()
			h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
			snapshot := budget.Snapshot()
			if w.Code != http.StatusOK || snapshot.Blocked || math.Abs(snapshot.EstimatedSpentUSD-0.019) > 0.000000002 ||
				metrics.record.InputTokens != 180_000 || metrics.record.OutputTokens != 2_000 {
				t.Fatalf("iteration accounting: status=%d budget=%+v record=%+v", w.Code, snapshot, metrics.record)
			}
		})
	}
}

func TestCopilotNativeCompactionCountsIterations(t *testing.T) {
	model := directTestModel("claude-opus-5-5")
	priced, err := state.AnthropicBudgetModel(&model, "", "")
	if err != nil {
		t.Fatal(err)
	}
	priced.ID = "claude-opus-5.5"
	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	metrics := &cacheTestMetrics{}
	copilot := &cacheTestCopilot{response: `{"id":"m","type":"message","content":[],"usage":{"input_tokens":90000,"output_tokens":1000,"iterations":[{"type":"compaction","input_tokens":90000,"output_tokens":1000},{"type":"message","input_tokens":90000,"output_tokens":1000}]}}`}
	h := directTestHandler(nil, copilot, budget, metrics)
	h.directAnthropic = nil
	h.state.SetModels([]state.Model{*priced})
	w := httptest.NewRecorder()
	h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-opus-5-5","max_tokens":5000,"context_management":{"edits":[{"type":"compact_20260112"}]},"messages":[{"role":"user","content":"hi"}]}`)))
	if snapshot := budget.Snapshot(); w.Code != http.StatusOK || snapshot.Blocked ||
		math.Abs(snapshot.EstimatedSpentUSD-0.76) > 0.000000002 || metrics.record.InputTokens != 180_000 {
		t.Fatalf("Copilot iteration accounting: status=%d budget=%+v record=%+v", w.Code, snapshot, metrics.record)
	}
}

func TestDirectCompactionIncompleteUsagePausesBudget(t *testing.T) {
	for _, tt := range []struct {
		name     string
		stream   bool
		response string
	}{
		{"missing iterations", false, `{"id":"m","type":"message","stop_reason":"compaction","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`},
		{"interrupted stream", true, "event: message_start\n" + `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			direct := &directTestClient{models: []state.Model{directTestModel("claude-opus-5-5")}, response: tt.response}
			budget, err := state.NewTokenBudget(100)
			if err != nil {
				t.Fatal(err)
			}
			h := directTestHandler(direct, &cacheTestCopilot{}, budget, &cacheTestMetrics{})
			body := fmt.Sprintf(`{"model":"claude-opus-5-5","stream":%v,"max_tokens":5000,"compaction":{"type":"summarize"},"messages":[{"role":"user","content":"hi"}]}`, tt.stream)
			w := httptest.NewRecorder()
			h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
			if snapshot := budget.Snapshot(); w.Code != http.StatusOK || !snapshot.Blocked {
				t.Fatalf("incomplete compaction must pause admission: status=%d budget=%+v", w.Code, snapshot)
			}
		})
	}
}

func TestNativeIncompleteCacheBreakdownPausesBudget(t *testing.T) {
	for _, tt := range []struct {
		name  string
		usage string
	}{
		{"iteration missing total", `{"input_tokens":0,"output_tokens":0,"iterations":[{"type":"compaction","input_tokens":0,"output_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":100000}}]}`},
		{"iteration exceeds total", `{"input_tokens":0,"output_tokens":0,"iterations":[{"type":"message","input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":1}}]}`},
		{"message missing total", `{"input_tokens":0,"output_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":100000}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			direct := &directTestClient{
				models:   []state.Model{directTestModel("claude-opus-5-5")},
				response: `{"id":"m","type":"message","content":[],"usage":` + tt.usage + `}`,
			}
			budget, err := state.NewTokenBudget(100)
			if err != nil {
				t.Fatal(err)
			}
			h := directTestHandler(direct, &cacheTestCopilot{}, budget, &cacheTestMetrics{})
			w := httptest.NewRecorder()
			h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
				`{"model":"claude-opus-5-5","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)))
			snapshot := budget.Snapshot()
			if w.Code != http.StatusOK || !snapshot.Blocked || !strings.Contains(snapshot.Reason, "cache-write") {
				t.Fatalf("partial cache breakdown must pause admission: status=%d budget=%+v", w.Code, snapshot)
			}
		})
	}
}

func TestNativePartialCacheBreakdownUsesConservativePrice(t *testing.T) {
	direct := &directTestClient{
		models:   []state.Model{directTestModel("claude-opus-5-5")},
		response: `{"id":"m","type":"message","content":[],"usage":{"input_tokens":10,"output_tokens":1,"cache_creation_input_tokens":12,"cache_creation":{"ephemeral_1h_input_tokens":5}}}`,
	}
	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	h := directTestHandler(direct, &cacheTestCopilot{}, budget, &cacheTestMetrics{})
	w := httptest.NewRecorder()
	h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-opus-5-5","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)))
	snapshot := budget.Snapshot()
	if w.Code != http.StatusOK || snapshot.Blocked || math.Abs(snapshot.EstimatedSpentUSD-0.000156) > 0.000000002 {
		t.Fatalf("partial cache breakdown: status=%d budget=%+v", w.Code, snapshot)
	}
}

func TestNativeStreamPartialCacheUpdateClearsEarlierBreakdown(t *testing.T) {
	direct := &directTestClient{
		models: []state.Model{directTestModel("claude-opus-5-5")},
		response: "event: message_start\n" +
			`data: {"message":{"usage":{"input_tokens":0,"cache_creation_input_tokens":100000,"cache_creation":{"ephemeral_5m_input_tokens":100000,"ephemeral_1h_input_tokens":0}}}}` + "\n\n" +
			"event: message_delta\n" +
			`data: {"usage":{"output_tokens":1,"cache_creation_input_tokens":100000,"cache_creation":{"ephemeral_1h_input_tokens":100000}}}` + "\n\n" +
			"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n",
	}
	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	metrics := &cacheTestMetrics{}
	h := directTestHandler(direct, &cacheTestCopilot{}, budget, metrics)
	w := httptest.NewRecorder()
	h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-opus-5-5","stream":true,"max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)))
	snapshot := budget.Snapshot()
	if w.Code != http.StatusOK || snapshot.Blocked || math.Abs(snapshot.EstimatedSpentUSD-0.800020) > 0.000000002 ||
		metrics.record.CacheWriteDetailsKnown || metrics.record.CacheWrite5mTokens != 0 {
		t.Fatalf("partial cache update: status=%d budget=%+v record=%+v", w.Code, snapshot, metrics.record)
	}
}

func TestDirectBudgetRejectsUnpricedMultiModelFeatures(t *testing.T) {
	for _, option := range []string{
		`"fallbacks":"default"`,
		`"tools":[{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5-5"}]`,
	} {
		direct := &directTestClient{models: []state.Model{directTestModel("claude-haiku-5-5")}}
		budget, err := state.NewTokenBudget(100)
		if err != nil {
			t.Fatal(err)
		}
		h := directTestHandler(direct, &cacheTestCopilot{}, budget, &cacheTestMetrics{})
		body := `{"model":"claude-haiku-5-5","max_tokens":5000,` + option + `,"messages":[{"role":"user","content":"hi"}]}`
		w := httptest.NewRecorder()
		h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest || direct.messageCalls != 0 || budget.Snapshot().Blocked {
			t.Fatalf("multi-model feature %s was not rejected safely: status=%d body=%s", option, w.Code, w.Body)
		}
	}
}

func TestDirectUnexpectedIterationPausesBudget(t *testing.T) {
	direct := &directTestClient{
		models:   []state.Model{directTestModel("claude-haiku-5-5")},
		response: `{"id":"m","type":"message","content":[],"usage":{"input_tokens":1,"output_tokens":1,"iterations":[{"type":"advisor_message","model":"claude-opus-5-5","input_tokens":100,"output_tokens":5}]}}`,
	}
	budget, err := state.NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	h := directTestHandler(direct, &cacheTestCopilot{}, budget, &cacheTestMetrics{})
	w := httptest.NewRecorder()
	h.Messages(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-haiku-5-5","max_tokens":5000,"messages":[{"role":"user","content":"hi"}]}`)))
	if snapshot := budget.Snapshot(); w.Code != http.StatusOK || !snapshot.Blocked || !strings.Contains(snapshot.Reason, "unpriced") {
		t.Fatalf("unknown iteration must pause admission: status=%d budget=%+v", w.Code, snapshot)
	}
}
