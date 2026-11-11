package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type cacheTestCopilot struct {
	capturingResponsesCopilot
	body     []byte
	beta     string
	response string
}

type safeguardsTestCopilot struct {
	capturingResponsesCopilot
	bodies [][]byte
	betas  []string
}

func (c *safeguardsTestCopilot) ProxyMessages(_ context.Context, body []byte, beta string, _, _ bool) (*http.Response, error) {
	c.bodies = append(c.bodies, append([]byte(nil), body...))
	c.betas = append(c.betas, beta)
	if len(c.bodies) == 1 {
		return nil, &api.HTTPError{StatusCode: http.StatusBadRequest, Body: `{"error":{"message":"safeguards: Extra inputs are not permitted"}}`}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
		`{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
}

func TestClaudeCodeSafeguardsFallback(t *testing.T) {
	models := &state.State{}
	models.SetModels([]state.Model{{ID: "claude-test", SupportedEndpoints: []string{"/v1/messages"}}})
	for _, claudeCode := range []bool{true, false} {
		upstream := &safeguardsTestCopilot{}
		h := New(Dependencies{State: models, Metrics: &cacheTestMetrics{}, Copilot: upstream, Config: responsesTestConfig{}})
		body := `{"model":"claude-test","max_tokens":20,"system":"test","safeguards":[{"type":"dangerous_tool_use"}],"messages":[{"role":"user","content":"hello"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Anthropic-Beta", "other-beta,dangerous-tool-use-2026-09-03")
		if claudeCode {
			req.Header.Set("User-Agent", "claude-cli/2.1.280")
		}
		response := httptest.NewRecorder()
		h.Messages(response, req)
		if !claudeCode {
			if response.Code != http.StatusBadRequest || len(upstream.bodies) != 1 {
				t.Fatalf("non-Claude client should receive upstream rejection: status=%d calls=%d", response.Code, len(upstream.bodies))
			}
			continue
		}
		if response.Code != http.StatusOK || len(upstream.bodies) != 2 || response.Header().Get("X-Copilot-Proxy-Safeguards") != "client-classifier" {
			t.Fatalf("Claude fallback: status=%d calls=%d header=%q", response.Code, len(upstream.bodies), response.Header().Get("X-Copilot-Proxy-Safeguards"))
		}
		var first, second map[string]any
		if err := json.Unmarshal(upstream.bodies[0], &first); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(upstream.bodies[1], &second); err != nil {
			t.Fatal(err)
		}
		if first["safeguards"] == nil || second["safeguards"] != nil || upstream.betas[0] != "other-beta,dangerous-tool-use-2026-09-03" || upstream.betas[1] != "other-beta" {
			t.Fatalf("incorrect safeguards fallback: first=%v second=%v betas=%v", first, second, upstream.betas)
		}
		if strings.Contains(response.Body.String(), "safeguard_results") {
			t.Fatal("proxy fabricated a safeguards verdict")
		}
	}
}

func (c *cacheTestCopilot) ProxyMessages(_ context.Context, body []byte, beta string, _, _ bool) (*http.Response, error) {
	c.body = append([]byte(nil), body...)
	c.beta = beta
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(c.response))}, nil
}

type cacheTestMetrics struct {
	responsesTestMetrics
	record state.RequestRecord
}

func (m *cacheTestMetrics) RecordRequest(rec state.RequestRecord) { m.record = rec }

func cacheTestHandler(upstream *cacheTestCopilot, metrics *cacheTestMetrics) *Handler {
	appState := &state.State{}
	appState.SetModels([]state.Model{{ID: "claude-test", SupportedEndpoints: []string{"/v1/messages"}}})
	return New(Dependencies{State: appState, Metrics: metrics, Copilot: upstream, Config: responsesTestConfig{}})
}

func cacheControl(t *testing.T, block map[string]any) map[string]any {
	t.Helper()
	control, ok := block["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("missing cache_control in %v", block)
	}
	return control
}

func TestNativeCacheControlsAndUsageOnWire(t *testing.T) {
	upstream := &cacheTestCopilot{response: `{"id":"m","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"cache_read_input_tokens":70,"cache_creation_input_tokens":20,"output_tokens":5}}`}
	metrics := &cacheTestMetrics{}
	h := cacheTestHandler(upstream, metrics)
	body := `{
		"model":"claude-test","max_tokens":20,
		"system":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral","scope":"ignored"}}],
		"tools":[{"name":"example","input_schema":{"type":"object","properties":{"cache_control":{"type":"object","scope":"keep-schema"}}},"cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"example","input":{"cache_control":{"scope":"keep-input"}}}]},
			{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Anthropic-Beta", "extended-cache-ttl-2025-04-11,other-beta")
	recorder := httptest.NewRecorder()
	h.Messages(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if upstream.beta != "other-beta" {
		t.Fatalf("forwarded beta = %q", upstream.beta)
	}
	var sent map[string]any
	if err := json.Unmarshal(upstream.body, &sent); err != nil {
		t.Fatal(err)
	}
	system := sent["system"].([]any)[0].(map[string]any)
	tool := sent["tools"].([]any)[0].(map[string]any)
	messages := sent["messages"].([]any)
	user := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	for _, block := range []map[string]any{system, tool, user} {
		cc := cacheControl(t, block)
		if cc["type"] != "ephemeral" || cc["ttl"] != "1h" || cc["scope"] != nil {
			t.Errorf("forwarded cache control = %v", cc)
		}
	}
	property := tool["input_schema"].(map[string]any)["properties"].(map[string]any)["cache_control"].(map[string]any)
	input := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)["input"].(map[string]any)["cache_control"].(map[string]any)
	if property["scope"] != "keep-schema" || property["ttl"] != nil || input["scope"] != "keep-input" || input["ttl"] != nil {
		t.Fatalf("tool data was changed: schema=%v input=%v", property, input)
	}
	if got := metrics.record; got.InputTokens != 3 || got.CachedTokens != 70 || got.CacheCreationTokens != 20 || got.OutputTokens != 5 {
		t.Fatalf("recorded usage = %+v", got)
	}
}

func TestNativeForceCacheTTLOverridesExplicitValues(t *testing.T) {
	upstream := &cacheTestCopilot{response: `{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`}
	h := cacheTestHandler(upstream, &cacheTestMetrics{})
	h.forceCacheTTL = "1h"
	body := `{"model":"claude-test","max_tokens":20,
		"system":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral","ttl":"5m"}}],
		"tools":[{"name":"example","input_schema":{"type":"object","properties":{"cache_control":{"ttl":"5m"}}}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`
	response := httptest.NewRecorder()
	h.Messages(response, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var sent map[string]any
	if err := json.Unmarshal(upstream.body, &sent); err != nil {
		t.Fatal(err)
	}
	system := sent["system"].([]any)[0].(map[string]any)
	user := sent["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if cacheControl(t, system)["ttl"] != "1h" || cacheControl(t, user)["ttl"] != "1h" {
		t.Fatalf("one-hour TTL not forced: system=%v user=%v", system, user)
	}
	schema := sent["tools"].([]any)[0].(map[string]any)["input_schema"].(map[string]any)["properties"].(map[string]any)["cache_control"].(map[string]any)
	if schema["ttl"] != "5m" {
		t.Fatalf("tool schema modified: %v", schema)
	}
}

func TestResponsesViaMessagesCachesAndReportsUsage(t *testing.T) {
	upstream := &cacheTestCopilot{response: `{"id":"m","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"cache_read_input_tokens":70,"cache_creation_input_tokens":20,"output_tokens":5}}`}
	metrics := &cacheTestMetrics{}
	h := cacheTestHandler(upstream, metrics)
	body := `{"model":"claude-test","instructions":"stable","input":"hello","tools":[{"type":"function","name":"example","parameters":{"type":"object"}},{"type":"function","name":"web_search","parameters":{"type":"object"}}]}`
	recorder := httptest.NewRecorder()
	h.Responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var sent map[string]any
	if err := json.Unmarshal(upstream.body, &sent); err != nil {
		t.Fatal(err)
	}
	system := sent["system"].([]any)[0].(map[string]any)
	tools := sent["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("unsupported tool was not removed: %v", tools)
	}
	message := sent["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	for _, block := range []map[string]any{system, tools[0].(map[string]any), message} {
		if cc := cacheControl(t, block); cc["type"] != "ephemeral" || cc["ttl"] != "1h" {
			t.Errorf("forwarded cache control = %v", cc)
		}
	}
	var result ResponsesResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil || result.Usage.InputTokens != 93 || result.Usage.InputTokensDetails == nil || result.Usage.InputTokensDetails.CachedTokens != 70 {
		t.Fatalf("translated usage = %+v", result.Usage)
	}
	if got := metrics.record; got.InputTokens != 3 || got.CachedTokens != 70 || got.CacheCreationTokens != 20 {
		t.Fatalf("recorded usage = %+v", got)
	}
}

func TestNativeStreamUpdatesCachedUsage(t *testing.T) {
	upstream := &cacheTestCopilot{response: strings.Join([]string{
		`event: message_start`, `data: {"message":{"usage":{"input_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":20}}}`, "",
		`event: message_delta`, `data: {"delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":0,"cache_read_input_tokens":5,"cache_creation_input_tokens":12,"output_tokens":7}}`, "",
		`event: message_stop`, `data: {"type":"message_stop"}`, "",
	}, "\n") + "\n"}
	metrics := &cacheTestMetrics{}
	h := cacheTestHandler(upstream, metrics)
	recorder := httptest.NewRecorder()
	h.Messages(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-test","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hello"}]}`,
	)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"cache_creation_input_tokens":12`) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := metrics.record; got.InputTokens != 0 || got.CachedTokens != 5 || got.CacheCreationTokens != 12 || got.OutputTokens != 7 {
		t.Fatalf("recorded usage = %+v", got)
	}
}
