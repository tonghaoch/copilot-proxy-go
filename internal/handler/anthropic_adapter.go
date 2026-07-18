package handler

// AnthropicAdapter isolates protocol conversion from HTTP orchestration.
// Streaming state machines remain specialized, while request/response object
// conversion can be replaced and contract-tested independently.
type AnthropicAdapter interface {
	ToChat(*AnthropicRequest, string) (*ChatCompletionRequest, error)
	ToResponses(*AnthropicRequest, string) (*ResponsesPayload, error)
	FromChat(*ChatCompletionResponse) *AnthropicResponse
	FromResponses(*ResponsesResult) *AnthropicResponse
}

type defaultAnthropicAdapter struct {
	models ModelStore
	config RuntimeConfig
}

func (a defaultAnthropicAdapter) ToChat(req *AnthropicRequest, extraPrompt string) (*ChatCompletionRequest, error) {
	return translateToOpenAIWithModels(req, extraPrompt, a.models)
}

func (a defaultAnthropicAdapter) ToResponses(req *AnthropicRequest, extraPrompt string) (*ResponsesPayload, error) {
	return translateToResponsesWithEffort(req, extraPrompt, a.config.ReasoningEffort(normalizeModelName(req.Model)))
}

func (defaultAnthropicAdapter) FromChat(resp *ChatCompletionResponse) *AnthropicResponse {
	return translateToAnthropic(resp)
}

func (defaultAnthropicAdapter) FromResponses(resp *ResponsesResult) *AnthropicResponse {
	return translateResponsesResultToAnthropic(resp)
}
