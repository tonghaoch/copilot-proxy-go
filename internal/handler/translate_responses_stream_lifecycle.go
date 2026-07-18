package handler

import "encoding/json"

func (s *ResponsesStreamState) handleResponseCreated(data string) ([]SSEEvent, error) {
	var event struct {
		Response struct {
			ID    string          `json:"id"`
			Model string          `json:"model"`
			Usage *ResponsesUsage `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	s.hasStarted = true
	s.model = event.Response.Model
	usage := AnthropicUsage{}
	if event.Response.Usage != nil {
		usage.InputTokens = event.Response.Usage.InputTokens
		if event.Response.Usage.InputTokensDetails != nil {
			usage.CacheReadInputTokens = event.Response.Usage.InputTokensDetails.CachedTokens
			usage.InputTokens -= usage.CacheReadInputTokens
		}
		s.inputTokens = usage.InputTokens
		s.cachedTokens = usage.CacheReadInputTokens
	}
	return []SSEEvent{{
		Event: "message_start",
		Data: MessageStartEvent{Type: "message_start", Message: AnthropicResponse{
			ID: event.Response.ID, Type: "message", Role: "assistant", Model: event.Response.Model, Usage: usage,
		}},
	}}, nil
}

func (s *ResponsesStreamState) handleResponseCompleted(data string) ([]SSEEvent, error) {
	s.messageCompleted = true
	events := s.closeCurrentBlock()
	var event struct {
		Response json.RawMessage `json:"response"`
	}
	json.Unmarshal([]byte(data), &event)
	var result ResponsesResult
	if event.Response != nil {
		json.Unmarshal(event.Response, &result)
	}
	translated := translateResponsesResultToAnthropic(&result)
	if result.Usage != nil {
		s.inputTokens = translated.Usage.InputTokens
		s.outputTokens = translated.Usage.OutputTokens
		s.cachedTokens = translated.Usage.CacheReadInputTokens
	}
	events = append(events,
		SSEEvent{Event: "message_delta", Data: MessageDeltaEvent{
			Type: "message_delta", Delta: MessageDelta{StopReason: translated.StopReason},
			Usage: DeltaUsage{OutputTokens: translated.Usage.OutputTokens},
		}},
		SSEEvent{Event: "message_stop", Data: MessageStopEvent{Type: "message_stop"}},
	)
	return events, nil
}

func (s *ResponsesStreamState) handleResponseFailed(data string) ([]SSEEvent, error) {
	s.messageCompleted = true
	var event struct {
		Response struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	json.Unmarshal([]byte(data), &event)
	message := "Response failed"
	if event.Response.Error.Message != "" {
		message = event.Response.Error.Message
	}
	events := s.closeCurrentBlock()
	events = append(events, streamErrorEvent(message))
	return events, nil
}

func (s *ResponsesStreamState) handleStreamError(data string) ([]SSEEvent, error) {
	s.messageCompleted = true
	var event struct {
		Message string `json:"message"`
	}
	json.Unmarshal([]byte(data), &event)
	events := s.closeCurrentBlock()
	events = append(events, streamErrorEvent(event.Message))
	return events, nil
}

func streamErrorEvent(message string) SSEEvent {
	return SSEEvent{Event: "error", Data: StreamErrorEvent{
		Type: "error", Error: StreamErrBody{Type: "api_error", Message: message},
	}}
}
