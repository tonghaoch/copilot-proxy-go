package handler

import "encoding/json"

// --- Responses API Request (what we send to Copilot) ---

type ResponsesPayload struct {
	Model             string              `json:"model"`
	Input             []ResponsesInput    `json:"input"`
	Instructions      string              `json:"instructions,omitempty"`
	MaxOutputTokens   int                 `json:"max_output_tokens,omitempty"`
	Temperature       *float64            `json:"temperature,omitempty"`
	Tools             []any               `json:"tools,omitempty"`
	ToolChoice        any                 `json:"tool_choice,omitempty"`
	Reasoning         *ResponsesReasoning `json:"reasoning,omitempty"`
	Include           []string            `json:"include,omitempty"`
	Store             *bool               `json:"store"`
	ParallelToolCalls *bool               `json:"parallel_tool_calls,omitempty"`
	Stream            bool                `json:"stream"`
	ServiceTier       any                 `json:"service_tier"`
}

type ResponsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// ResponsesInput is a polymorphic input item.
type ResponsesInput struct {
	Type             string          `json:"type"`
	Role             string          `json:"role,omitempty"`
	Content          any             `json:"content,omitempty"`
	CallID           string          `json:"call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        string          `json:"arguments,omitempty"`
	Output           string          `json:"output,omitempty"`
	Status           string          `json:"status,omitempty"`
	ID               string          `json:"id,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Summary          []SummaryItem   `json:"summary,omitempty"`
	Phase            string          `json:"phase,omitempty"`
}

type SummaryItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- Responses API Result ---

type ResponsesResult struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	Model             string            `json:"model"`
	Output            []ResponsesOutput `json:"output"`
	OutputText        string            `json:"output_text"`
	Status            string            `json:"status"`
	Usage             *ResponsesUsage   `json:"usage,omitempty"`
	IncompleteDetails *IncompleteDetail `json:"incomplete_details,omitempty"`
}

type ResponsesOutput struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	Status           string          `json:"status,omitempty"`
	Role             string          `json:"role,omitempty"`
	Content          []OutputContent `json:"content,omitempty"`
	CallID           string          `json:"call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        string          `json:"arguments,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Summary          []SummaryItem   `json:"summary,omitempty"`
}

type OutputContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type ResponsesUsage struct {
	InputTokens        int                     `json:"input_tokens"`
	OutputTokens       int                     `json:"output_tokens"`
	InputTokensDetails *InputTokensDetails     `json:"input_tokens_details,omitempty"`
}

type InputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type IncompleteDetail struct {
	Reason string `json:"reason"`
}

// --- Responses Streaming Events ---

type ResponsesStreamEvent struct {
	Type     string          `json:"type"`
	Response json.RawMessage `json:"response,omitempty"`

	// output_item.added / output_item.done
	OutputIndex int             `json:"output_index,omitempty"`
	Item        json.RawMessage `json:"item,omitempty"`

	// text/function_call/reasoning deltas
	ItemID       string `json:"item_id,omitempty"`
	ContentIndex int    `json:"content_index,omitempty"`
	SummaryIndex int    `json:"summary_index,omitempty"`
	Delta        string `json:"delta,omitempty"`

	// error
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}
