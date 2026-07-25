package handler

import (
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// SSEEvent is a single event to write to the client.
type SSEEvent struct {
	Event string
	Data  any
}

// AnthropicStreamState tracks the state of the streaming translation
// from OpenAI Chat Completion chunks to Anthropic SSE events.
type AnthropicStreamState struct {
	blockIndex    int
	openBlockType string      // "text", "tool_use", "thinking", ""
	toolCallMap   map[int]int // OpenAI tool call index -> Anthropic block index
	hasStarted    bool
	completed     bool
	model         string
	inputTokens   int
	outputTokens  int
	cachedTokens  int
	isClaudeModel bool
}

// NewAnthropicStreamState creates a new stream state.
func NewAnthropicStreamState(model string) *AnthropicStreamState {
	return &AnthropicStreamState{
		blockIndex:    -1,
		toolCallMap:   make(map[int]int),
		model:         model,
		isClaudeModel: isClaude(model),
	}
}

// TokenCounts returns the accumulated token counts from the stream.
func (s *AnthropicStreamState) TokenCounts() (input, output, cached int) {
	return s.inputTokens, s.outputTokens, s.cachedTokens
}

// TranslateChunk translates a single OpenAI Chat Completion chunk into
// zero or more Anthropic SSE events.
func (s *AnthropicStreamState) TranslateChunk(chunk *ChatCompletionChunk) []SSEEvent {
	var events []SSEEvent

	// Emit message_start on first chunk
	if !s.hasStarted {
		s.hasStarted = true
		usage := s.captureUsage(chunk.Usage)
		events = append(events, SSEEvent{
			Event: "message_start",
			Data: MessageStartEvent{
				Type: "message_start",
				Message: AnthropicResponse{
					ID:    chunk.ID,
					Type:  "message",
					Role:  "assistant",
					Model: chunk.Model,
					Usage: usage,
				},
			},
		})
	}

	if len(chunk.Choices) == 0 {
		// Usage-only chunk at the end
		s.captureUsage(chunk.Usage)
		return events
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	// Handle reasoning_text (thinking)
	if delta.ReasoningText != nil && *delta.ReasoningText != "" {
		if s.openBlockType == "text" && s.isClaudeModel {
			// Edge case: reasoning_text arrives while text block is open
			// Treat as text content instead (Copilot bug workaround)
			events = append(events, SSEEvent{
				Event: "content_block_delta",
				Data: ContentBlockDeltaEvent{
					Type:  "content_block_delta",
					Index: s.blockIndex,
					Delta: Delta{Type: "text_delta", Text: *delta.ReasoningText},
				},
			})
			return events
		}

		if s.openBlockType != "thinking" {
			events = append(events, s.closeCurrentBlock()...)
			events = append(events, s.openThinkingBlock()...)
		}
		events = append(events, SSEEvent{
			Event: "content_block_delta",
			Data: ContentBlockDeltaEvent{
				Type:  "content_block_delta",
				Index: s.blockIndex,
				Delta: Delta{Type: "thinking_delta", Thinking: *delta.ReasoningText},
			},
		})
	}

	// Handle reasoning_opaque
	if delta.ReasoningOpaque != nil && *delta.ReasoningOpaque != "" {
		opaque := *delta.ReasoningOpaque

		// If a thinking block is open and we get opaque, close with signature
		if s.openBlockType == "thinking" {
			events = append(events, SSEEvent{
				Event: "content_block_delta",
				Data: ContentBlockDeltaEvent{
					Type:  "content_block_delta",
					Index: s.blockIndex,
					Delta: Delta{Type: "signature_delta", Signature: opaque},
				},
			})
			events = append(events, s.closeCurrentBlock()...)
		} else if delta.Content != nil && *delta.Content == "" && s.openBlockType == "thinking" {
			// Edge case: empty content with opaque while thinking is open
			events = append(events, SSEEvent{
				Event: "content_block_delta",
				Data: ContentBlockDeltaEvent{
					Type:  "content_block_delta",
					Index: s.blockIndex,
					Delta: Delta{Type: "signature_delta", Signature: opaque},
				},
			})
			events = append(events, s.closeCurrentBlock()...)
		} else {
			// Self-contained opaque thinking block
			events = append(events, s.closeCurrentBlock()...)
			events = append(events, s.openThinkingBlock()...)
			events = append(events, SSEEvent{
				Event: "content_block_delta",
				Data: ContentBlockDeltaEvent{
					Type:  "content_block_delta",
					Index: s.blockIndex,
					Delta: Delta{Type: "thinking_delta", Thinking: "Thinking..."},
				},
			})
			events = append(events, SSEEvent{
				Event: "content_block_delta",
				Data: ContentBlockDeltaEvent{
					Type:  "content_block_delta",
					Index: s.blockIndex,
					Delta: Delta{Type: "signature_delta", Signature: opaque},
				},
			})
			events = append(events, s.closeCurrentBlock()...)
		}
	}

	// Handle text content
	if delta.Content != nil && *delta.Content != "" {
		if s.openBlockType == "thinking" {
			// Close thinking block before opening text
			events = append(events, SSEEvent{
				Event: "content_block_delta",
				Data: ContentBlockDeltaEvent{
					Type:  "content_block_delta",
					Index: s.blockIndex,
					Delta: Delta{Type: "signature_delta", Signature: ""},
				},
			})
			events = append(events, s.closeCurrentBlock()...)
		}

		if s.openBlockType != "text" {
			events = append(events, s.closeCurrentBlock()...)
			events = append(events, s.openTextBlock()...)
		}
		events = append(events, SSEEvent{
			Event: "content_block_delta",
			Data: ContentBlockDeltaEvent{
				Type:  "content_block_delta",
				Index: s.blockIndex,
				Delta: Delta{Type: "text_delta", Text: *delta.Content},
			},
		})
	}

	// Handle tool calls
	for _, tc := range delta.ToolCalls {
		blockIdx, exists := s.toolCallMap[tc.Index]
		if !exists {
			// New tool call: close current block, open tool_use
			events = append(events, s.closeCurrentBlock()...)
			s.blockIndex++
			blockIdx = s.blockIndex
			s.toolCallMap[tc.Index] = blockIdx
			s.openBlockType = "tool_use"

			name := ""
			if tc.Function != nil {
				name = tc.Function.Name
			}
			events = append(events, SSEEvent{
				Event: "content_block_start",
				Data: ContentBlockStartEvent{
					Type:  "content_block_start",
					Index: blockIdx,
					ContentBlock: ContentBlock{
						Type: "tool_use",
						ID:   tc.ID,
						Name: name,
					},
				},
			})
		}

		if tc.Function != nil && tc.Function.Arguments != "" {
			events = append(events, SSEEvent{
				Event: "content_block_delta",
				Data: ContentBlockDeltaEvent{
					Type:  "content_block_delta",
					Index: blockIdx,
					Delta: Delta{Type: "input_json_delta", PartialJSON: tc.Function.Arguments},
				},
			})
		}
	}

	// Handle finish_reason
	if choice.FinishReason != nil {
		s.captureUsage(chunk.Usage)
		events = append(events, s.finish(mapStopReason(*choice.FinishReason))...)
	}

	return events
}

// IsComplete reports whether the stream reached a finish_reason.
func (s *AnthropicStreamState) IsComplete() bool { return s.completed }

// Finish closes any open block and ends the message. Callers use it when the
// stream stopped without a finish_reason; without it the client waits forever.
func (s *AnthropicStreamState) Finish(stopReason string) []SSEEvent {
	if s.completed {
		return nil
	}
	return s.finish(stopReason)
}

func (s *AnthropicStreamState) finish(stopReason string) []SSEEvent {
	s.completed = true
	events := s.closeCurrentBlock()
	if !s.hasStarted {
		s.hasStarted = true
		events = append(events, SSEEvent{
			Event: "message_start",
			Data: MessageStartEvent{Type: "message_start", Message: AnthropicResponse{
				Type: "message", Role: "assistant", Model: s.model,
			}},
		})
	}
	return append(events,
		SSEEvent{Event: "message_delta", Data: MessageDeltaEvent{
			Type:  "message_delta",
			Delta: MessageDelta{StopReason: stopReason},
			Usage: DeltaUsage{OutputTokens: s.outputTokens},
		}},
		SSEEvent{Event: "message_stop", Data: MessageStopEvent{Type: "message_stop"}},
	)
}

// captureUsage updates the metrics counters from any chunk carrying usage.
// OpenAI commonly sends usage only in a final chunk with no choices, while
// Anthropic needs non-cached input tokens in message_start. Keep the internal
// input counter as the full prompt total so metrics remain consistent with the
// non-streaming Chat, Responses, and native Messages paths.
func (s *AnthropicStreamState) captureUsage(usage *ChatCompletionUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}
	cached := 0
	if usage.PromptTokensDetails != nil {
		cached = usage.PromptTokensDetails.CachedTokens
	}
	s.inputTokens = usage.PromptTokens
	s.outputTokens = usage.CompletionTokens
	s.cachedTokens = cached
	return AnthropicUsage{
		InputTokens:          usage.PromptTokens - cached,
		OutputTokens:         usage.CompletionTokens,
		CacheReadInputTokens: cached,
	}
}

func (s *AnthropicStreamState) closeCurrentBlock() []SSEEvent {
	if s.openBlockType == "" {
		return nil
	}
	event := SSEEvent{
		Event: "content_block_stop",
		Data: ContentBlockStopEvent{
			Type:  "content_block_stop",
			Index: s.blockIndex,
		},
	}
	s.openBlockType = ""
	return []SSEEvent{event}
}

func (s *AnthropicStreamState) openThinkingBlock() []SSEEvent {
	if s.openBlockType != "" {
		return nil // should close first
	}
	s.blockIndex++
	s.openBlockType = "thinking"
	return []SSEEvent{{
		Event: "content_block_start",
		Data: ContentBlockStartEvent{
			Type:  "content_block_start",
			Index: s.blockIndex,
			ContentBlock: ContentBlock{
				Type:     "thinking",
				Thinking: "",
			},
		},
	}}
}

func (s *AnthropicStreamState) openTextBlock() []SSEEvent {
	if s.openBlockType != "" {
		return nil
	}
	s.blockIndex++
	s.openBlockType = "text"
	return []SSEEvent{{
		Event: "content_block_start",
		Data: ContentBlockStartEvent{
			Type:  "content_block_start",
			Index: s.blockIndex,
			ContentBlock: ContentBlock{
				Type: "text",
				Text: "",
			},
		},
	}}
}

// TranslateErrorEvent creates an Anthropic error SSE event.
func TranslateErrorEvent(message string) SSEEvent {
	return SSEEvent{
		Event: "error",
		Data: StreamErrorEvent{
			Type: "error",
			Error: StreamErrBody{
				Type:    "api_error",
				Message: message,
			},
		},
	}
}

// isInitiatorAgent checks if the last message in the Anthropic request
// indicates an agent-initiated request (no user text content).
func isInitiatorAgent(messages []AnthropicMsg) bool {
	if len(messages) == 0 {
		return false
	}
	last := messages[len(messages)-1]
	if last.Role != "user" {
		return true
	}
	blocks := ParseMessageContent(last.Content)
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

// isResponsesSupported checks if a model supports the Responses API.
func isResponsesSupported(model *state.Model) bool {
	if model == nil {
		return false
	}
	for _, ep := range model.SupportedEndpoints {
		if ep == "/responses" {
			return true
		}
	}
	return false
}

// isMessagesSupported checks if a model supports the native Messages API.
func isMessagesSupported(model *state.Model) bool {
	if model == nil {
		return false
	}
	for _, ep := range model.SupportedEndpoints {
		if strings.Contains(ep, "/v1/messages") {
			return true
		}
	}
	return false
}
