package handler

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

const interleavedThinkingPrompt = `
<interleaved_thinking_protocol>
You MUST think after receiving a tool result. After EVERY tool result, you MUST produce a thinking block before producing any other content. This is NON-NEGOTIABLE.
</interleaved_thinking_protocol>`

const interleavedThinkingReminder = `<system-reminder>you MUST follow interleaved_thinking_protocol</system-reminder>`

// translateToOpenAI converts an Anthropic request to an OpenAI Chat Completions payload.
func translateToOpenAI(req *AnthropicRequest, extraPrompt string) (*ChatCompletionRequest, error) {
	model := normalizeModelName(req.Model)
	isClaudeModel := isClaude(model)

	// Build messages
	var messages []OpenAIMsg

	// System message
	systemText := ParseSystemPrompt(req.System)
	if extraPrompt != "" {
		if systemText != "" {
			systemText += "\n\n" + extraPrompt
		} else {
			systemText = extraPrompt
		}
	}

	// Interleaved thinking protocol for Claude models with thinking
	if isClaudeModel && req.Thinking != nil && req.Thinking.BudgetTokens > 0 {
		systemText += interleavedThinkingPrompt
	}

	if systemText != "" {
		messages = append(messages, OpenAIMsg{
			Role:    "system",
			Content: systemText,
		})
	}

	// Translate messages
	needThinkingReminder := isClaudeModel && req.Thinking != nil && req.Thinking.BudgetTokens > 0
	firstUserSeen := false

	for _, msg := range req.Messages {
		blocks := ParseMessageContent(msg.Content)

		if msg.Role == "user" {
			translated := translateUserMessage(blocks, needThinkingReminder && !firstUserSeen)
			messages = append(messages, translated...)
			firstUserSeen = true
		} else if msg.Role == "assistant" {
			translated := translateAssistantMessage(blocks, isClaudeModel)
			messages = append(messages, translated...)
		}
	}

	// Build request
	ccReq := &ChatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	// Max tokens
	maxTokens := req.MaxTokens
	ccReq.MaxTokens = &maxTokens

	// Thinking budget
	if req.Thinking != nil && req.Thinking.BudgetTokens > 0 {
		budget := clampThinkingBudget(req.Model, req.Thinking.BudgetTokens, req.MaxTokens)
		ccReq.MaxTokens = &budget
	}

	// Stop sequences
	if len(req.StopSequences) > 0 {
		ccReq.Stop = req.StopSequences
	}

	// Tools
	if len(req.Tools) > 0 {
		ccReq.Tools = translateTools(req.Tools)
	}

	// Tool choice
	if req.ToolChoice != nil {
		ccReq.ToolChoice = translateToolChoice(req.ToolChoice)
	}

	return ccReq, nil
}

// translateUserMessage translates Anthropic user message blocks to OpenAI messages.
func translateUserMessage(blocks []ContentBlock, addThinkingReminder bool) []OpenAIMsg {
	var msgs []OpenAIMsg

	// Separate tool results from other content
	var toolResults []ContentBlock
	var otherBlocks []ContentBlock
	for _, b := range blocks {
		if b.Type == "tool_result" {
			toolResults = append(toolResults, b)
		} else {
			otherBlocks = append(otherBlocks, b)
		}
	}

	// Tool results become separate "tool" role messages
	for _, tr := range toolResults {
		msgs = append(msgs, OpenAIMsg{
			Role:       "tool",
			Content:    getToolResultText(tr.Content),
			ToolCallID: tr.ToolUseID,
		})
	}

	// Other content becomes a user message
	if len(otherBlocks) > 0 {
		content := buildUserContent(otherBlocks, addThinkingReminder)
		msgs = append(msgs, OpenAIMsg{
			Role:    "user",
			Content: content,
		})
	} else if addThinkingReminder && len(toolResults) > 0 {
		// Inject thinking reminder even if there's only tool results
		msgs = append(msgs, OpenAIMsg{
			Role:    "user",
			Content: interleavedThinkingReminder,
		})
	}

	return msgs
}

// buildUserContent builds OpenAI content from non-tool-result blocks.
func buildUserContent(blocks []ContentBlock, addReminder bool) any {
	// Check if there are any images
	hasImages := false
	for _, b := range blocks {
		if b.Type == "image" {
			hasImages = true
			break
		}
	}

	if !hasImages {
		// Text-only: return as string
		var parts []string
		if addReminder {
			parts = append(parts, interleavedThinkingReminder)
		}
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	// Multimodal: return as array of content parts
	var parts []OpenAIContentPart
	if addReminder {
		parts = append(parts, OpenAIContentPart{Type: "text", Text: interleavedThinkingReminder})
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, OpenAIContentPart{Type: "text", Text: b.Text})
		case "image":
			if b.Source != nil {
				url := fmt.Sprintf("data:%s;base64,%s", b.Source.MediaType, b.Source.Data)
				parts = append(parts, OpenAIContentPart{
					Type:     "image_url",
					ImageURL: &OpenAIImgURL{URL: url},
				})
			}
		}
	}
	return parts
}

