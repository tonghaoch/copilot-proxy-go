package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type translatedResponsesCopilot struct {
	capturingResponsesCopilot
	body   []byte
	agent  bool
	vision bool
	calls  int
}

func (c *translatedResponsesCopilot) ProxyMessages(_ context.Context, body []byte, _ string, vision, agent bool) (*http.Response, error) {
	c.body, c.vision, c.agent = append([]byte(nil), body...), vision, agent
	c.calls++
	response := `{"id":"m","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	if strings.Contains(string(body), `"stream":true`) {
		response = strings.Join([]string{
			`event: message_start`, `data: {"message":{"id":"m","usage":{"input_tokens":1}}}`, "",
			`event: message_delta`, `data: {"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, "",
			`event: message_stop`, `data: {}`, "",
		}, "\n") + "\n"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
}

func (c *translatedResponsesCopilot) ProxyChatCompletionEx(_ context.Context, body []byte, agent, vision bool) (*http.Response, error) {
	c.body, c.vision, c.agent = append([]byte(nil), body...), vision, agent
	c.calls++
	response := `{"id":"c","model":"claude-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	if strings.Contains(string(body), `"stream":true`) {
		response = `data: {"id":"c","model":"claude-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}` + "\n\n"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
}

func TestTranslatedResponsesPreserveRequestSemantics(t *testing.T) {
	for _, endpoint := range []string{"/v1/messages", "/chat/completions"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", endpoint, streaming), func(t *testing.T) {
				upstream := &translatedResponsesCopilot{}
				metrics := &cacheTestMetrics{}
				app := &state.State{}
				model := state.Model{ID: "claude-test", SupportedEndpoints: []string{endpoint}}
				model.Capabilities.Limits.MaxOutputTokens = 100
				model.Capabilities.Limits.MaxNonStreamingOutput = 20
				model.Capabilities.Supports.AdaptiveThinking = true
				app.SetModels([]state.Model{model})
				h := New(Dependencies{State: app, Metrics: metrics, Copilot: upstream, Config: responsesTestConfig{}})
				body := fmt.Sprintf(`{
					"model":"claude-test","stream":%t,"max_output_tokens":200,"reasoning":{"effort":"none"},
					"input":[
						{"type":"message","role":"user","content":"inspect"},
						{"type":"function_call","name":"inspect","call_id":"c1","arguments":"{}"},
						{"type":"function_call_output","call_id":"c1","output":[
							{"type":"input_text","text":"result"},
							{"type":"input_image","image_url":"https://example.test/image.png"}
						]}
					]
				}`, streaming)
				recorder := httptest.NewRecorder()
				h.Responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
				if recorder.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
				}
				if !upstream.agent || !upstream.vision || !metrics.record.HasVision ||
					metrics.record.Initiator != "agent" || metrics.record.Streaming != streaming {
					t.Fatalf("upstream agent=%v vision=%v, metrics=%+v", upstream.agent, upstream.vision, metrics.record)
				}
				var sent map[string]any
				if err := json.Unmarshal(upstream.body, &sent); err != nil {
					t.Fatal(err)
				}
				wantLimit := float64(20)
				if streaming {
					wantLimit = 100
				}
				if sent["max_tokens"] != wantLimit {
					t.Fatalf("max_tokens = %v, want %v", sent["max_tokens"], wantLimit)
				}
				if endpoint == "/v1/messages" {
					thinking := sent["thinking"].(map[string]any)
					if thinking["type"] != "disabled" || thinking["budget_tokens"] != nil {
						t.Fatalf("thinking = %v", thinking)
					}
				} else if sent["reasoning_effort"] != "none" || !strings.Contains(string(upstream.body), `"url":"https://example.test/image.png"`) {
					t.Fatalf("chat effort or image lost: %s", upstream.body)
				}
			})
		}
	}
}

