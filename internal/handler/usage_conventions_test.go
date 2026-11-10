package handler

import (
	"encoding/json"
	"testing"
)

func TestChatUsageToAnthropic(t *testing.T) {
	tests := []struct {
		name                          string
		raw                           string
		input, output, cached, writes int
	}{
		{"plain", `{"prompt_tokens":10,"completion_tokens":5}`, 10, 5, 0, 0},
		{"cached excluded from input", `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":60}}`, 40, 5, 60, 0},
		{"cache writes excluded from input", `{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":60,"cache_write_tokens":20}}`, 20, 5, 60, 20},
		{"gemini reasoning only in total", `{"prompt_tokens":2,"completion_tokens":0,"reasoning_tokens":5,"total_tokens":7}`, 2, 5, 0, 0},
		{"total smaller than completion", `{"prompt_tokens":2,"completion_tokens":9,"total_tokens":3}`, 2, 9, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var u ChatCompletionUsage
			if err := json.Unmarshal([]byte(tt.raw), &u); err != nil {
				t.Fatal(err)
			}
			got := u.toAnthropic()
			if got.InputTokens != tt.input || got.OutputTokens != tt.output || got.CacheReadInputTokens != tt.cached || got.CacheCreationInputTokens != tt.writes {
				t.Errorf("got %+v, want input=%d output=%d cached=%d writes=%d", got, tt.input, tt.output, tt.cached, tt.writes)
			}
		})
	}
}

func messageDeltaUsage(t *testing.T, events []SSEEvent) DeltaUsage {
	t.Helper()
	for _, e := range events {
		if d, ok := e.Data.(MessageDeltaEvent); ok {
			return d.Usage
		}
	}
	t.Fatalf("no message_delta in %v", eventNames(events))
	return DeltaUsage{}
}

func TestChatStreamMessageDeltaCarriesInput(t *testing.T) {
	// message_start goes out on the first chunk, before usage is known, so the
	// final message_delta must carry the input counts.
	text, reason, writes := "hi", "stop", 20
	s := NewAnthropicStreamState("m")
	s.TranslateChunk(&ChatCompletionChunk{ID: "x", Model: "m",
		Choices: []ChatCompletionChunkChoice{{Delta: ChatCompletionChunkDelta{Content: &text}}}})
	events := s.TranslateChunk(&ChatCompletionChunk{ID: "x", Model: "m",
		Choices: []ChatCompletionChunkChoice{{FinishReason: &reason}},
		Usage: &ChatCompletionUsage{PromptTokens: 100, CompletionTokens: 7,
			PromptTokensDetails: &PromptTokensDetails{CachedTokens: 60, CacheWriteTokens: &writes}}})
	got := messageDeltaUsage(t, events)
	if got.InputTokens == nil || *got.InputTokens != 20 || got.CacheReadInputTokens == nil || *got.CacheReadInputTokens != 60 || got.CacheCreationInputTokens == nil || *got.CacheCreationInputTokens != 20 || got.OutputTokens != 7 {
		t.Errorf("message_delta usage = %+v", got)
	}
}

func TestChatStreamMessageDeltaDistinguishesMissingAndZeroUsage(t *testing.T) {
	stop := "stop"
	for _, tt := range []struct {
		name  string
		usage *ChatCompletionUsage
	}{
		{"missing", nil},
		{"zero", &ChatCompletionUsage{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := NewAnthropicStreamState("m")
			events := s.TranslateChunk(&ChatCompletionChunk{
				Choices: []ChatCompletionChunkChoice{{FinishReason: &stop}},
				Usage:   tt.usage,
			})
			got := messageDeltaUsage(t, events)
			if (got.InputTokens != nil) != (tt.usage != nil) || (got.CacheReadInputTokens != nil) != (tt.usage != nil) {
				t.Fatalf("usage presence = %+v", got)
			}
			if got.InputTokens != nil && (*got.InputTokens != 0 || *got.CacheReadInputTokens != 0) {
				t.Fatalf("usage counts = %+v", got)
			}
		})
	}
}

func TestResponsesStreamMessageDeltaCarriesInput(t *testing.T) {
	s := NewResponsesStreamState("m")
	if _, err := s.TranslateEvent("response.created", `{"response":{"id":"r","model":"m"}}`); err != nil {
		t.Fatal(err)
	}
	events, err := s.TranslateEvent("response.completed", `{"response":{"status":"completed","output":[],
		"usage":{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":60,"cache_write_tokens":20}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	got := messageDeltaUsage(t, events)
	if got.InputTokens == nil || *got.InputTokens != 20 || got.CacheReadInputTokens == nil || *got.CacheReadInputTokens != 60 || got.CacheCreationInputTokens == nil || *got.CacheCreationInputTokens != 20 || got.OutputTokens != 7 {
		t.Errorf("message_delta usage = %+v", got)
	}
}

func TestAnthropicUsageToResponses(t *testing.T) {
	got := anthropicUsageToResponses(AnthropicUsage{InputTokens: 10, OutputTokens: 5, CacheReadInputTokens: 60, CacheCreationInputTokens: 30})
	if got.InputTokens != 100 || got.OutputTokens != 5 || got.TotalTokens != 105 ||
		got.InputTokensDetails == nil || got.InputTokensDetails.CachedTokens != 60 ||
		got.InputTokensDetails.CacheWriteTokens == nil || *got.InputTokensDetails.CacheWriteTokens != 30 {
		t.Errorf("got %+v", got)
	}
}

func TestResponsesStreamWriterUsage(t *testing.T) {
	var completed map[string]any
	w := newResponsesStreamWriter(func(event string, data any) error {
		if event == "response.completed" {
			completed = data.(map[string]any)
		}
		return nil
	}, "m", nil)
	for _, ev := range [][2]string{
		{"message_start", `{"message":{"id":"x","usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":60,"cache_creation_input_tokens":30}}}`},
		{"message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`},
		{"message_stop", `{}`},
	} {
		if err := w.Handle(ev[0], ev[1]); err != nil {
			t.Fatal(err)
		}
	}
	got := completed["response"].(map[string]any)["usage"].(ResponsesUsage)
	if got.InputTokens != 100 || got.TotalTokens != 105 || got.InputTokensDetails.CachedTokens != 60 ||
		got.InputTokensDetails.CacheWriteTokens == nil || *got.InputTokensDetails.CacheWriteTokens != 30 {
		t.Errorf("got %+v", got)
	}
}

func TestResponsesStreamWriterExplicitZeroUsage(t *testing.T) {
	var completed map[string]any
	w := newResponsesStreamWriter(func(event string, data any) error {
		if event == "response.completed" {
			completed = data.(map[string]any)
		}
		return nil
	}, "m", nil)
	for _, ev := range [][2]string{
		{"message_start", `{"message":{"id":"x","usage":{"input_tokens":10,"cache_read_input_tokens":60,"cache_creation_input_tokens":30}}}`},
		{"message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":5}}`},
		{"message_stop", `{}`},
	} {
		if err := w.Handle(ev[0], ev[1]); err != nil {
			t.Fatal(err)
		}
	}
	got := completed["response"].(map[string]any)["usage"].(ResponsesUsage)
	if got.InputTokens != 0 || got.TotalTokens != 5 || got.InputTokensDetails != nil {
		t.Errorf("got %+v", got)
	}
}
