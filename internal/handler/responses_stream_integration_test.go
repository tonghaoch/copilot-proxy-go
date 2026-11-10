package handler

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type streamingMessagesCopilot struct {
	capturingResponsesCopilot
	body io.ReadCloser
	err  error
}

func (c *streamingMessagesCopilot) ProxyMessages(context.Context, []byte, string, bool, bool) (*http.Response, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: c.body}, nil
}

func (c *streamingMessagesCopilot) ProxyChatCompletionEx(context.Context, []byte, bool, bool) (*http.Response, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: c.body}, nil
}

func streamingResponsesHandler(upstream *streamingMessagesCopilot, endpoint string) *Handler {
	models := &state.State{}
	models.SetModels([]state.Model{{ID: "claude-test", SupportedEndpoints: []string{endpoint}}})
	return New(Dependencies{State: models, Metrics: responsesTestMetrics{}, Copilot: upstream, Config: responsesTestConfig{}})
}

func TestResponsesViaMessagesStreamsBeforeUpstreamFinishes(t *testing.T) {
	tests := []struct {
		name, endpoint string
		start, end     []string
	}{
		{
			name: "native", endpoint: "/v1/messages",
			start: []string{
				`event: message_start`, `data: {"message":{"id":"m","usage":{"input_tokens":1}}}`, "",
				`event: content_block_start`, `data: {"content_block":{"type":"text","text":""}}`, "",
				`event: content_block_delta`, `data: {"delta":{"type":"text_delta","text":"hello"}}`, "",
			},
			end: []string{
				`event: content_block_stop`, `data: {}`, "",
				`event: message_delta`, `data: {"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, "",
				`event: message_stop`, `data: {}`, "",
			},
		},
		{
			name: "chat", endpoint: "/chat/completions",
			start: []string{
				`data: {"id":"c","model":"claude-test","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`, "",
			},
			end: []string{
				`data: {"id":"c","model":"claude-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testResponsesViaMessagesStreaming(t, tt.endpoint, tt.start, tt.end)
		})
	}
}

func testResponsesViaMessagesStreaming(t *testing.T, endpoint string, start, end []string) {
	t.Helper()
	reader, writer := io.Pipe()
	sendTerminal := make(chan struct{})
	stopProducer := make(chan struct{})
	upstream := &streamingMessagesCopilot{body: reader}
	server := httptest.NewServer(http.HandlerFunc(streamingResponsesHandler(upstream, endpoint).Responses))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		close(stopProducer)
		writer.Close()
		server.Close()
	})
	go func() {
		io.WriteString(writer, strings.Join(start, "\n")+"\n")
		select {
		case <-sendTerminal:
			io.WriteString(writer, strings.Join(end, "\n")+"\n")
			<-stopProducer
		case <-stopProducer:
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(
		`{"model":"claude-test","input":"hello","stream":true}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Timeout = 5 * time.Second
	type result struct {
		resp *http.Response
		err  error
	}
	response := make(chan result, 1)
	go func() {
		resp, err := client.Do(req)
		response <- result{resp, err}
	}()
	var resp *http.Response
	select {
	case got := <-response:
		if got.err != nil {
			t.Fatal(got.err)
		}
		resp = got.resp
	case <-time.After(2 * time.Second):
		t.Fatal("no response was flushed while upstream was still generating")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	stream := bufio.NewReader(resp.Body)
	for {
		line, err := stream.ReadString('\n')
		if err != nil {
			t.Fatalf("reading streamed event: %v", err)
		}
		if strings.Contains(line, `"delta":"hello"`) {
			break
		}
	}

	close(sendTerminal)
	finished := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(stream)
		finished <- err
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("reading completed stream: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("response did not end after message_stop while upstream stayed open")
	}
}

func TestResponsesViaMessagesPreservesUpstreamStreamingErrors(t *testing.T) {
	upstream := &streamingMessagesCopilot{err: &api.HTTPError{
		StatusCode: http.StatusTooManyRequests,
		Message:    "rate limited",
		Header:     http.Header{"Retry-After": []string{"17"}},
	}}
	recorder := httptest.NewRecorder()
	streamingResponsesHandler(upstream, "/v1/messages").Responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"claude-test","input":"hello","stream":true}`,
	)))
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "17" || !strings.Contains(recorder.Body.String(), "rate limited") {
		t.Fatalf("error response = %d %v %s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestResponsesViaMessagesReportsTruncatedStreams(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		want       string
	}{
		{"empty", "", http.StatusBadGateway, "upstream stream ended without a terminal event"},
		{"partial", "event: message_start\ndata: {\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n", http.StatusOK, `"code":"stream_error"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &streamingMessagesCopilot{body: io.NopCloser(strings.NewReader(tt.body))}
			recorder := httptest.NewRecorder()
			streamingResponsesHandler(upstream, "/v1/messages").Responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
				`{"model":"claude-test","input":"hello","stream":true}`,
			)))
			if recorder.Code != tt.status || !strings.Contains(recorder.Body.String(), tt.want) || strings.Contains(recorder.Body.String(), "response.completed") {
				t.Fatalf("truncated stream = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
