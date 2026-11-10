package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesToAnthropicRoundTripsToolHistory(t *testing.T) {
	payload := map[string]any{
		"instructions":      "be brief",
		"max_output_tokens": float64(512),
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "list files"},
			}},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "shell", "arguments": `{"cmd":"ls"}`},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "a.go\nb.go"},
		},
		"tools": []any{
			map[string]any{"type": "function", "name": "shell", "description": "run", "parameters": map[string]any{"type": "object"}},
		},
	}

	req, custom, err := responsesToAnthropic(payload, "claude-opus-5.5")
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "claude-opus-5.5" || req.MaxTokens != 512 {
		t.Fatalf("model/max_tokens = %q/%d", req.Model, req.MaxTokens)
	}
	if len(custom) != 0 {
		t.Fatalf("no custom tools expected, got %v", custom)
	}
	if string(req.System) != `"be brief"` {
		t.Fatalf("system = %s", req.System)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("want user, assistant tool_use, user tool_result; got %d messages", len(req.Messages))
	}
	if req.Messages[1].Role != "assistant" || !strings.Contains(string(req.Messages[1].Content), `"tool_use"`) {
		t.Fatalf("assistant turn = %s", req.Messages[1].Content)
	}
	if !strings.Contains(string(req.Messages[2].Content), `"tool_result"`) || !strings.Contains(string(req.Messages[2].Content), `call_1`) {
		t.Fatalf("tool result turn = %s", req.Messages[2].Content)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "shell" {
		t.Fatalf("tools = %+v", req.Tools)
	}
}

func TestResponsesToAnthropicMergesSameRoleItems(t *testing.T) {
	payload := map[string]any{"input": []any{
		map[string]any{"type": "message", "role": "user", "content": "one"},
		map[string]any{"type": "message", "role": "user", "content": "two"},
	}}
	req, _, err := responsesToAnthropic(payload, "claude-haiku-5.5")
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("consecutive user items should merge, got %d turns", len(req.Messages))
	}
}

func TestResponsesToAnthropicFreeformToolRoundTrip(t *testing.T) {
	payload := map[string]any{
		"tools": []any{map[string]any{"type": "custom", "name": "apply_patch", "description": "patch"}},
		"input": []any{
			map[string]any{"type": "custom_tool_call", "call_id": "p1", "name": "apply_patch", "input": "*** patch"},
		},
	}
	req, custom, err := responsesToAnthropic(payload, "claude-sonnet-5.5")
	if err != nil {
		t.Fatal(err)
	}
	if !custom["apply_patch"] {
		t.Fatalf("apply_patch should be a custom tool, got %v", custom)
	}
	if !strings.Contains(string(req.Messages[0].Content), `"input":"*** patch"`) {
		t.Fatalf("freeform input not wrapped: %s", req.Messages[0].Content)
	}

	result, err := anthropicToResponsesResult(&AnthropicResponse{
		ID:         "msg_x",
		Model:      "claude-sonnet-5.5",
		StopReason: "tool_use",
		Content: []ContentBlock{
			{Type: "tool_use", ID: "p2", Name: "apply_patch", Input: json.RawMessage(`{"input":"*** end"}`)},
		},
	}, custom)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(result.Output[0])
	if err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	if err := json.Unmarshal(wire, &item); err != nil {
		t.Fatal(err)
	}
	if item["type"] != "custom_tool_call" || item["input"] != "*** end" || item["arguments"] != nil {
		t.Fatalf("freeform output = %s", wire)
	}
}

