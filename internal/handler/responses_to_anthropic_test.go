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
	if custom["shell"].custom {
		t.Fatalf("shell should not be a custom tool: %v", custom)
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
	if !custom["apply_patch"].custom {
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

func TestResponsesReasoningEffortSurvivesBackendTranslation(t *testing.T) {
	for _, effort := range []string{"none", "low", "high"} {
		t.Run(effort, func(t *testing.T) {
			req, _, err := responsesToAnthropic(map[string]any{
				"input": "hello", "reasoning": map[string]any{"effort": effort},
			}, "claude-test")
			if err != nil {
				t.Fatal(err)
			}
			chat, err := translateToOpenAI(req, "")
			if err != nil {
				t.Fatal(err)
			}
			if chat.ReasoningEffort != effort {
				t.Fatalf("chat effort = %q, want %q", chat.ReasoningEffort, effort)
			}
			if effort == "none" {
				raw, err := json.Marshal(req)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				applyAdaptiveThinkingInMap(payload, req, stubModels{adaptiveThinkingModel()}, stubConfig{"high"})
				thinking := payload["thinking"].(map[string]any)
				if thinking["type"] != "disabled" || thinking["budget_tokens"] != nil || payload["output_config"] != nil {
					t.Fatalf("disabled thinking was not preserved: %s", raw)
				}
			} else if req.OutputConfig == nil || req.OutputConfig.Effort != effort {
				t.Fatalf("native effort = %+v", req.OutputConfig)
			}
		})
	}
}

func TestResponsesNamespacedAndAdditionalToolsRoundTrip(t *testing.T) {
	payload := map[string]any{
		"tools": []any{
			map[string]any{"type": "local_shell"},
			map[string]any{"type": "function", "name": "exec", "parameters": map[string]any{"type": "object"}},
		},
		"tool_choice": map[string]any{"type": "custom", "namespace": "functions", "name": "patch"},
		"input": []any{
			map[string]any{"type": "additional_tools", "tools": []any{
				map[string]any{"type": "namespace", "name": "functions", "tools": []any{
					map[string]any{"type": "function", "name": "exec", "parameters": map[string]any{"type": "object"}},
					map[string]any{"type": "custom", "name": "patch"},
				}},
			}},
			map[string]any{"type": "function_call", "namespace": "functions", "name": "exec", "call_id": "c1", "arguments": `{}`},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "done"},
		},
	}
	req, tools, err := responsesToAnthropic(payload, "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	execAlias := rsToolName("functions", "exec")
	patchAlias := rsToolName("functions", "patch")
	if len(req.Tools) != 4 || req.Tools[0].Name != "local_shell" || req.Tools[1].Name != "exec" ||
		req.Tools[2].Name != execAlias || req.Tools[3].Name != patchAlias {
		t.Fatalf("translated tools = %+v", req.Tools)
	}
	var choice map[string]any
	if err := json.Unmarshal(req.ToolChoice, &choice); err != nil {
		t.Fatal(err)
	}
	if choice["type"] != "tool" || choice["name"] != patchAlias {
		t.Fatalf("translated tool choice = %v", choice)
	}
	call := ParseMessageContent(req.Messages[0].Content)[0]
	result := ParseMessageContent(req.Messages[1].Content)[0]
	if call.Name != execAlias || call.ID != "c1" || result.ToolUseID != "c1" {
		t.Fatalf("translated history: call=%+v result=%+v", call, result)
	}
	translated, err := anthropicToResponsesResult(&AnthropicResponse{Content: []ContentBlock{
		{Type: "tool_use", ID: "f", Name: execAlias, Input: json.RawMessage(`{"x":1}`)},
		{Type: "tool_use", ID: "p", Name: patchAlias, Input: json.RawMessage(`{"input":"patch body"}`)},
	}}, tools)
	if err != nil {
		t.Fatal(err)
	}
	if got := translated.Output; len(got) != 2 || got[0].Name != "exec" || got[0].Namespace != "functions" ||
		got[1].Name != "patch" || got[1].Namespace != "functions" || got[1].Input == nil || *got[1].Input != "patch body" {
		t.Fatalf("translated output = %+v", got)
	}
	var added map[string]any
	stream := newResponsesStreamWriter(func(event string, data any) error {
		if event == "response.output_item.added" {
			added = data.(map[string]any)["item"].(map[string]any)
		}
		return nil
	}, "claude-test", tools)
	if err := stream.openBlock(ContentBlock{Type: "tool_use", ID: "p", Name: patchAlias}); err != nil {
		t.Fatal(err)
	}
	if err := stream.delta(Delta{Type: "input_json_delta", PartialJSON: `{"input":"patch body"}`}); err != nil {
		t.Fatal(err)
	}
	if err := stream.closeBlock(); err != nil {
		t.Fatal(err)
	}
	if added["name"] != "patch" || added["namespace"] != "functions" || stream.output[0].Namespace != "functions" {
		t.Fatalf("streamed namespaced call = %v / %+v", added, stream.output)
	}
}

func TestResponsesToolOutputImagesSurviveTranslation(t *testing.T) {
	payload := map[string]any{"input": []any{
		map[string]any{"type": "message", "role": "user", "content": "inspect"},
		map[string]any{"type": "function_call", "name": "inspect", "call_id": "c1", "arguments": `{}`},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": []any{
			map[string]any{"type": "input_text", "text": "screenshot"},
			map[string]any{"type": "input_image", "image_url": "https://example.test/image.png"},
		}},
	}}
	req, _, err := responsesToAnthropic(payload, "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	if !detectVisionInResponses(payload) || !hasVision(req.Messages) {
		t.Fatal("image in tool result was not detected")
	}
	toolResult := ParseMessageContent(req.Messages[2].Content)[0]
	blocks := ParseMessageContent(toolResult.Content)
	if len(blocks) != 2 || blocks[0].Text != "screenshot" || blocks[1].Source == nil || blocks[1].Source.URL != "https://example.test/image.png" {
		t.Fatalf("tool result blocks = %+v", blocks)
	}
	chat, err := translateToOpenAI(req, "")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(chat.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"role":"tool","content":"screenshot"`) || !strings.Contains(string(wire), `"url":"https://example.test/image.png"`) {
		t.Fatalf("chat lost tool text or image: %s", wire)
	}
}

func TestResponsesStreamPreservesFragmentedSignature(t *testing.T) {
	stream := newResponsesStreamWriter(func(string, any) error { return nil }, "claude-test", nil)
	if err := stream.openBlock(ContentBlock{Type: "thinking", Signature: "first"}); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"-", "second", "-third"} {
		if err := stream.delta(Delta{Type: "signature_delta", Signature: fragment}); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.closeBlock(); err != nil {
		t.Fatal(err)
	}
	if got := stream.output[0].EncryptedContent; got != "first-second-third" {
		t.Fatalf("signature = %q", got)
	}
}

func TestResponsesStreamTruncatedCustomInput(t *testing.T) {
	for _, reason := range []string{"max_tokens", "tool_use"} {
		t.Run(reason, func(t *testing.T) {
			var lastEvent string
			stream := newResponsesStreamWriter(func(event string, _ any) error {
				lastEvent = event
				return nil
			}, "claude-test", responseTools{"patch": {name: "patch", custom: true}})
			if err := stream.Handle("message_start", `{"message":{"id":"m"}}`); err != nil {
				t.Fatal(err)
			}
			if err := stream.Handle("content_block_start", `{"content_block":{"type":"tool_use","id":"p","name":"patch"}}`); err != nil {
				t.Fatal(err)
			}
			delta, _ := json.Marshal(ContentBlockDeltaEvent{Delta: Delta{Type: "input_json_delta", PartialJSON: `{"input":"partial`}})
			if err := stream.Handle("content_block_delta", string(delta)); err != nil {
				t.Fatal(err)
			}
			if err := stream.Handle("content_block_stop", `{}`); err != nil {
				t.Fatal(err)
			}
			if err := stream.Handle("message_delta", `{"delta":{"stop_reason":"`+reason+`"},"usage":{}}`); err != nil {
				t.Fatal(err)
			}
			err := stream.Handle("message_stop", `{}`)
			if reason == "tool_use" {
				if err == nil {
					t.Fatal("malformed completed tool call was accepted")
				}
				return
			}
			if err != nil || lastEvent != "response.incomplete" || len(stream.output) != 1 ||
				stream.output[0].Input == nil || *stream.output[0].Input != "partial" || stream.output[0].Status != "incomplete" {
				t.Fatalf("truncated output = %+v, event=%s, error=%v", stream.output, lastEvent, err)
			}
		})
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
		"response.function_call_arguments.done",
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
	stream := newResponsesStreamWriter(emit, "claude-test", responseTools{"apply_patch": {name: "apply_patch", custom: true}})
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
			}, "claude-test", responseTools{"patch": {name: "patch", custom: true}})
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
		}}}, responseTools{"patch": {name: "patch", custom: true}})
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

