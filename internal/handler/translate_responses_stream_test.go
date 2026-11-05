package handler

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestResponsesStreamLifecycle(t *testing.T) {
	state := NewResponsesStreamState("fallback-model")

	events, err := state.TranslateEvent("response.created", `{
		"response":{"id":"resp-1","model":"test-model","usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":20}}}
	}`)
	if err != nil || len(events) != 1 || events[0].Event != "message_start" {
		t.Fatalf("unexpected created events: events=%+v err=%v", events, err)
	}
	if input, _, cached := state.TokenCounts(); input != 80 || cached != 20 {
		t.Fatalf("unexpected created usage: input=%d cached=%d", input, cached)
	}

	events, err = state.TranslateEvent("response.output_text.delta", `{"output_index":2,"content_index":3,"delta":"hello"}`)
	if err != nil || len(events) != 2 || events[0].Event != "content_block_start" || events[1].Event != "content_block_delta" {
		t.Fatalf("unexpected text events: events=%+v err=%v", events, err)
	}

	events, err = state.TranslateEvent("response.completed", `{
		"response":{"status":"completed","output":[],"usage":{"input_tokens":110,"output_tokens":7,"input_tokens_details":{"cached_tokens":30}}}
	}`)
	if err != nil || len(events) != 3 || events[0].Event != "content_block_stop" || events[2].Event != "message_stop" {
		t.Fatalf("unexpected completion events: events=%+v err=%v", events, err)
	}
	if !state.IsComplete() {
		t.Fatal("stream should be complete")
	}
	if input, output, cached := state.TokenCounts(); input != 80 || output != 7 || cached != 30 {
		t.Fatalf("unexpected final usage: input=%d output=%d cached=%d", input, output, cached)
	}
}

// Assert the serialized client-facing usage, not just the internal metrics:
// Responses commonly sends usage:null at creation and real counts only at completion.
func TestResponsesStreamFinalUsage(t *testing.T) {
	for _, status := range []string{"completed", "incomplete"} {
		for _, tc := range []struct {
			name         string
			initialUsage string
			finalUsage   string
			wantInput    int
			wantCached   int
			wantOutput   int
			wantUsage    bool
		}{
			{"late_uncached", `null`, `{"input_tokens":11,"output_tokens":5}`, 11, 0, 5, true},
			{"late_cached", `null`, `{"input_tokens":110,"output_tokens":7,"input_tokens_details":{"cached_tokens":30}}`, 80, 30, 7, true},
			{"fully_cached", `null`, `{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":100}}`, 0, 100, 7, true},
			{"updated_counts", `{"input_tokens":100,"input_tokens_details":{"cached_tokens":20}}`, `{"input_tokens":110,"output_tokens":7,"input_tokens_details":{"cached_tokens":30}}`, 80, 30, 7, true},
			{"explicit_zero", `{"input_tokens":100,"input_tokens_details":{"cached_tokens":20}}`, `{"input_tokens":0,"output_tokens":0}`, 0, 0, 0, true},
			{"missing_final_usage", `{"input_tokens":100,"input_tokens_details":{"cached_tokens":20}}`, `null`, 80, 20, 0, false},
		} {
			t.Run(status+"/"+tc.name, func(t *testing.T) {
				state := NewResponsesStreamState("test-model")
				created := fmt.Sprintf(`{"response":{"id":"resp-1","model":"test-model","usage":%s}}`, tc.initialUsage)
				if _, err := state.TranslateEvent("response.created", created); err != nil {
					t.Fatal(err)
				}
				completed := fmt.Sprintf(`{"response":{"status":%q,"output":[],"usage":%s}}`, status, tc.finalUsage)
				events, err := state.TranslateEvent("response."+status, completed)
				if err != nil || len(events) != 2 || events[0].Event != "message_delta" || events[1].Event != "message_stop" {
					t.Fatalf("unexpected final events: %+v, err=%v", events, err)
				}
				wire, err := json.Marshal(events[0].Data)
				if err != nil {
					t.Fatal(err)
				}
				var delta struct {
					Usage map[string]int `json:"usage"`
				}
				if err := json.Unmarshal(wire, &delta); err != nil {
					t.Fatal(err)
				}
				for key, want := range map[string]int{"input_tokens": tc.wantInput, "cache_read_input_tokens": tc.wantCached} {
					got, present := delta.Usage[key]
					if present != tc.wantUsage || (present && got != want) {
						t.Errorf("%s = %d (present=%v), want %d (present=%v); wire=%s", key, got, present, want, tc.wantUsage, wire)
					}
				}
				if got, present := delta.Usage["output_tokens"]; !present || got != tc.wantOutput {
					t.Errorf("output_tokens = %d (present=%v), want %d", got, present, tc.wantOutput)
				}
				if input, output, cached := state.TokenCounts(); input != tc.wantInput || output != tc.wantOutput || cached != tc.wantCached {
					t.Errorf("metrics = (%d, %d, %d), want (%d, %d, %d)", input, output, cached, tc.wantInput, tc.wantOutput, tc.wantCached)
				}
			})
		}
	}
}

func TestResponsesStreamFunctionCall(t *testing.T) {
	state := NewResponsesStreamState("test-model")
	events, err := state.TranslateEvent("response.output_item.added", `{
		"output_index":1,"item":{"type":"function_call","call_id":"call-1","name":"tool"}
	}`)
	if err != nil || len(events) != 1 || events[0].Event != "content_block_start" {
		t.Fatalf("unexpected function start: events=%+v err=%v", events, err)
	}

	events, err = state.TranslateEvent("response.function_call_arguments.delta", `{"output_index":1,"delta":"{\"ok\":"}`)
	if err != nil || len(events) != 1 || events[0].Event != "content_block_delta" {
		t.Fatalf("unexpected function delta: events=%+v err=%v", events, err)
	}

	events, err = state.TranslateEvent("response.output_item.done", `{
		"output_index":1,"item":{"type":"function_call","call_id":"call-1","name":"tool"}
	}`)
	if err != nil || len(events) != 1 || events[0].Event != "content_block_stop" {
		t.Fatalf("unexpected function stop: events=%+v err=%v", events, err)
	}
}
