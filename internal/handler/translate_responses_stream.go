package handler

import (
	"encoding/json"
	"strings"
	"unicode"
)

// ResponsesStreamState tracks the state of the streaming translation
// from Responses API events to Anthropic SSE events.
type ResponsesStreamState struct {
	blockIndex       int
	openBlockType    string // "text", "tool_use", "thinking", ""
	toolCallBlocks   map[int]int // output_index -> Anthropic block index
	hasStarted       bool
	messageCompleted bool
	model            string

	// For infinite whitespace detection
	wsRunLength map[int]int // output_index -> consecutive whitespace count

	// For combining reasoning summaries
	reasoningSummaryBlock map[int]int // output_index -> block index
}

// NewResponsesStreamState creates a new stream state.
func NewResponsesStreamState(model string) *ResponsesStreamState {
	return &ResponsesStreamState{
		blockIndex:            -1,
		toolCallBlocks:        make(map[int]int),
		model:                 model,
		wsRunLength:           make(map[int]int),
		reasoningSummaryBlock: make(map[int]int),
	}
}

// TranslateEvent translates a single Responses API stream event into
// zero or more Anthropic SSE events.
func (s *ResponsesStreamState) TranslateEvent(eventType, data string) ([]SSEEvent, error) {
	var events []SSEEvent

	switch eventType {
	case "response.created":
		var evt struct {
			Response struct {
				ID    string         `json:"id"`
				Model string         `json:"model"`
				Usage *ResponsesUsage `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return nil, err
		}
		s.hasStarted = true
		s.model = evt.Response.Model

		usage := AnthropicUsage{}
		if evt.Response.Usage != nil {
			usage.InputTokens = evt.Response.Usage.InputTokens
			if evt.Response.Usage.InputTokensDetails != nil {
				usage.CacheReadInputTokens = evt.Response.Usage.InputTokensDetails.CachedTokens
				usage.InputTokens -= usage.CacheReadInputTokens
			}
		}

		events = append(events, SSEEvent{
			Event: "message_start",
			Data: MessageStartEvent{
				Type: "message_start",
				Message: AnthropicResponse{
					ID:    evt.Response.ID,
					Type:  "message",
					Role:  "assistant",
					Model: evt.Response.Model,
					Usage: usage,
				},
			},
		})

	case "response.output_item.added":
		var evt struct {
			OutputIndex int             `json:"output_index"`
			Item        json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return nil, err
		}
		var item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
			ID     string `json:"id"`
		}
		json.Unmarshal(evt.Item, &item)

		if item.Type == "function_call" {
			// Close any open block
			events = append(events, s.closeCurrentBlock()...)
			s.blockIndex++
			s.toolCallBlocks[evt.OutputIndex] = s.blockIndex
			s.openBlockType = "tool_use"
			s.wsRunLength[evt.OutputIndex] = 0

			events = append(events, SSEEvent{
				Event: "content_block_start",
				Data: ContentBlockStartEvent{
					Type:  "content_block_start",
					Index: s.blockIndex,
					ContentBlock: ContentBlock{
						Type: "tool_use",
						ID:   item.CallID,
						Name: item.Name,
					},
				},
			})
		}

	case "response.output_item.done":
		var evt struct {
			OutputIndex int             `json:"output_index"`
			Item        json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return nil, err
		}
		var item ResponsesOutput
		json.Unmarshal(evt.Item, &item)

		if item.Type == "reasoning" {
			// Emit thinking block with summary and signature
			events = append(events, s.closeCurrentBlock()...)
			s.blockIndex++
			s.openBlockType = "thinking"

			thinking := "Thinking..."
			if len(item.Summary) > 0 {
				var parts []string
				for _, sum := range item.Summary {
					parts = append(parts, sum.Text)
				}
				thinking = strings.Join(parts, "\n")
			}

			sig := ""
			if item.EncryptedContent != "" {
				sig = item.EncryptedContent
				if item.ID != "" {
					sig += "@" + item.ID
				}
			}

			events = append(events, SSEEvent{
				Event: "content_block_start",
				Data: ContentBlockStartEvent{
					Type:  "content_block_start",
					Index: s.blockIndex,
					ContentBlock: ContentBlock{
						Type:     "thinking",
						Thinking: "",
					},
				},
			})
			events = append(events, SSEEvent{
				Event: "content_block_delta",
				Data: ContentBlockDeltaEvent{
					Type:  "content_block_delta",
					Index: s.blockIndex,
					Delta: Delta{Type: "thinking_delta", Thinking: thinking},
				},
			})
			if sig != "" {
				events = append(events, SSEEvent{
					Event: "content_block_delta",
					Data: ContentBlockDeltaEvent{
						Type:  "content_block_delta",
						Index: s.blockIndex,
						Delta: Delta{Type: "signature_delta", Signature: sig},
					},
				})
			}
			events = append(events, s.closeCurrentBlock()...)
		}

		// Close tool_use block when function_call is done
		if item.Type == "function_call" {
			if blockIdx, ok := s.toolCallBlocks[evt.OutputIndex]; ok {
				if s.openBlockType == "tool_use" && s.blockIndex == blockIdx {
					events = append(events, s.closeCurrentBlock()...)
				}
			}
		}

	case "response.reasoning_summary_text.delta":
		var evt struct {
			ItemID       string `json:"item_id"`
			OutputIndex  int    `json:"output_index"`
			SummaryIndex int    `json:"summary_index"`
			Delta        string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return nil, err
		}

		// Check if we already have a thinking block for this output_index
		blockIdx, exists := s.reasoningSummaryBlock[evt.OutputIndex]
		if !exists {
			// Open a new thinking block
			events = append(events, s.closeCurrentBlock()...)
			s.blockIndex++
			blockIdx = s.blockIndex
			s.reasoningSummaryBlock[evt.OutputIndex] = blockIdx
			s.openBlockType = "thinking"

			events = append(events, SSEEvent{
				Event: "content_block_start",
				Data: ContentBlockStartEvent{
					Type:  "content_block_start",
					Index: blockIdx,
					ContentBlock: ContentBlock{
						Type:     "thinking",
						Thinking: "",
					},
				},
			})
		}

		events = append(events, SSEEvent{
			Event: "content_block_delta",
			Data: ContentBlockDeltaEvent{
				Type:  "content_block_delta",
				Index: blockIdx,
				Delta: Delta{Type: "thinking_delta", Thinking: evt.Delta},
			},
		})

	case "response.output_text.delta":
		var evt struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return nil, err
		}

		if s.openBlockType != "text" {
			events = append(events, s.closeCurrentBlock()...)
			s.blockIndex++
			s.openBlockType = "text"
			events = append(events, SSEEvent{
				Event: "content_block_start",
				Data: ContentBlockStartEvent{
					Type:  "content_block_start",
					Index: s.blockIndex,
					ContentBlock: ContentBlock{
						Type: "text",
						Text: "",
					},
				},
			})
		}

		events = append(events, SSEEvent{
			Event: "content_block_delta",
			Data: ContentBlockDeltaEvent{
				Type:  "content_block_delta",
				Index: s.blockIndex,
				Delta: Delta{Type: "text_delta", Text: evt.Delta},
			},
		})

	case "response.function_call_arguments.delta":
		var evt struct {
			OutputIndex int    `json:"output_index"`
			Delta       string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return nil, err
		}

		// Infinite whitespace detection
		wsCount := s.wsRunLength[evt.OutputIndex]
		for _, r := range evt.Delta {
			if unicode.IsSpace(r) {
				wsCount++
			} else {
				wsCount = 0
			}
		}
		s.wsRunLength[evt.OutputIndex] = wsCount

		if wsCount > 20 {
			// Abort: Copilot infinite whitespace bug
			events = append(events, s.closeCurrentBlock()...)
			events = append(events, SSEEvent{
				Event: "error",
				Data: StreamErrorEvent{
					Type: "error",
					Error: StreamErrBody{
						Type:    "api_error",
						Message: "Function call arguments contain excessive whitespace (possible infinite loop). Stream aborted.",
					},
				},
			})
			return events, nil
		}

		if blockIdx, ok := s.toolCallBlocks[evt.OutputIndex]; ok {
			events = append(events, SSEEvent{
				Event: "content_block_delta",
				Data: ContentBlockDeltaEvent{
					Type:  "content_block_delta",
					Index: blockIdx,
					Delta: Delta{Type: "input_json_delta", PartialJSON: evt.Delta},
				},
			})
		}

	case "response.function_call_arguments.done":
		// No-op: arguments already streamed via deltas

	case "response.completed", "response.incomplete":
		s.messageCompleted = true
		events = append(events, s.closeCurrentBlock()...)

		// Parse the full result for final usage/stop_reason
		var evt struct {
			Response json.RawMessage `json:"response"`
		}
		json.Unmarshal([]byte(data), &evt)

		var result ResponsesResult
		if evt.Response != nil {
			json.Unmarshal(evt.Response, &result)
		}

		translated := translateResponsesResultToAnthropic(&result)

		events = append(events, SSEEvent{
			Event: "message_delta",
			Data: MessageDeltaEvent{
				Type: "message_delta",
				Delta: MessageDelta{
					StopReason: translated.StopReason,
				},
				Usage: DeltaUsage{
					OutputTokens: translated.Usage.OutputTokens,
				},
			},
		})
		events = append(events, SSEEvent{
			Event: "message_stop",
			Data:  MessageStopEvent{Type: "message_stop"},
		})

	case "response.failed":
		s.messageCompleted = true
		var evt struct {
			Response struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		json.Unmarshal([]byte(data), &evt)

		events = append(events, s.closeCurrentBlock()...)
		msg := "Response failed"
		if evt.Response.Error.Message != "" {
			msg = evt.Response.Error.Message
		}
		events = append(events, SSEEvent{
			Event: "error",
			Data: StreamErrorEvent{
				Type:  "error",
				Error: StreamErrBody{Type: "api_error", Message: msg},
			},
		})

	case "error":
		s.messageCompleted = true
		var evt struct {
			Message string `json:"message"`
		}
		json.Unmarshal([]byte(data), &evt)

		events = append(events, s.closeCurrentBlock()...)
		events = append(events, SSEEvent{
			Event: "error",
			Data: StreamErrorEvent{
				Type:  "error",
				Error: StreamErrBody{Type: "api_error", Message: evt.Message},
			},
		})
	}

	return events, nil
}

func (s *ResponsesStreamState) closeCurrentBlock() []SSEEvent {
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

// IsComplete returns true if the stream has received a completion event.
func (s *ResponsesStreamState) IsComplete() bool {
	return s.messageCompleted
}