func TestResponsesStreamInitializesSDKOutputItems(t *testing.T) {
	items := map[int]map[string]any{}
	ids := map[string]bool{}
	emit := func(eventType string, data any) error {
		wire, err := json.Marshal(data)
		if err != nil {
			return err
		}
		var event map[string]any
		if err := json.Unmarshal(wire, &event); err != nil {
			return err
		}
		index, _ := event["output_index"].(float64)
		item := items[int(index)]
		switch eventType {
		case "response.output_item.added":
			item = event["item"].(map[string]any)
			id := item["id"].(string)
			if ids[id] {
				t.Fatalf("duplicate output item ID %q", id)
			}
			ids[id] = true
			items[int(index)] = item
			if item["type"] == "function_call" && item["arguments"] != "" {
				t.Fatalf("function arguments not initialized: %v", item)
			}
		case "response.content_part.added":
			parts := item["content"].([]any)
			item["content"] = append(parts, event["part"])
		case "response.output_text.delta":
			parts := item["content"].([]any)
			if len(parts) != 1 || event["logprobs"] == nil {
				t.Fatalf("text delta arrived before initialized content or logprobs: item=%v event=%v", item, event)
			}
			part := parts[0].(map[string]any)
			part["text"] = part["text"].(string) + event["delta"].(string)
		case "response.function_call_arguments.delta":
			args, ok := item["arguments"].(string)
			if !ok {
				t.Fatalf("function delta arrived before arguments: %v", item)
			}
			item["arguments"] = args + event["delta"].(string)
		}
		return nil
	}
	stream := newResponsesStreamWriter(emit, "claude-test", nil)
	events := []struct{ typ, data string }{
		{"message_start", `{"message":{"id":"m","usage":{"input_tokens":1}}}`},
		{"content_block_start", `{"index":0,"content_block":{"type":"text"}}`},
		{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"hello"}}`},
		{"content_block_stop", `{"index":0}`},
		{"content_block_start", `{"index":1,"content_block":{"type":"thinking"}}`},
		{"content_block_delta", `{"index":1,"delta":{"type":"thinking_delta","thinking":"one"}}`},
		{"content_block_stop", `{"index":1}`},
		{"content_block_start", `{"index":2,"content_block":{"type":"text"}}`},
		{"content_block_delta", `{"index":2,"delta":{"type":"text_delta","text":"again"}}`},
		{"content_block_stop", `{"index":2}`},
		{"content_block_start", `{"index":3,"content_block":{"type":"thinking"}}`},
		{"content_block_delta", `{"index":3,"delta":{"type":"thinking_delta","thinking":"two"}}`},
		{"content_block_stop", `{"index":3}`},
		{"content_block_start", `{"index":4,"content_block":{"type":"tool_use","id":"t","name":"shell"}}`},
		{"content_block_delta", `{"index":4,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}`},
		{"content_block_stop", `{"index":4}`},
		{"message_stop", `{}`},
	}
	for _, event := range events {
		if err := stream.Handle(event.typ, event.data); err != nil {
			t.Fatalf("%s: %v", event.typ, err)
		}
	}
	if got := items[0]["content"].([]any)[0].(map[string]any)["text"]; got != "hello" {
		t.Fatalf("accumulated text = %v", got)
	}
	if got := items[4]["arguments"]; got != `{"x":1}` {
		t.Fatalf("accumulated arguments = %v", got)
	}
}

func TestAnthropicToResponsesResultUsesDistinctItemIDs(t *testing.T) {
	result, err := anthropicToResponsesResult(&AnthropicResponse{ID: "m", Content: []ContentBlock{
		{Type: "text", Text: "one"}, {Type: "thinking", Thinking: "a"},
		{Type: "text", Text: "two"}, {Type: "thinking", Thinking: "b"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range result.Output {
		if item.ID == "" || seen[item.ID] {
			t.Fatalf("missing or duplicate output item ID: %+v", result.Output)
		}
		seen[item.ID] = true
	}
}
