package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func TestConvertLocalShellTools(t *testing.T) {
	tools := []any{
		map[string]any{"type": "local_shell"},
		map[string]any{"type": "function", "name": "existing"},
	}

	got := convertLocalShellTools(tools)
	if len(got) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(got))
	}

	converted, ok := got[0].(map[string]any)
	if !ok {
		t.Fatalf("expected converted tool to be an object, got %T", got[0])
	}
	if converted["type"] != "function" {
		t.Fatalf("expected local_shell to become function, got %v", converted["type"])
	}
	if converted["name"] != "local_shell" {
		t.Fatalf("expected function name local_shell, got %v", converted["name"])
	}
	params, ok := converted["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters object, got %T", converted["parameters"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties object, got %T", params["properties"])
	}
	if _, ok := props["command"]; !ok {
		t.Fatalf("expected command property in local_shell schema")
	}
}

func TestNormalizeResponsesToolDescriptions(t *testing.T) {
	raw := `{
		"tools": [
			{
				"type": "function",
				"name": "top_level",
				"parameters": {
					"type": "object",
					"properties": {
						"value": {"type": "string", "description": ""}
					}
				}
			},
			{"type": "function", "name": "preserved", "description": "Keep this description."},
			{"type": "code_interpreter"},
			{"type": "custom", "description": ""}
		],
		"input": [
			{
				"type": "additional_tools",
				"role": "developer",
				"tools": [
					{
						"type": "namespace",
						"name": "functions",
						"description": "",
						"tools": [
							{"type": "custom", "name": "exec", "description": "   "}
						]
					}
				]
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "hello", "description": ""}]
			}
		]
	}`

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	normalizeResponsesToolDescriptions(payload)

	topTools := payload["tools"].([]any)
	topLevel := topTools[0].(map[string]any)
	if got := topLevel["description"]; got != "Invoke the top_level tool." {
		t.Errorf("top-level fallback = %q", got)
	}
	if got := topTools[1].(map[string]any)["description"]; got != "Keep this description." {
		t.Errorf("valid description changed to %q", got)
	}
	if _, present := topTools[2].(map[string]any)["description"]; present {
		t.Error("built-in tool gained a description")
	}
	if got := topTools[3].(map[string]any)["description"]; got != "Tool provided by the client." {
		t.Errorf("unnamed tool fallback = %q", got)
	}
	property := topLevel["parameters"].(map[string]any)["properties"].(map[string]any)["value"].(map[string]any)
	if got := property["description"]; got != "" {
		t.Errorf("schema description changed to %q", got)
	}

	input := payload["input"].([]any)
	namespace := input[0].(map[string]any)["tools"].([]any)[0].(map[string]any)
	if got := namespace["description"]; got != "Tools in the functions namespace." {
		t.Errorf("namespace fallback = %q", got)
	}
	child := namespace["tools"].([]any)[0].(map[string]any)
	if got := child["description"]; got != "Invoke the exec tool." {
		t.Errorf("nested tool fallback = %q", got)
	}
	content := input[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if got := content["description"]; got != "" {
		t.Errorf("non-tool description changed to %q", got)
	}
}

func TestResponsesNormalizesToolDescriptionsBeforeProxying(t *testing.T) {
	appState := &state.State{}
	appState.SetModels([]state.Model{{ID: "test-model", SupportedEndpoints: []string{"/responses"}}})
	upstream := &capturingResponsesCopilot{}
	h := New(Dependencies{
		State:   appState,
		Metrics: responsesTestMetrics{},
		Copilot: upstream,
		Config:  responsesTestConfig{},
	})

	body := `{
		"model": "test-model",
		"stream": false,
		"tools": [{"type": "function", "name": "top_level", "parameters": {"type": "object"}}],
		"input": [{
			"type": "additional_tools",
			"role": "developer",
			"tools": [{
				"type": "namespace",
				"name": "functions",
				"description": "",
				"tools": [{"type": "custom", "name": "exec", "description": "   "}]
			}]
		}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))
	recorder := httptest.NewRecorder()

	h.Responses(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var forwarded map[string]any
	if err := json.Unmarshal(upstream.body, &forwarded); err != nil {
		t.Fatalf("unmarshal forwarded body: %v", err)
	}
	topLevel := forwarded["tools"].([]any)[0].(map[string]any)
	if got := topLevel["description"]; got != "Invoke the top_level tool." {
		t.Errorf("forwarded top-level description = %q", got)
	}
	namespace := forwarded["input"].([]any)[0].(map[string]any)["tools"].([]any)[0].(map[string]any)
	if got := namespace["description"]; got != "Tools in the functions namespace." {
		t.Errorf("forwarded namespace description = %q", got)
	}
	child := namespace["tools"].([]any)[0].(map[string]any)
	if got := child["description"]; got != "Invoke the exec tool." {
		t.Errorf("forwarded nested description = %q", got)
	}
}

type capturingResponsesCopilot struct {
	body []byte
}

func (*capturingResponsesCopilot) FetchModels(context.Context) ([]state.Model, error) {
	panic("unexpected FetchModels call")
}

func (*capturingResponsesCopilot) ProxyChatCompletionEx(context.Context, []byte, bool, bool) (*http.Response, error) {
	panic("unexpected ProxyChatCompletionEx call")
}

func (*capturingResponsesCopilot) ProxyMessages(context.Context, []byte, string, bool, bool) (*http.Response, error) {
	panic("unexpected ProxyMessages call")
}

func (c *capturingResponsesCopilot) ProxyResponses(_ context.Context, body []byte, _, _ bool) (*http.Response, error) {
	c.body = append([]byte(nil), body...)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(`{"id":"response-test","status":"completed","output":[]}`)),
	}, nil
}

func (*capturingResponsesCopilot) ProxyEmbeddings(context.Context, []byte) (*http.Response, error) {
	panic("unexpected ProxyEmbeddings call")
}

type responsesTestMetrics struct{}

func (responsesTestMetrics) RecordRequest(state.RequestRecord)   {}
func (responsesTestMetrics) UpdateSession(state.SessionSnapshot) {}
func (responsesTestMetrics) Snapshot() state.MetricsSnapshot     { return state.MetricsSnapshot{} }

type responsesTestConfig struct{}

func (responsesTestConfig) Snapshot() *config.Config      { return &config.Config{} }
func (responsesTestConfig) APIKeys() []string             { return nil }
func (responsesTestConfig) ExtraPrompt(string) string     { return "" }
func (responsesTestConfig) ReasoningEffort(string) string { return "" }
