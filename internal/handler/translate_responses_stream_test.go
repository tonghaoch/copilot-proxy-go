package handler

import "testing"

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
