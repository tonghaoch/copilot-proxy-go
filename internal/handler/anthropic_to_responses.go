package handler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// anthropicToResponsesResult converts a complete Anthropic message into the
// Responses API result shape that Codex reads.
func anthropicToResponsesResult(resp *AnthropicResponse, customTools responseTools) (*ResponsesResult, error) {
	result := &ResponsesResult{
		ID:     "resp_" + resp.ID,
		Object: "response",
		Model:  resp.Model,
		Status: "completed",
		Usage:  anthropicUsageToResponses(resp.Usage),
		Output: []ResponsesOutput{},
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
				ID:      fmt.Sprintf("msg_%s_%d", resp.ID, len(result.Output)),
				Status:  "completed",
				Role:    "assistant",
				Content: []OutputContent{{Type: "output_text", Text: block.Text, Annotations: []any{}}},
			})
		case "thinking":
			result.Output = append(result.Output, ResponsesOutput{
				Type:             "reasoning",
				ID:               fmt.Sprintf("rs_%s_%d", resp.ID, len(result.Output)),
				Status:           "completed",
				Summary:          []SummaryItem{{Type: "summary_text", Text: block.Thinking}},
				EncryptedContent: block.Signature,
			})
		case "tool_use":
			item, err := toolCallItem(block, customTools)
			if err != nil {
				return nil, err
			}
			result.Output = append(result.Output, item)
		}
	}
	result.OutputText = text.String()
	return result, nil
}

// toolCallItem builds a function_call item, or a custom_tool_call item when the
// tool was declared as freeform. Freeform tools carry their raw input as {"input": "..."}.
func toolCallItem(block ContentBlock, customTools responseTools) (ResponsesOutput, error) {
	tool := customTools.lookup(block.Name)
	if tool.custom {
		var args struct {
			Input *string `json:"input"`
		}
		if err := json.Unmarshal(block.Input, &args); err != nil {
			return ResponsesOutput{}, fmt.Errorf("invalid custom tool input for %q: %w", block.Name, err)
		}
		if args.Input == nil {
			return ResponsesOutput{}, fmt.Errorf("custom tool %q did not supply an input string", block.Name)
		}
		return ResponsesOutput{Type: "custom_tool_call", ID: block.ID, Status: "completed", CallID: block.ID, Name: tool.name, Namespace: tool.namespace, Input: args.Input}, nil
	}
	return ResponsesOutput{Type: "function_call", ID: block.ID, Status: "completed", CallID: block.ID, Name: tool.name, Namespace: tool.namespace, Arguments: string(block.Input)}, nil
}

// responsesStreamWriter turns an Anthropic SSE stream into Responses SSE events.
// It tracks one open output item at a time, which is how Anthropic streams its
// content blocks, and emits the item's done event when the block closes.
type responsesStreamWriter struct {
	emit        func(eventType string, data any) error
	respID      string
	model       string
	customTools responseTools

	seq           int
	outputIdx     int
	blockIndex    int
	block         *ContentBlock
	itemID        string
	text          strings.Builder
	args          strings.Builder
	customText    strings.Builder
	inputPos      int
	inputEnd      bool
	thinking      strings.Builder
	signature     string
	output        []ResponsesOutput
	usage         AnthropicUsage
	stopReason    string
	invalidCustom error
	started       bool
	finished      bool
}

func newResponsesStreamWriter(emit func(string, any) error, model string, customTools responseTools) *responsesStreamWriter {
	return &responsesStreamWriter{emit: emit, model: model, customTools: customTools}
}

func (s *responsesStreamWriter) send(eventType string, fields map[string]any) error {
	s.seq++
	fields["type"] = eventType
	fields["sequence_number"] = s.seq
	return s.emit(eventType, fields)
}

func (s *responsesStreamWriter) responseObject(status string) map[string]any {
	output := s.output
	if output == nil {
		output = []ResponsesOutput{}
	}
	return map[string]any{
		"id":     s.respID,
		"object": "response",
		"model":  s.model,
		"status": status,
		"output": output,
	}
}

// anthropicUsageToResponses converts Anthropic usage, whose input_tokens
// excludes cache reads and writes, to Responses usage, whose input_tokens
// includes them with the cache reads broken out as cached_tokens.
func anthropicUsageToResponses(u AnthropicUsage) *ResponsesUsage {
	input := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	usage := &ResponsesUsage{InputTokens: input, OutputTokens: u.OutputTokens, TotalTokens: input + u.OutputTokens}
	if u.CacheReadInputTokens > 0 {
		usage.InputTokensDetails = &InputTokensDetails{CachedTokens: u.CacheReadInputTokens}
	}
	return usage
}

