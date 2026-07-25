package handler

import (
	"strings"
	"testing"
)

func TestReadSSEHandlesEventsAboveOneMegabyte(t *testing.T) {
	// bufio.Scanner aborts the whole stream on a long line. Real streams exceed
	// a megabyte on a single event: a large apply_patch argument blob, or a
	// response.completed carrying the full response object.
	sizes := []int{500 << 10, 1 << 20, 4 << 20, 16 << 20}
	for _, size := range sizes {
		payload := `{"delta":"` + strings.Repeat("x", size) + `"}`
		stream := "event: t\ndata: " + payload + "\n\n"

		var got string
		count := 0
		err := readSSE(strings.NewReader(stream), func(_, data string) error {
			count++
			got = data
			return nil
		})
		if err != nil {
			t.Errorf("%d KB: unexpected error %v", size>>10, err)
			continue
		}
		if count != 1 {
			t.Errorf("%d KB: got %d events, want 1", size>>10, count)
		}
		if len(got) != len(payload) {
			t.Errorf("%d KB: got %d bytes, want %d", size>>10, len(got), len(payload))
		}
	}
}

func TestReadSSEFraming(t *testing.T) {
	tests := []struct {
		name       string
		stream     string
		wantEvents []string
		wantTypes  []string
	}{
		{
			name:       "stops at DONE",
			stream:     "data: {\"a\":1}\n\ndata: [DONE]\n\ndata: {\"b\":2}\n\n",
			wantEvents: []string{`{"a":1}`},
			wantTypes:  []string{""},
		},
		{
			name:       "final line without trailing newline",
			stream:     "data: {\"x\":1}",
			wantEvents: []string{`{"x":1}`},
			wantTypes:  []string{""},
		},
		{
			name:       "CRLF line endings",
			stream:     "event: e\r\ndata: {\"y\":1}\r\n\r\n",
			wantEvents: []string{`{"y":1}`},
			wantTypes:  []string{"e"},
		},
		{
			name:       "event type resets between events",
			stream:     "event: a\ndata: 1\n\ndata: 2\n\n",
			wantEvents: []string{"1", "2"},
			wantTypes:  []string{"a", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var data, types []string
			if err := readSSE(strings.NewReader(tt.stream), func(eventType, d string) error {
				types = append(types, eventType)
				data = append(data, d)
				return nil
			}); err != nil {
				t.Fatalf("readSSE: %v", err)
			}
			if strings.Join(data, "|") != strings.Join(tt.wantEvents, "|") {
				t.Errorf("data = %q, want %q", data, tt.wantEvents)
			}
			if strings.Join(types, "|") != strings.Join(tt.wantTypes, "|") {
				t.Errorf("types = %q, want %q", types, tt.wantTypes)
			}
		})
	}
}

func eventNames(events []SSEEvent) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e.Event)
	}
	return names
}

func TestChatStreamTerminatesWhenUpstreamTruncates(t *testing.T) {
	// Upstream can stop before finish_reason. Without the terminating events the
	// client waits forever on a message that never ends.
	text := "hello"
	state := NewAnthropicStreamState("m")
	state.TranslateChunk(&ChatCompletionChunk{ID: "x", Model: "m",
		Choices: []ChatCompletionChunkChoice{{Delta: ChatCompletionChunkDelta{Content: &text}}}})

	if state.IsComplete() {
		t.Fatal("stream should not be complete before finish_reason")
	}
	got := strings.Join(eventNames(state.Finish("end_turn")), ",")
	if got != "content_block_stop,message_delta,message_stop" {
		t.Errorf("Finish() = %q, want the open block closed and the message ended", got)
	}
}

func TestChatStreamFinishIsIdempotent(t *testing.T) {
	text, reason := "hi", "stop"
	state := NewAnthropicStreamState("m")
	state.TranslateChunk(&ChatCompletionChunk{ID: "x", Model: "m",
		Choices: []ChatCompletionChunkChoice{{Delta: ChatCompletionChunkDelta{Content: &text}}}})
	state.TranslateChunk(&ChatCompletionChunk{ID: "x", Model: "m",
		Choices: []ChatCompletionChunkChoice{{FinishReason: &reason}}})

	if !state.IsComplete() {
		t.Fatal("finish_reason should mark the stream complete")
	}
	if events := state.Finish("end_turn"); len(events) != 0 {
		t.Errorf("Finish() after a normal end emitted %v, want nothing", eventNames(events))
	}
}

func TestChatStreamFinishWithoutAnyChunks(t *testing.T) {
	// Upstream closed before sending anything: the client still needs a
	// well-formed, terminated message.
	state := NewAnthropicStreamState("m")
	got := strings.Join(eventNames(state.Finish("end_turn")), ",")
	if got != "message_start,message_delta,message_stop" {
		t.Errorf("Finish() = %q, want a complete empty message", got)
	}
}
