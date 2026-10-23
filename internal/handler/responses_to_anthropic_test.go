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

	result := anthropicToResponsesResult(&AnthropicResponse{
		ID:         "msg_x",
		Model:      "claude-sonnet-5.5",
		StopReason: "tool_use",
		Content: []ContentBlock{
			{Type: "tool_use", ID: "p2", Name: "apply_patch", Input: json.RawMessage(`{"input":"*** end"}`)},
		},
	}, custom)
	if result.Output[0].Type != "custom_tool_call" || result.Output[0].Arguments != "*** end" {
		t.Fatalf("freeform output = %+v", result.Output[0])
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
	result := anthropicToResponsesResult(resp, nil)
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
