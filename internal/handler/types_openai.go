package handler

import "encoding/json"

// --- Chat Completions Request (what we send to Copilot) ---

type ChatCompletionRequest struct {
	Model           string             `json:"model"`
	Messages        []OpenAIMsg        `json:"messages"`
	MaxTokens       *int               `json:"max_tokens,omitempty"`
	Temperature     *float64           `json:"temperature,omitempty"`
	TopP            *float64           `json:"top_p,omitempty"`
	Stream          bool               `json:"stream"`
	StreamOptions   *ChatStreamOptions `json:"stream_options,omitempty"`
	Tools           []OpenAITool       `json:"tools,omitempty"`
	ToolChoice      any                `json:"tool_choice,omitempty"`
	Stop            any                `json:"stop,omitempty"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
}

type ChatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type OpenAIMsg struct {
	Role            string           `json:"role"`
	Content         any              `json:"content"` // string or []OpenAIContentPart
	ToolCalls       []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID      string           `json:"tool_call_id,omitempty"`
	ReasoningText   *string          `json:"reasoning_text,omitempty"`
	ReasoningOpaque *string          `json:"reasoning_opaque,omitempty"`
}

type OpenAIContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *OpenAIImgURL `json:"image_url,omitempty"`
}

type OpenAIImgURL struct {
	URL string `json:"url"`
}

type OpenAITool struct {
	Type     string         `json:"type"` // "function"
	Function OpenAIFunction `json:"function"`
}

type OpenAIFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // "function"
	Function OpenAIToolCallFunc `json:"function"`
}

type OpenAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// --- Chat Completions Response ---

type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   *ChatCompletionUsage   `json:"usage,omitempty"`
}

type ChatCompletionChoice struct {
	Index        int             `json:"index"`
	Message      ChatCompletionM `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type ChatCompletionM struct {
	Role            string           `json:"role"`
	Content         *string          `json:"content"`
	ToolCalls       []OpenAIToolCall `json:"tool_calls,omitempty"`
	ReasoningText   *string          `json:"reasoning_text,omitempty"`
	ReasoningOpaque *string          `json:"reasoning_opaque,omitempty"`
}

type ChatCompletionUsage struct {
	PromptTokens        int                  `json:"prompt_tokens"`
	CompletionTokens    int                  `json:"completion_tokens"`
	TotalTokens         int                  `json:"total_tokens,omitempty"`
	PromptTokensDetails *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	inputKnown          bool
	outputKnown         bool
}

func (u *ChatCompletionUsage) UnmarshalJSON(data []byte) error {
	type wire ChatCompletionUsage
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*u = ChatCompletionUsage(value)
	u.inputKnown, u.outputKnown = fields.PromptTokens != nil, fields.CompletionTokens != nil
	return nil
}

// toAnthropic converts to Anthropic convention, where input_tokens excludes
// cached tokens. Gemini reports reasoning outside completion_tokens (a
// top-level reasoning_tokens) but counts it in total_tokens, so output is
// taken from total-prompt when that is larger.
func (u *ChatCompletionUsage) toAnthropic() AnthropicUsage {
	cached := 0
	writes := 0
	if u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
		if u.PromptTokensDetails.CacheWriteTokens != nil {
			writes = *u.PromptTokensDetails.CacheWriteTokens
		}
	}
	return AnthropicUsage{
		InputTokens:              u.PromptTokens - cached - writes,
		OutputTokens:             max(u.CompletionTokens, u.TotalTokens-u.PromptTokens),
		CacheReadInputTokens:     cached,
		CacheCreationInputTokens: writes,
	}
}

type PromptTokensDetails struct {
	CachedTokens     int  `json:"cached_tokens"`
	CacheWriteTokens *int `json:"cache_write_tokens,omitempty"`
}

// --- Chat Completions Streaming Chunk ---

type ChatCompletionChunk struct {
	ID      string                      `json:"id"`
	Object  string                      `json:"object"`
	Created int64                       `json:"created"`
	Model   string                      `json:"model"`
	Choices []ChatCompletionChunkChoice `json:"choices"`
	Usage   *ChatCompletionUsage        `json:"usage,omitempty"`
}

type ChatCompletionChunkChoice struct {
	Index        int                      `json:"index"`
	Delta        ChatCompletionChunkDelta `json:"delta"`
	FinishReason *string                  `json:"finish_reason"`
}

type ChatCompletionChunkDelta struct {
	Role            string          `json:"role,omitempty"`
	Content         *string         `json:"content,omitempty"`
	ToolCalls       []ToolCallDelta `json:"tool_calls,omitempty"`
	ReasoningText   *string         `json:"reasoning_text,omitempty"`
	ReasoningOpaque *string         `json:"reasoning_opaque,omitempty"`
}

type ToolCallDelta struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function *ToolCallFuncDelta `json:"function,omitempty"`
}

type ToolCallFuncDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}