// Handle processes one Anthropic SSE event (event type plus JSON data).
func (s *responsesStreamWriter) Handle(eventType, data string) error {
	if s.finished {
		return nil
	}
	switch eventType {
	case "message_start":
		var ev struct {
			Message AnthropicResponse `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}
		s.respID = "resp_" + ev.Message.ID
		usage := &ev.Message.Usage
		s.mergeUsage(&usage.InputTokens, &usage.CacheReadInputTokens, &usage.CacheCreationInputTokens)
		if !s.started {
			s.started = true
			return s.send("response.created", map[string]any{"response": s.responseObject("in_progress")})
		}
	case "content_block_start":
		if s.invalidCustom != nil {
			return s.invalidCustom
		}
		var ev ContentBlockStartEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}
		if s.block != nil {
			return fmt.Errorf("content block %d started before block %d stopped", ev.Index, s.blockIndex)
		}
		s.blockIndex = ev.Index
		return s.openBlock(ev.ContentBlock)
	case "content_block_delta":
		var ev ContentBlockDeltaEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}
		if s.block == nil || ev.Index != s.blockIndex {
			return fmt.Errorf("delta for unopened content block %d", ev.Index)
		}
		return s.delta(ev.Delta)
	case "content_block_stop":
		var ev ContentBlockStopEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}
		if s.block == nil || ev.Index != s.blockIndex {
			return fmt.Errorf("stop for unopened content block %d", ev.Index)
		}
		return s.closeBlock()
	case "message_delta":
		var ev MessageDeltaEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}
		s.stopReason = ev.Delta.StopReason
		s.usage.OutputTokens = ev.Usage.OutputTokens
		s.mergeUsage(ev.Usage.InputTokens, ev.Usage.CacheReadInputTokens, ev.Usage.CacheCreationInputTokens)
	case "message_stop":
		if s.block != nil {
			return fmt.Errorf("message stopped before content block %d", s.blockIndex)
		}
		if s.invalidCustom != nil && s.stopReason != "max_tokens" {
			return s.invalidCustom
		}
		status := "completed"
		event := "response.completed"
		if s.stopReason == "max_tokens" {
			status = "incomplete"
			event = "response.incomplete"
		}
		resp := s.responseObject(status)
		if status == "incomplete" {
			resp["incomplete_details"] = IncompleteDetail{Reason: "max_output_tokens"}
		}
		resp["usage"] = *anthropicUsageToResponses(s.usage)
		if err := s.send(event, map[string]any{"response": resp}); err != nil {
			return err
		}
		s.finished = true
		return nil
	case "error":
		var ev StreamErrorEvent
		_ = json.Unmarshal([]byte(data), &ev)
		if err := s.send("error", map[string]any{"code": ev.Error.Type, "message": ev.Error.Message}); err != nil {
			return err
		}
		s.finished = true
		return nil
	}
	return nil
}

// mergeUsage keeps the latest supplied input counts, including explicit zeros.
func (s *responsesStreamWriter) mergeUsage(input, cacheRead, cacheCreation *int) {
	if input != nil {
		s.usage.InputTokens = *input
	}
	if cacheRead != nil {
		s.usage.CacheReadInputTokens = *cacheRead
	}
	if cacheCreation != nil {
		s.usage.CacheCreationInputTokens = *cacheCreation
	}
}

func (s *responsesStreamWriter) openBlock(block ContentBlock) error {
	s.block = &block
	s.outputIdx = len(s.output)
	s.text.Reset()
	s.args.Reset()
	s.customText.Reset()
	s.inputPos = -1
	s.inputEnd = false
	s.thinking.Reset()
	s.signature = ""
	switch block.Type {
	case "text":
		s.itemID = fmt.Sprintf("msg_%s_%d", s.respID, s.outputIdx)
		if err := s.send("response.output_item.added", map[string]any{
			"output_index": s.outputIdx,
			"item":         map[string]any{"type": "message", "id": s.itemID, "status": "in_progress", "role": "assistant", "content": []any{}},
		}); err != nil {
			return err
		}
		if err := s.send("response.content_part.added", map[string]any{
			"item_id": s.itemID, "output_index": s.outputIdx, "content_index": 0,
			"part": OutputContent{Type: "output_text", Text: "", Annotations: []any{}},
		}); err != nil {
			return err
		}
		if block.Text != "" {
			return s.delta(Delta{Type: "text_delta", Text: block.Text})
		}
		return nil
	case "thinking":
		s.itemID = fmt.Sprintf("rs_%s_%d", s.respID, s.outputIdx)
		if err := s.send("response.output_item.added", map[string]any{
			"output_index": s.outputIdx,
			"item":         map[string]any{"type": "reasoning", "id": s.itemID, "status": "in_progress", "summary": []any{}},
		}); err != nil {
			return err
		}
		if err := s.send("response.reasoning_summary_part.added", map[string]any{
			"item_id": s.itemID, "output_index": s.outputIdx, "summary_index": 0,
			"part": SummaryItem{Type: "summary_text", Text: ""},
		}); err != nil {
			return err
		}
		s.signature = block.Signature
		if block.Thinking != "" {
			return s.delta(Delta{Type: "thinking_delta", Thinking: block.Thinking})
		}
		return nil
	case "tool_use":
		s.itemID = block.ID
		tool := s.customTools.lookup(block.Name)
		item := map[string]any{"type": "function_call", "id": block.ID, "call_id": block.ID, "name": tool.name, "status": "in_progress", "arguments": ""}
		if tool.namespace != "" {
			item["namespace"] = tool.namespace
		}
		if tool.custom {
			item["type"] = "custom_tool_call"
			item["input"] = ""
			delete(item, "arguments")
		}
		return s.send("response.output_item.added", map[string]any{
			"output_index": s.outputIdx,
			"item":         item,
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
		return s.send("response.output_text.delta", map[string]any{"item_id": s.itemID, "output_index": s.outputIdx, "content_index": 0, "delta": d.Text, "logprobs": []any{}})
	case "thinking_delta":
		s.thinking.WriteString(d.Thinking)
		return s.send("response.reasoning_summary_text.delta", map[string]any{"item_id": s.itemID, "output_index": s.outputIdx, "summary_index": 0, "delta": d.Thinking})
	case "signature_delta":
		s.signature += d.Signature
	case "input_json_delta":
		s.args.WriteString(d.PartialJSON)
		if s.customTools.lookup(s.block.Name).custom {
			return s.streamCustomInput()
		}
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
			Content: []OutputContent{{Type: "output_text", Text: s.text.String(), Annotations: []any{}}}}
		if err := s.send("response.output_text.done", map[string]any{
			"item_id": s.itemID, "output_index": s.outputIdx, "content_index": 0, "text": s.text.String(), "logprobs": []any{},
		}); err != nil {
			return err
		}
		if err := s.send("response.content_part.done", map[string]any{
			"item_id": s.itemID, "output_index": s.outputIdx, "content_index": 0, "part": item.Content[0],
		}); err != nil {
			return err
		}
	case "thinking":
		item = ResponsesOutput{Type: "reasoning", ID: s.itemID, Status: "completed", EncryptedContent: s.signature,
			Summary: []SummaryItem{{Type: "summary_text", Text: s.thinking.String()}}}
		if err := s.send("response.reasoning_summary_text.done", map[string]any{
			"item_id": s.itemID, "output_index": s.outputIdx, "summary_index": 0, "text": s.thinking.String(),
		}); err != nil {
			return err
		}
		if err := s.send("response.reasoning_summary_part.done", map[string]any{
			"item_id": s.itemID, "output_index": s.outputIdx, "summary_index": 0, "part": item.Summary[0],
		}); err != nil {
			return err
		}
	case "tool_use":
		if s.args.Len() > 0 {
			block.Input = json.RawMessage(s.args.String())
		} else if len(block.Input) == 0 {
			block.Input = json.RawMessage("{}")
		}
		var err error
		item, err = toolCallItem(block, s.customTools)
		if err != nil {
			if !s.customTools.lookup(block.Name).custom {
				return err
			}
			s.invalidCustom = err
			tool := s.customTools.lookup(block.Name)
			partial := s.customText.String()
			item = ResponsesOutput{Type: "custom_tool_call", ID: block.ID, Status: "incomplete", CallID: block.ID, Name: tool.name, Namespace: tool.namespace, Input: &partial}
		}
		item.ID = block.ID
		if item.Type == "custom_tool_call" {
			if s.inputPos < 0 && *item.Input != "" {
				s.customText.WriteString(*item.Input)
				if err := s.send("response.custom_tool_call_input.delta", map[string]any{"item_id": s.itemID, "output_index": s.outputIdx, "delta": *item.Input}); err != nil {
					return err
				}
			}
			if s.customText.String() != *item.Input {
				return fmt.Errorf("custom tool %q changed its input while streaming", block.Name)
			}
			if err := s.send("response.custom_tool_call_input.done", map[string]any{"item_id": s.itemID, "output_index": s.outputIdx, "input": *item.Input}); err != nil {
				return err
			}
		} else {
			if s.args.Len() == 0 && item.Arguments != "" {
				if err := s.send("response.function_call_arguments.delta", map[string]any{
					"item_id": s.itemID, "output_index": s.outputIdx, "delta": item.Arguments,
				}); err != nil {
					return err
				}
			}
			if err := s.send("response.function_call_arguments.done", map[string]any{
				"item_id": s.itemID, "output_index": s.outputIdx, "arguments": item.Arguments,
			}); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	s.output = append(s.output, item)
	return s.send("response.output_item.done", map[string]any{"output_index": s.outputIdx, "item": item})
}
