package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func TestAnthropicStreamStateCapturesFinalUsageOnlyChunk(t *testing.T) {
	stream := NewAnthropicStreamState("gpt-4.1")
	stream.TranslateChunk(&ChatCompletionChunk{
		ID:      "chatcmpl-test",
		Model:   "gpt-4.1",
		Choices: []ChatCompletionChunkChoice{{}},
	})
	stream.TranslateChunk(&ChatCompletionChunk{
		Usage: &ChatCompletionUsage{
			PromptTokens:     120,
			CompletionTokens: 30,
			PromptTokensDetails: &PromptTokensDetails{
				CachedTokens: 40,
			},
		},
	})

	input, output, cached := stream.TokenCounts()
	if input != 80 || output != 30 || cached != 40 {
		t.Fatalf("unexpected token counts: input=%d output=%d cached=%d", input, output, cached)
	}
}

func TestChatStreamForwardsLateCacheUsage(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"id":"x","model":"m","choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":0}}`, "",
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, "",
		`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":7,"total_tokens":107,"prompt_tokens_details":{"cached_tokens":60,"cache_write_tokens":20}}}`, "",
		`data: [DONE]`, "",
	}, "\n") + "\n"
	recorder := httptest.NewRecorder()
	rec := &state.RequestRecord{}
	New(Dependencies{}).streamChatToAnthropic(recorder, &http.Response{Body: io.NopCloser(strings.NewReader(raw))}, "m", rec)
	var delta MessageDeltaEvent
	stops := 0
	err := readSSE(bytes.NewReader(recorder.Body.Bytes()), func(event, data string) error {
		switch event {
		case "message_delta":
			return json.Unmarshal([]byte(data), &delta)
		case "message_stop":
			stops++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stops != 1 || delta.Usage.InputTokens == nil || *delta.Usage.InputTokens != 20 ||
		delta.Usage.CacheReadInputTokens == nil || *delta.Usage.CacheReadInputTokens != 60 ||
		delta.Usage.CacheCreationInputTokens == nil || *delta.Usage.CacheCreationInputTokens != 20 || delta.Usage.OutputTokens != 7 {
		t.Fatalf("final usage = %+v, stops = %d, body = %s", delta.Usage, stops, recorder.Body.String())
	}
	if rec.InputTokens != 20 || rec.CachedTokens != 60 || rec.CacheCreationTokens != 20 || rec.OutputTokens != 7 {
		t.Fatalf("recorded usage = %+v", rec)
	}
}

func TestChatStreamTerminatesIfFinalUsageNeverArrives(t *testing.T) {
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer writer.Close()
		io.WriteString(writer, "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		<-done
	}()
	defer close(done)
	recorder := httptest.NewRecorder()
	rec := &state.RequestRecord{}
	start := time.Now()
	New(Dependencies{}).streamChatToAnthropic(recorder, &http.Response{Body: reader}, "m", rec)
	if elapsed := time.Since(start); elapsed > finalChatUsageWait+time.Second {
		t.Fatalf("stream did not terminate after finish_reason: %s", elapsed)
	}
	if !strings.Contains(recorder.Body.String(), "event: message_stop") || rec.Error != "" || rec.OutputUsageKnown {
		t.Fatalf("unexpected terminal stream: %s; record=%+v", recorder.Body.String(), rec)
	}
}

func TestChatStreamReportsUnexpectedEOF(t *testing.T) {
	raw := "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
	recorder := httptest.NewRecorder()
	rec := &state.RequestRecord{}
	New(Dependencies{}).streamChatToAnthropic(recorder, &http.Response{Body: io.NopCloser(strings.NewReader(raw))}, "m", rec)
	if body := recorder.Body.String(); !strings.Contains(body, "event: error") || !strings.Contains(body, "event: message_stop") || !strings.Contains(rec.Error, "finish_reason") {
		t.Fatalf("unexpected EOF was not reported and terminated: body=%s record=%+v", body, rec)
	}
}
