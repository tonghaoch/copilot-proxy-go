package handler

import (
	"encoding/json"
	"strings"
)

// anthropicToResponsesResult converts a complete Anthropic message into the
// Responses API result shape that Codex reads.
func anthropicToResponsesResult(resp *AnthropicResponse, customTools map[string]bool) *ResponsesResult {
	result := &ResponsesResult{
		ID:     "resp_" + resp.ID,
		Object: "response",
		Model:  resp.Model,
		Status: "completed",
		Usage: &ResponsesUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}
	if resp.Usage.CacheReadInputTokens > 0 {
		result.Usage.InputTokensDetails = &InputTokensDetails{CachedTokens: resp.Usage.CacheReadInputTokens}
	}
	if resp.StopReason == "max_tokens" {
		result.Status = "incomplete"
		result.IncompleteDetails = &IncompleteDetail{Reason: "max_output_tokens"}
	}

	var text strings.Builder
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
			result.Output = append(result.Output, ResponsesOutput{
				Type:    "message",
				ID:      "msg_" + resp.ID,
				Status:  "completed",
				Role:    "assistant",
				Content: []OutputContent{{Type: "output_text", Text: block.Text}},
			})
		case "thinking":
			result.Output = append(result.Output, ResponsesOutput{
				Type:             "reasoning",
				ID:               "rs_" + resp.ID,
				Summary:          []SummaryItem{{Type: "summary_text", Text: block.Thinking}},
				EncryptedContent: block.Signature,
			})
		case "tool_use":
			result.Output = append(result.Output, toolCallItem(block, customTools))
		}
	}
	result.OutputText = text.String()
	return result
}

// toolCallItem builds a function_call item, or a custom_tool_call item when the
// tool was declared as freeform. Freeform tools carry their raw input as {"input": "..."}.
func toolCallItem(block ContentBlock, customTools map[string]bool) ResponsesOutput {
	if customTools[block.Name] {
		var args struct {
			Input string `json:"input"`
		}
		_ = json.Unmarshal(block.Input, &args)
		return ResponsesOutput{Type: "custom_tool_call", Status: "completed", CallID: block.ID, Name: block.Name, Arguments: args.Input}
	}
	return ResponsesOutput{Type: "function_call", Status: "completed", CallID: block.ID, Name: block.Name, Arguments: string(block.Input)}
}

// responsesStreamWriter turns an Anthropic SSE stream into Responses SSE events.
// It tracks one open output item at a time, which is how Anthropic streams its
// content blocks, and emits the item's done event when the block closes.
type responsesStreamWriter struct {
	emit        func(eventType string, data any) error
	respID      string
	model       string
	customTools map[string]bool

	seq        int
	outputIdx  int
	block      *ContentBlock
	itemID     string
	text       strings.Builder
	args       strings.Builder
	thinking   strings.Builder
	signature  string
	output     []ResponsesOutput
	usage      ResponsesUsage
	stopReason string
	started    bool
}

func newResponsesStreamWriter(emit func(string, any) error, model string, customTools map[string]bool) *responsesStreamWriter {
	return &responsesStreamWriter{emit: emit, model: model, customTools: customTools}
}

func (s *responsesStreamWriter) send(eventType string, fields map[string]any) error {
	s.seq++
	fields["type"] = eventType
	fields["sequence_number"] = s.seq
	return s.emit(eventType, fields)
}

func (s *responsesStreamWriter) responseObject(status string) map[string]any {
	return map[string]any{
		"id":     s.respID,
		"object": "response",
		"model":  s.model,
		"status": status,
		"output": s.output,
	}
}

