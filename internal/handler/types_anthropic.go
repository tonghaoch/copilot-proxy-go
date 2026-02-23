package handler

import "encoding/json"

// --- Request Types ---

// AnthropicRequest is the incoming request to POST /v1/messages.
type AnthropicRequest struct {
	Model         string           `json:"model"`
	Messages      []AnthropicMsg   `json:"messages"`
	MaxTokens     int              `json:"max_tokens"`
	System        json.RawMessage  `json:"system,omitempty"`
	Metadata      *AnthropicMeta   `json:"metadata,omitempty"`
	StopSequences []string         `json:"stop_sequences,omitempty"`
	Stream        bool             `json:"stream"`
	Temperature   *float64         `json:"temperature,omitempty"`
	TopP          *float64         `json:"top_p,omitempty"`
	TopK          *int             `json:"top_k,omitempty"`
	Tools         []AnthropicTool  `json:"tools,omitempty"`
	ToolChoice    json.RawMessage  `json:"tool_choice,omitempty"`
	Thinking      *ThinkingConfig  `json:"thinking,omitempty"`
	OutputConfig  *OutputConfig    `json:"output_config,omitempty"`
}

type AnthropicMeta struct {
	UserID string `json:"user_id,omitempty"`
}

type ThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type OutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// AnthropicMsg represents a message in the Anthropic format.
type AnthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or []ContentBlock
}

// ContentBlock is a flat union of all Anthropic content block types.
type ContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// image
	Source *ImageSource `json:"source,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string or []ContentBlock
	IsError   *bool           `json:"is_error,omitempty"`

	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // e.g. "image/png"
	Data      string `json:"data"`
}

// AnthropicTool defines a tool in the Anthropic format.
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// --- Response Types ---

// AnthropicResponse is the response we return from POST /v1/messages.
type AnthropicResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Content      []ContentBlock `json:"content"`
	Model        string         `json:"model"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        AnthropicUsage `json:"usage"`
}

type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// --- SSE Stream Event Types ---

type MessageStartEvent struct {
	Type    string           `json:"type"`
	Message AnthropicResponse `json:"message"`
}

type ContentBlockStartEvent struct {
	Type         string       `json:"type"`
	Index        int          `json:"index"`
	ContentBlock ContentBlock `json:"content_block"`
}

type ContentBlockDeltaEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta Delta  `json:"delta"`
}

type Delta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	Signature   string `json:"signature,omitempty"`
}

type ContentBlockStopEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type MessageDeltaEvent struct {
	Type  string       `json:"type"`
	Delta MessageDelta `json:"delta"`
	Usage DeltaUsage   `json:"usage"`
}

type MessageDelta struct {
	StopReason   string  `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
}

type DeltaUsage struct {
	OutputTokens int `json:"output_tokens"`
}

type MessageStopEvent struct {
	Type string `json:"type"`
}

type StreamErrorEvent struct {
	Type  string         `json:"type"`
	Error StreamErrBody  `json:"error"`
}

type StreamErrBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// --- Helpers ---

// ParseMessageContent parses Anthropic message content which can be a string
// or an array of ContentBlock.
func ParseMessageContent(raw json.RawMessage) []ContentBlock {
	if raw == nil {
		return nil
	}
	// Try as string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []ContentBlock{{Type: "text", Text: s}}
	}
	// Parse as array
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// ParseSystemPrompt extracts the system prompt text from the System field,
// which can be a string or an array of {type, text} blocks.
func ParseSystemPrompt(raw json.RawMessage) string {
	if raw == nil || len(raw) == 0 {
		return ""
	}
	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try as array of blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var result string
		for _, b := range blocks {
			if b.Type == "text" {
				if result != "" {
					result += "\n"
				}
				result += b.Text
			}
		}
		return result
	}
	return ""
}