func TestAnthropicToResponsesResultMapsBlocks(t *testing.T) {
	resp := &AnthropicResponse{
		ID:         "msg_1",
		Model:      "claude-opus-5.5",
		StopReason: "end_turn",
		Usage:      AnthropicUsage{InputTokens: 10, OutputTokens: 4},
		Content: []ContentBlock{
			{Type: "thinking", Thinking: "hmm", Signature: "sig"},
			{Type: "text", Text: "hello"},
		},
	}
	result, err := anthropicToResponsesResult(resp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.OutputText != "hello" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Output) != 2 || result.Output[0].Type != "reasoning" || result.Output[0].EncryptedContent != "sig" {
		t.Fatalf("output = %+v", result.Output)
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

func TestResponsesStreamWriterEmitsCompletedWithToolCall(t *testing.T) {
	var events []string
	emit := func(eventType string, data any) error {
		events = append(events, eventType)
		return nil
	}
	stream := newResponsesStreamWriter(emit, "claude-opus-5.5", nil)
	stream.respID = "resp_test"
	sse := strings.Join([]string{
		"event: message_start", `data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":3}}}`, "",
		"event: content_block_start", `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"shell"}}`, "",
		"event: content_block_delta", `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}`, "",
		"event: content_block_delta", `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`, "",
		"event: content_block_stop", `data: {"type":"content_block_stop","index":0}`, "",
		"event: message_delta", `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`, "",
		"event: message_stop", `data: {"type":"message_stop"}`, "",
	}, "\n") + "\n"

	if err := readSSE(strings.NewReader(sse), stream.Handle); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"response.created",
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.delta",
		"response.output_item.done",
		"response.completed",
	}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if len(stream.output) != 1 || stream.output[0].Type != "function_call" || stream.output[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("output = %+v", stream.output)
	}
}

func TestResponsesStreamWriterStreamsCustomToolInput(t *testing.T) {
	var deltas strings.Builder
	var added, done, inputDone map[string]any
	emit := func(eventType string, data any) error {
		wire, err := json.Marshal(data)
		if err != nil {
			return err
		}
		var event map[string]any
		if err := json.Unmarshal(wire, &event); err != nil {
			return err
		}
		switch eventType {
		case "response.output_item.added":
			added = event["item"].(map[string]any)
		case "response.custom_tool_call_input.delta":
			deltas.WriteString(event["delta"].(string))
		case "response.custom_tool_call_input.done":
			inputDone = event
		case "response.function_call_arguments.delta":
			t.Error("custom tool emitted a function-call delta")
		case "response.output_item.done":
			done = event["item"].(map[string]any)
		}
		return nil
	}
	stream := newResponsesStreamWriter(emit, "claude-test", map[string]bool{"apply_patch": true})
	if err := stream.Handle("message_start", `{"message":{"id":"m","usage":{"input_tokens":1}}}`); err != nil {
		t.Fatal(err)
	}
	if err := stream.Handle("content_block_start", `{"content_block":{"type":"tool_use","id":"p","name":"apply_patch"}}`); err != nil {
		t.Fatal(err)
	}
	parts := []string{`{"other":[1,2],"input":"first `, `lin`, `e\n`, `\uD83`, `D`, `\uDE`, `03`, ` and \"`, `quoted\"`, `"}`}
	for i, part := range parts {
		wire, err := json.Marshal(ContentBlockDeltaEvent{Delta: Delta{Type: "input_json_delta", PartialJSON: part}})
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Handle("content_block_delta", string(wire)); err != nil {
			t.Fatal(err)
		}
		if i == 2 && deltas.String() != "first line\n" {
			t.Fatalf("input was not streamed before the tool finished: %q", deltas.String())
		}
		if i == 4 && deltas.String() != "first line\n" {
			t.Fatalf("partial surrogate was emitted prematurely: %q", deltas.String())
		}
		if i == 6 && deltas.String() != "first line\n😃" {
			t.Fatalf("split surrogate pair was not decoded: %q", deltas.String())
		}
	}
	if err := stream.Handle("content_block_stop", `{}`); err != nil {
		t.Fatal(err)
	}
	want := "first line\n😃 and \"quoted\""
	if got := deltas.String(); got != want {
		t.Fatalf("custom input deltas = %q, want %q", got, want)
	}
	if added["input"] != "" || inputDone["input"] != want || done["input"] != want || done["arguments"] != nil {
		t.Fatalf("custom tool events: added=%v input done=%v item done=%v", added, inputDone, done)
	}
}

func TestCustomToolInputHandlesJSONFragmentBoundaries(t *testing.T) {
	inputs := []string{
		`{"input":""}`,
		`{"input":"tab\tnewline\nquote\"slash\\solidus\/"}`,
		`{"other":{"input":"ignored"},"input":"\u0061\uD83D\uDE03"}`,
		`{"\u0069nput":"\uD800x\uD800\uD83D\uDE03"}`,
	}
	for _, raw := range inputs {
		var expected struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal([]byte(raw), &expected); err != nil {
			t.Fatal(err)
		}
		for split := 0; split <= len(raw); split++ {
			var received strings.Builder
			var doneInput string
			stream := newResponsesStreamWriter(func(eventType string, data any) error {
				fields := data.(map[string]any)
				switch eventType {
				case "response.custom_tool_call_input.delta":
					received.WriteString(fields["delta"].(string))
				case "response.custom_tool_call_input.done":
					doneInput = fields["input"].(string)
				}
				return nil
			}, "claude-test", map[string]bool{"patch": true})
			if err := stream.openBlock(ContentBlock{Type: "tool_use", ID: "p", Name: "patch"}); err != nil {
				t.Fatal(err)
			}
			for _, fragment := range []string{raw[:split], raw[split:]} {
				if err := stream.delta(Delta{Type: "input_json_delta", PartialJSON: fragment}); err != nil {
					t.Fatalf("%s split at %d: %v", raw, split, err)
				}
			}
			if err := stream.closeBlock(); err != nil {
				t.Fatalf("%s split at %d: %v", raw, split, err)
			}
			if received.String() != expected.Input || doneInput != expected.Input {
				t.Fatalf("%s split at %d: deltas=%q done=%q, want %q", raw, split, received.String(), doneInput, expected.Input)
			}
		}
	}
}

func TestCustomToolRejectsMissingOrInvalidInput(t *testing.T) {
	for _, raw := range []string{`{}`, `{"input":null}`, `{"input":42}`, `{"input":"unfinished`} {
		_, err := anthropicToResponsesResult(&AnthropicResponse{Content: []ContentBlock{{
			Type: "tool_use", Name: "patch", Input: json.RawMessage(raw),
		}}}, map[string]bool{"patch": true})
		if err == nil {
			t.Fatalf("invalid custom tool input accepted: %s", raw)
		}
	}
}

func TestResponsesStreamWriterReportsIncompleteResponse(t *testing.T) {
	var eventType string
	var event map[string]any
	stream := newResponsesStreamWriter(func(typ string, data any) error {
		eventType = typ
		wire, err := json.Marshal(data)
		if err != nil {
			return err
		}
		return json.Unmarshal(wire, &event)
	}, "claude-test", nil)
	if err := stream.Handle("message_start", `{"message":{"id":"m","usage":{"input_tokens":1}}}`); err != nil {
		t.Fatal(err)
	}
	if err := stream.Handle("message_delta", `{"delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":2}}`); err != nil {
		t.Fatal(err)
	}
	if err := stream.Handle("message_stop", `{}`); err != nil {
		t.Fatal(err)
	}
	response := event["response"].(map[string]any)
	details, _ := response["incomplete_details"].(map[string]any)
	if eventType != "response.incomplete" || response["status"] != "incomplete" || details["reason"] != "max_output_tokens" {
		t.Fatalf("terminal event = %s, response = %v", eventType, response)
	}
}

func TestResponsesStreamWriterReasoningItemHasStatus(t *testing.T) {
	var added, done []string
	emit := func(eventType string, data any) error {
		m, _ := data.(map[string]any)
		switch eventType {
		case "response.output_item.added":
			item, _ := m["item"].(map[string]any)
			added = append(added, rsStr(item["status"]))
		case "response.output_item.done":
			item, _ := m["item"].(ResponsesOutput)
			done = append(done, item.Status)
		}
		return nil
	}
	stream := newResponsesStreamWriter(emit, "gemini-3.8-flash", nil)
	stream.respID = "resp_test"
	sse := strings.Join([]string{
		"event: message_start", `data: {"type":"message_start","message":{"id":"m1","usage":{"input_tokens":1}}}`, "",
		"event: content_block_start", `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`, "",
		"event: content_block_delta", `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`, "",
		"event: content_block_delta", `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`, "",
		"event: content_block_stop", `data: {"type":"content_block_stop","index":0}`, "",
		"event: message_stop", `data: {"type":"message_stop"}`, "",
	}, "\n") + "\n"

	if err := readSSE(strings.NewReader(sse), stream.Handle); err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "in_progress" {
		t.Fatalf("reasoning added status = %v, want in_progress", added)
	}
	if len(done) != 1 || done[0] != "completed" {
		t.Fatalf("reasoning done status = %v, want completed", done)
	}
}

// Codex sends a history ending in the assistant's last reply. Anthropic
// models reject a trailing assistant turn, so the conversation must end with a user turn.
func TestResponsesToAnthropicEndsWithUserTurn(t *testing.T) {
	endings := map[string][]any{
		"assistant text": {
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "PONG"}}},
		},
		"signed reasoning": {
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": "sig"},
		},
		"tool output": {
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			map[string]any{"type": "function_call", "call_id": "c", "name": "shell", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c", "output": "ok"},
		},
	}
	for name, input := range endings {
		req, _, err := responsesToAnthropic(map[string]any{"input": input}, "claude-haiku-5.5")
		if err != nil {
			t.Fatal(err)
		}
		if last := req.Messages[len(req.Messages)-1].Role; last != "user" {
			t.Fatalf("%s: conversation ends with %q, want user", name, last)
		}
	}
}

