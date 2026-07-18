package handler

import "encoding/json"

func (s *ResponsesStreamState) handleFunctionArgumentsDelta(data string) ([]SSEEvent, error) {
	var event struct {
		OutputIndex int    `json:"output_index"`
		Delta       string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	whitespace := s.wsRunLength[event.OutputIndex]
	for _, char := range event.Delta {
		if char == '\r' || char == '\n' || char == '\t' {
			whitespace++
		} else {
			whitespace = 0
		}
	}
	s.wsRunLength[event.OutputIndex] = whitespace
	if whitespace > 20 {
		events := s.closeCurrentBlock()
		events = append(events, streamErrorEvent("Function call arguments contain excessive whitespace (possible infinite loop). Stream aborted."))
		return events, nil
	}
	blockIndex, ok := s.toolCallBlocks[event.OutputIndex]
	if !ok {
		return nil, nil
	}
	s.blockHasDelta[blockIndex] = true
	return []SSEEvent{{Event: "content_block_delta", Data: ContentBlockDeltaEvent{
		Type: "content_block_delta", Index: blockIndex,
		Delta: Delta{Type: "input_json_delta", PartialJSON: event.Delta},
	}}}, nil
}

func (s *ResponsesStreamState) handleFunctionArgumentsDone(data string) ([]SSEEvent, error) {
	var event struct {
		OutputIndex int    `json:"output_index"`
		Arguments   string `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	blockIndex, ok := s.toolCallBlocks[event.OutputIndex]
	if !ok || event.Arguments == "" || s.blockHasDelta[blockIndex] {
		return nil, nil
	}
	return []SSEEvent{{Event: "content_block_delta", Data: ContentBlockDeltaEvent{
		Type: "content_block_delta", Index: blockIndex,
		Delta: Delta{Type: "input_json_delta", PartialJSON: event.Arguments},
	}}}, nil
}
