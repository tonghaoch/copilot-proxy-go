package handler

// ResponsesStreamState tracks the state of the streaming translation from
// Responses API events to Anthropic SSE events.
type ResponsesStreamState struct {
	blockIndex       int
	openBlockType    string
	toolCallBlocks   map[int]int
	hasStarted       bool
	messageCompleted bool
	model            string
	wsRunLength      map[int]int

	reasoningSummaryBlock map[int]int
	blockHasDelta         map[int]bool
	textBlockByKey        map[string]int

	inputTokens  int
	outputTokens int
	cachedTokens int
}

func NewResponsesStreamState(model string) *ResponsesStreamState {
	return &ResponsesStreamState{
		blockIndex: -1, toolCallBlocks: make(map[int]int), model: model,
		wsRunLength: make(map[int]int), reasoningSummaryBlock: make(map[int]int),
		blockHasDelta: make(map[int]bool), textBlockByKey: make(map[string]int),
	}
}

func (s *ResponsesStreamState) TokenCounts() (input, output, cached int) {
	return s.inputTokens, s.outputTokens, s.cachedTokens
}

func (s *ResponsesStreamState) TranslateEvent(eventType, data string) ([]SSEEvent, error) {
	switch eventType {
	case "response.created":
		return s.handleResponseCreated(data)
	case "response.output_item.added":
		return s.handleOutputItemAdded(data)
	case "response.output_item.done":
		return s.handleOutputItemDone(data)
	case "response.reasoning_summary_text.delta":
		return s.handleReasoningDelta(data)
	case "response.reasoning_summary_text.done":
		return s.handleReasoningDone(data)
	case "response.output_text.delta":
		return s.handleOutputTextDelta(data)
	case "response.output_text.done":
		return s.handleOutputTextDone(data)
	case "response.function_call_arguments.delta":
		return s.handleFunctionArgumentsDelta(data)
	case "response.function_call_arguments.done":
		return s.handleFunctionArgumentsDone(data)
	case "response.completed", "response.incomplete":
		return s.handleResponseCompleted(data)
	case "response.failed":
		return s.handleResponseFailed(data)
	case "error":
		return s.handleStreamError(data)
	default:
		return nil, nil
	}
}

func (s *ResponsesStreamState) closeCurrentBlock() []SSEEvent {
	if s.openBlockType == "" {
		return nil
	}
	event := SSEEvent{
		Event: "content_block_stop",
		Data:  ContentBlockStopEvent{Type: "content_block_stop", Index: s.blockIndex},
	}
	s.openBlockType = ""
	return []SSEEvent{event}
}

func (s *ResponsesStreamState) IsComplete() bool {
	return s.messageCompleted
}