func TestTranslatedResponsesRejectPreviousResponseID(t *testing.T) {
	upstream := &translatedResponsesCopilot{}
	app := &state.State{}
	app.SetModels([]state.Model{{ID: "claude-test", SupportedEndpoints: []string{"/v1/messages"}}})
	h := New(Dependencies{State: app, Metrics: responsesTestMetrics{}, Copilot: upstream, Config: responsesTestConfig{}})
	recorder := httptest.NewRecorder()
	h.Responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"claude-test","input":"hello","previous_response_id":"resp_previous"}`,
	)))
	if recorder.Code != http.StatusBadRequest || upstream.calls != 0 || !strings.Contains(recorder.Body.String(), "previous_response_id") {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, upstream.calls, recorder.Body.String())
	}
}

func TestTranslatedResponsesKeepAssistantInitiator(t *testing.T) {
	for _, endpoint := range []string{"/v1/messages", "/chat/completions"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", endpoint, streaming), func(t *testing.T) {
				upstream := &translatedResponsesCopilot{}
				metrics := &cacheTestMetrics{}
				app := &state.State{}
				app.SetModels([]state.Model{{ID: "claude-test", SupportedEndpoints: []string{endpoint}}})
				h := New(Dependencies{State: app, Metrics: metrics, Copilot: upstream, Config: responsesTestConfig{}})
				body := fmt.Sprintf(`{"model":"claude-test","stream":%t,"input":[{"type":"message","role":"assistant","content":"finished"}]}`, streaming)
				recorder := httptest.NewRecorder()
				h.Responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
				if recorder.Code != http.StatusOK || !upstream.agent || metrics.record.Initiator != "agent" {
					t.Fatalf("status=%d agent=%v metrics=%+v body=%s", recorder.Code, upstream.agent, metrics.record, recorder.Body.String())
				}
				if !strings.Contains(string(upstream.body), "Continue.") {
					t.Fatalf("test did not exercise the inserted user turn: %s", upstream.body)
				}
			})
		}
	}
}

func TestTranslatedResponsesRejectUnsupportedToolOutput(t *testing.T) {
	for _, part := range []string{
		`{"type":"input_image","file_id":"file_123"}`,
		`{"type":"input_file","file_id":"file_123"}`,
	} {
		t.Run(part, func(t *testing.T) {
			upstream := &translatedResponsesCopilot{}
			app := &state.State{}
			app.SetModels([]state.Model{{ID: "claude-test", SupportedEndpoints: []string{"/v1/messages"}}})
			h := New(Dependencies{State: app, Metrics: responsesTestMetrics{}, Copilot: upstream, Config: responsesTestConfig{}})
			body := `{"model":"claude-test","input":[{"type":"function_call_output","call_id":"c","output":[` + part + `]}]}`
			recorder := httptest.NewRecorder()
			h.Responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
			if recorder.Code != http.StatusBadRequest || upstream.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", recorder.Code, upstream.calls, recorder.Body.String())
			}
		})
	}
}

func TestTranslatedResponsesBoundDefaultOutput(t *testing.T) {
	for _, tt := range []struct {
		stream bool
		want   float64
	}{{false, 4096}, {true, 8192}} {
		t.Run(fmt.Sprintf("stream=%v", tt.stream), func(t *testing.T) {
			upstream := &translatedResponsesCopilot{}
			app := &state.State{}
			model := state.Model{ID: "claude-test", SupportedEndpoints: []string{"/v1/messages"}}
			model.Capabilities.Limits.MaxOutputTokens = 8192
			model.Capabilities.Limits.MaxNonStreamingOutput = 4096
			app.SetModels([]state.Model{model})
			h := New(Dependencies{State: app, Metrics: responsesTestMetrics{}, Copilot: upstream, Config: responsesTestConfig{}})
			body := fmt.Sprintf(`{"model":"claude-test","stream":%t,"input":"hello"}`, tt.stream)
			recorder := httptest.NewRecorder()
			h.Responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
			var sent map[string]any
			if err := json.Unmarshal(upstream.body, &sent); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusOK || sent["max_tokens"] != tt.want {
				t.Fatalf("status=%d max_tokens=%v body=%s", recorder.Code, sent["max_tokens"], recorder.Body.String())
			}
		})
	}
}
