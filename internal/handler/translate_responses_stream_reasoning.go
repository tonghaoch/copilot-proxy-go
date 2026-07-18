package handler

import "encoding/json"

func (s *ResponsesStreamState) handleReasoningDelta(data string) ([]SSEEvent, error) {
	var event struct {
		OutputIndex int    `json:"output_index"`
		Delta       string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	blockIndex, exists := s.reasoningSummaryBlock[event.OutputIndex]
	var events []SSEEvent
	if !exists {
		events = append(events, s.closeCurrentBlock()...)
		s.blockIndex++
		blockIndex = s.blockIndex
		s.reasoningSummaryBlock[event.OutputIndex] = blockIndex
		s.openBlockType = "thinking"
		events = append(events, thinkingBlockStart(blockIndex))
	}
	events = append(events, SSEEvent{Event: "content_block_delta", Data: ContentBlockDeltaEvent{
		Type: "content_block_delta", Index: blockIndex,
		Delta: Delta{Type: "thinking_delta", Thinking: event.Delta},
	}})
	s.blockHasDelta[blockIndex] = true
	return events, nil
}

func (s *ResponsesStreamState) handleReasoningDone(data string) ([]SSEEvent, error) {
	var event struct {
		OutputIndex int    `json:"output_index"`
		Text        string `json:"text"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	blockIndex, exists := s.reasoningSummaryBlock[event.OutputIndex]
	var events []SSEEvent
	if !exists {
		events = append(events, s.closeCurrentBlock()...)
		s.blockIndex++
		blockIndex = s.blockIndex
		s.reasoningSummaryBlock[event.OutputIndex] = blockIndex
		s.openBlockType = "thinking"
		events = append(events, thinkingBlockStart(blockIndex))
	}
	if event.Text != "" && !s.blockHasDelta[blockIndex] {
		events = append(events, SSEEvent{Event: "content_block_delta", Data: ContentBlockDeltaEvent{
			Type: "content_block_delta", Index: blockIndex,
			Delta: Delta{Type: "thinking_delta", Thinking: event.Text},
		}})
	}
	return events, nil
}

func thinkingBlockStart(index int) SSEEvent {
	return SSEEvent{Event: "content_block_start", Data: ContentBlockStartEvent{
		Type: "content_block_start", Index: index,
		ContentBlock: ContentBlock{Type: "thinking", Thinking: ""},
	}}
}
