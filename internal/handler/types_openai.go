package handler

// --- Chat Completions Request (what we send to Copilot) ---

type ChatCompletionRequest struct {
	Model       string         `json:"model"`
	Messages    []OpenAIMsg    `json:"messages"`
	MaxTokens   *int           `json:"max_tokens,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	TopP        *float64       `json:"top_p,omitempty"`
	Stream      bool           `json:"stream"`
	Tools       []OpenAITool   `json:"tools,omitempty"`
	ToolChoice  any            `json:"tool_choice,omitempty"`
	Stop        any            `json:"stop,omitempty"`
}

type OpenAIMsg struct {
	Role            string          `json:"role"`
	Content         any             `json:"content"`                    // string or []OpenAIContentPart
	ToolCalls       []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID      string          `json:"tool_call_id,omitempty"`
	ReasoningText   *string         `json:"reasoning_text,omitempty"`
	ReasoningOpaque *string         `json:"reasoning_opaque,omitempty"`
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
	Index        int              `json:"index"`
	Message      ChatCompletionM  `json:"message"`
	FinishReason string           `json:"finish_reason"`
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
	PromptTokensDetails *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
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
