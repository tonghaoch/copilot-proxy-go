package handler

import (
	"encoding/json"
	"fmt"
)

func (s *ResponsesStreamState) handleOutputTextDelta(data string) ([]SSEEvent, error) {
	var event struct {
		OutputIndex  int    `json:"output_index"`
		ContentIndex int    `json:"content_index"`
		Delta        string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	var events []SSEEvent
	blockIndex := s.openOrGetTextBlock(event.OutputIndex, event.ContentIndex, &events)
	events = append(events, SSEEvent{Event: "content_block_delta", Data: ContentBlockDeltaEvent{
		Type: "content_block_delta", Index: blockIndex, Delta: Delta{Type: "text_delta", Text: event.Delta},
	}})
	s.blockHasDelta[blockIndex] = true
	return events, nil
}

func (s *ResponsesStreamState) handleOutputTextDone(data string) ([]SSEEvent, error) {
	var event struct {
		OutputIndex  int    `json:"output_index"`
		ContentIndex int    `json:"content_index"`
		Text         string `json:"text"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	var events []SSEEvent
	blockIndex := s.openOrGetTextBlock(event.OutputIndex, event.ContentIndex, &events)
	if event.Text != "" && !s.blockHasDelta[blockIndex] {
		events = append(events, SSEEvent{Event: "content_block_delta", Data: ContentBlockDeltaEvent{
			Type: "content_block_delta", Index: blockIndex, Delta: Delta{Type: "text_delta", Text: event.Text},
		}})
	}
	return events, nil
}

func (s *ResponsesStreamState) openOrGetTextBlock(outputIndex, contentIndex int, events *[]SSEEvent) int {
	key := fmt.Sprintf("%d:%d", outputIndex, contentIndex)
	if blockIndex, ok := s.textBlockByKey[key]; ok {
		return blockIndex
	}
	*events = append(*events, s.closeCurrentBlock()...)
	s.blockIndex++
	blockIndex := s.blockIndex
	s.textBlockByKey[key] = blockIndex
	s.openBlockType = "text"
	*events = append(*events, SSEEvent{Event: "content_block_start", Data: ContentBlockStartEvent{
		Type: "content_block_start", Index: blockIndex, ContentBlock: ContentBlock{Type: "text", Text: ""},
	}})
	return blockIndex
}