// translateAssistantMessage translates Anthropic assistant blocks to OpenAI messages.
func translateAssistantMessage(blocks []ContentBlock, isClaudeModel bool) []OpenAIMsg {
	msg := OpenAIMsg{Role: "assistant"}

	var textParts []string
	var toolCalls []OpenAIToolCall

	for _, b := range blocks {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)

		case "tool_use":
			argsStr := "{}"
			if b.Input != nil {
				argsStr = string(b.Input)
			}
			toolCalls = append(toolCalls, OpenAIToolCall{
				ID:   b.ID,
				Type: "function",
				Function: OpenAIToolCallFunc{
					Name:      b.Name,
					Arguments: argsStr,
				},
			})

		case "thinking":
			if isClaudeModel {
				// Filter out empty or placeholder thinking for Claude models
				if b.Thinking == "" || b.Thinking == "Thinking..." {
					continue
				}
				// Skip thinking blocks with @ in signature (from Responses API encoding)
				if strings.Contains(b.Signature, "@") {
					continue
				}
			}
			if b.Thinking != "" {
				msg.ReasoningText = &b.Thinking
			}
			if b.Signature != "" {
				msg.ReasoningOpaque = &b.Signature
			}
		}
	}

	if len(textParts) > 0 {
		text := strings.Join(textParts, "")
		msg.Content = text
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	return []OpenAIMsg{msg}
}

// translateTools converts Anthropic tools to OpenAI function tools.
func translateTools(tools []AnthropicTool) []OpenAITool {
	result := make([]OpenAITool, len(tools))
	for i, t := range tools {
		var params any
		if t.InputSchema != nil {
			json.Unmarshal(t.InputSchema, &params)
		}
		result[i] = OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		}
	}
	return result
}

// translateToolChoice converts Anthropic tool_choice to OpenAI tool_choice.
func translateToolChoice(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil
	}
	switch tc.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		return map[string]any{
			"type":     "function",
			"function": map[string]string{"name": tc.Name},
		}
	}
	return nil
}

// clampThinkingBudget clamps the thinking budget to model-supported bounds.
func clampThinkingBudget(modelID string, budget, maxTokens int) int {
	model := state.Global.FindModel(modelID)
	minBudget := 1024
	maxBudget := maxTokens - 1

	if model != nil {
		if model.Capabilities.Supports.MinThinkingBudget > 0 {
			minBudget = model.Capabilities.Supports.MinThinkingBudget
		}
		if model.Capabilities.Supports.MaxThinkingBudget > 0 {
			mb := model.Capabilities.Supports.MaxThinkingBudget
			if mb < maxBudget {
				maxBudget = mb
			}
		}
	}

	if budget < minBudget {
		budget = minBudget
	}
	if budget > maxBudget {
		budget = maxBudget
	}
	return budget
}

// translateToAnthropic converts an OpenAI Chat Completion response to an
// Anthropic response.
func translateToAnthropic(resp *ChatCompletionResponse) *AnthropicResponse {
	var content []ContentBlock

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		msg := choice.Message

		// Thinking/reasoning
		if msg.ReasoningText != nil && *msg.ReasoningText != "" {
			content = append(content, ContentBlock{
				Type:     "thinking",
				Thinking: *msg.ReasoningText,
			})
		} else if msg.ReasoningOpaque != nil && *msg.ReasoningOpaque != "" {
			content = append(content, ContentBlock{
				Type:      "thinking",
				Thinking:  "Thinking...",
				Signature: *msg.ReasoningOpaque,
			})
		}

		// Text content
		if msg.Content != nil && *msg.Content != "" {
			content = append(content, ContentBlock{
				Type: "text",
				Text: *msg.Content,
			})
		}

		// Tool calls
		for _, tc := range msg.ToolCalls {
			inputRaw := json.RawMessage(tc.Function.Arguments)
			content = append(content, ContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: inputRaw,
			})
		}
	}

	// Ensure at least one content block
	if len(content) == 0 {
		content = append(content, ContentBlock{Type: "text", Text: ""})
	}

	// Usage
	usage := AnthropicUsage{}
	stopReason := "end_turn"
	if resp.Usage != nil {
		usage.InputTokens = resp.Usage.PromptTokens
		usage.OutputTokens = resp.Usage.CompletionTokens
		if resp.Usage.PromptTokensDetails != nil {
			cached := resp.Usage.PromptTokensDetails.CachedTokens
			usage.CacheReadInputTokens = cached
			usage.InputTokens -= cached
		}
	}
	if len(resp.Choices) > 0 {
		stopReason = mapStopReason(resp.Choices[0].FinishReason)
	}

	return &AnthropicResponse{
		ID:       resp.ID,
		Type:     "message",
		Role:     "assistant",
		Content:  content,
		Model:    resp.Model,
		StopReason: stopReason,
		Usage:    usage,
	}
}