// Handle processes one Anthropic SSE event (event type plus JSON data).
func (s *responsesStreamWriter) Handle(eventType, data string) error {
	switch eventType {
	case "message_start":
		var ev struct {
			Message AnthropicResponse `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}
		s.respID = "resp_" + ev.Message.ID
		s.usage.InputTokens = ev.Message.Usage.InputTokens
		if !s.started {
			s.started = true
			return s.send("response.created", map[string]any{"response": s.responseObject("in_progress")})
		}
	case "content_block_start":
		var ev ContentBlockStartEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}
		return s.openBlock(ev.ContentBlock)
	case "content_block_delta":
		var ev ContentBlockDeltaEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}
		return s.delta(ev.Delta)
	case "content_block_stop":
		return s.closeBlock()
	case "message_delta":
		var ev MessageDeltaEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}
		s.stopReason = ev.Delta.StopReason
		s.usage.OutputTokens = ev.Usage.OutputTokens
	case "message_stop":
		status := "completed"
		if s.stopReason == "max_tokens" {
			status = "incomplete"
		}
		resp := s.responseObject(status)
		resp["usage"] = s.usage
		return s.send("response.completed", map[string]any{"response": resp})
	case "error":
		var ev StreamErrorEvent
		_ = json.Unmarshal([]byte(data), &ev)
		return s.send("error", map[string]any{"code": ev.Error.Type, "message": ev.Error.Message})
	}
	return nil
}

func (s *responsesStreamWriter) openBlock(block ContentBlock) error {
	s.block = &block
	s.outputIdx = len(s.output)
	s.text.Reset()
	s.args.Reset()
	s.thinking.Reset()
	s.signature = ""
	switch block.Type {
	case "text":
		s.itemID = "msg_" + s.respID
		return s.send("response.output_item.added", map[string]any{
			"output_index": s.outputIdx,
			"item":         map[string]any{"type": "message", "id": s.itemID, "status": "in_progress", "role": "assistant", "content": []any{}},
		})
	case "thinking":
		s.itemID = "rs_" + s.respID
		return s.send("response.output_item.added", map[string]any{
			"output_index": s.outputIdx,
			"item":         map[string]any{"type": "reasoning", "id": s.itemID, "summary": []any{}},
		})
	case "tool_use":
		s.itemID = block.ID
		typ := "function_call"
		if s.customTools[block.Name] {
			typ = "custom_tool_call"
		}
		return s.send("response.output_item.added", map[string]any{
			"output_index": s.outputIdx,
			"item":         map[string]any{"type": typ, "id": block.ID, "call_id": block.ID, "name": block.Name, "status": "in_progress"},
		})
	}
	return nil
}

func (s *responsesStreamWriter) delta(d Delta) error {
	if s.block == nil {
		return nil
	}
	switch d.Type {
	case "text_delta":
		s.text.WriteString(d.Text)
		return s.send("response.output_text.delta", map[string]any{"item_id": s.itemID, "output_index": s.outputIdx, "content_index": 0, "delta": d.Text})
	case "thinking_delta":
		s.thinking.WriteString(d.Thinking)
		return s.send("response.reasoning_summary_text.delta", map[string]any{"item_id": s.itemID, "output_index": s.outputIdx, "summary_index": 0, "delta": d.Thinking})
	case "signature_delta":
		s.signature = d.Signature
	case "input_json_delta":
		s.args.WriteString(d.PartialJSON)
		return s.send("response.function_call_arguments.delta", map[string]any{"item_id": s.itemID, "output_index": s.outputIdx, "delta": d.PartialJSON})
	}
	return nil
}

// closeBlock finalizes the open block into an output item and emits its done event.
func (s *responsesStreamWriter) closeBlock() error {
	if s.block == nil {
		return nil
	}
	block := *s.block
	s.block = nil
	var item ResponsesOutput
	switch block.Type {
	case "text":
		item = ResponsesOutput{Type: "message", ID: s.itemID, Status: "completed", Role: "assistant",
			Content: []OutputContent{{Type: "output_text", Text: s.text.String()}}}
	case "thinking":
		item = ResponsesOutput{Type: "reasoning", ID: s.itemID, EncryptedContent: s.signature,
			Summary: []SummaryItem{{Type: "summary_text", Text: s.thinking.String()}}}
	case "tool_use":
		block.Input = json.RawMessage(s.args.String())
		if s.args.Len() == 0 {
			block.Input = json.RawMessage("{}")
		}
		item = toolCallItem(block, s.customTools)
		item.ID = block.ID
		if item.Type == "custom_tool_call" {
			item.Arguments = ""
			var args struct {
				Input string `json:"input"`
			}
			_ = json.Unmarshal(block.Input, &args)
			item.Arguments = args.Input
		}
	default:
		return nil
	}
	s.output = append(s.output, item)
	return s.send("response.output_item.done", map[string]any{"output_index": s.outputIdx, "item": item})
}