// Codex parses response.completed strictly and fails without usage.total_tokens.
func TestResponsesUsageIncludesTotalTokens(t *testing.T) {
	result, err := anthropicToResponsesResult(&AnthropicResponse{
		ID: "m", Model: "claude-haiku-5.5",
		Usage: AnthropicUsage{InputTokens: 10, OutputTokens: 4},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.TotalTokens != 14 {
		t.Fatalf("result total_tokens = %d, want 14", result.Usage.TotalTokens)
	}

	var completed map[string]any
	emit := func(eventType string, data any) error {
		if eventType == "response.completed" {
			completed = data.(map[string]any)
		}
		return nil
	}
	stream := newResponsesStreamWriter(emit, "claude-haiku-5.5", nil)
	sse := strings.Join([]string{
		"event: message_start", `data: {"type":"message_start","message":{"id":"m","usage":{"input_tokens":10}}}`, "",
		"event: message_delta", `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`, "",
		"event: message_stop", `data: {"type":"message_stop"}`, "",
	}, "\n") + "\n"
	if err := readSSE(strings.NewReader(sse), stream.Handle); err != nil {
		t.Fatal(err)
	}
	usage := completed["response"].(map[string]any)["usage"].(ResponsesUsage)
	if usage.TotalTokens != 14 {
		t.Fatalf("stream total_tokens = %d, want 14", usage.TotalTokens)
	}
}

func TestAddCacheBreakpoints(t *testing.T) {
	body := []byte(`{"system":"sys","tools":[{"name":"a"},{"name":"b"}],"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"text","text":"y"}]}]}`)
	var got map[string]any
	if err := json.Unmarshal(addCacheBreakpoints(body), &got); err != nil {
		t.Fatal(err)
	}
	sys := got["system"].([]any)[0].(map[string]any)
	tools := got["tools"].([]any)
	blocks := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if sys["cache_control"] == nil || tools[1].(map[string]any)["cache_control"] == nil || blocks[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("missing breakpoint: %s", addCacheBreakpoints(body))
	}
	if tools[0].(map[string]any)["cache_control"] != nil || blocks[0].(map[string]any)["cache_control"] != nil {
		t.Fatal("only the last tool and last block should be marked")
	}
}
