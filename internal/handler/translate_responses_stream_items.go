package handler

import (
	"encoding/json"
	"strings"
)

func (s *ResponsesStreamState) handleOutputItemAdded(data string) ([]SSEEvent, error) {
	var event struct {
		OutputIndex int             `json:"output_index"`
		Item        json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	var item struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Name   string `json:"name"`
	}
	json.Unmarshal(event.Item, &item)
	if item.Type != "function_call" {
		return nil, nil
	}
	events := s.closeCurrentBlock()
	s.blockIndex++
	s.toolCallBlocks[event.OutputIndex] = s.blockIndex
	s.openBlockType = "tool_use"
	s.wsRunLength[event.OutputIndex] = 0
	events = append(events, SSEEvent{
		Event: "content_block_start",
		Data: ContentBlockStartEvent{Type: "content_block_start", Index: s.blockIndex,
			ContentBlock: ContentBlock{Type: "tool_use", ID: item.CallID, Name: item.Name}},
	})
	return events, nil
}

func (s *ResponsesStreamState) handleOutputItemDone(data string) ([]SSEEvent, error) {
	var event struct {
		OutputIndex int             `json:"output_index"`
		Item        json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	var item ResponsesOutput
	json.Unmarshal(event.Item, &item)
	var events []SSEEvent
	if item.Type == "reasoning" {
		events = append(events, s.closeCurrentBlock()...)
		s.blockIndex++
		s.openBlockType = "thinking"
		thinking := "Thinking..."
		if len(item.Summary) > 0 {
			parts := make([]string, 0, len(item.Summary))
			for _, summary := range item.Summary {
				parts = append(parts, summary.Text)
			}
			thinking = strings.Join(parts, "\n")
		}
		signature := item.EncryptedContent
		if signature != "" && item.ID != "" {
			signature += "@" + item.ID
		}
		events = append(events,
			SSEEvent{Event: "content_block_start", Data: ContentBlockStartEvent{
				Type: "content_block_start", Index: s.blockIndex,
				ContentBlock: ContentBlock{Type: "thinking", Thinking: ""},
			}},
			SSEEvent{Event: "content_block_delta", Data: ContentBlockDeltaEvent{
				Type: "content_block_delta", Index: s.blockIndex,
				Delta: Delta{Type: "thinking_delta", Thinking: thinking},
			}},
		)
		if signature != "" {
			events = append(events, SSEEvent{Event: "content_block_delta", Data: ContentBlockDeltaEvent{
				Type: "content_block_delta", Index: s.blockIndex,
				Delta: Delta{Type: "signature_delta", Signature: signature},
			}})
		}
		events = append(events, s.closeCurrentBlock()...)
	}
	if item.Type == "function_call" {
		if blockIndex, ok := s.toolCallBlocks[event.OutputIndex]; ok && s.openBlockType == "tool_use" && s.blockIndex == blockIndex {
			events = append(events, s.closeCurrentBlock()...)
		}
	}
	return events, nil
}
