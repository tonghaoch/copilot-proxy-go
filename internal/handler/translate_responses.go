package handler

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/config"
)

var (
	safetyIdentifierRe = regexp.MustCompile(`user_([^_]+)_account`)
	promptCacheKeyRe   = regexp.MustCompile(`_session_(.+)$`)
)

// translateToResponses converts an Anthropic request to a Responses API payload.
func translateToResponses(req *AnthropicRequest, extraPrompt string) (*ResponsesPayload, error) {
	model := normalizeModelName(req.Model)

	// Build input items from messages
	var input []ResponsesInput
	for _, msg := range req.Messages {
		blocks := ParseMessageContent(msg.Content)
		items := translateMsgToResponsesInput(msg.Role, blocks, model)
		input = append(input, items...)
	}

	// Instructions from system prompt (Responses API joins blocks with space,
	// and appends extraPrompt to the first block before joining — matching TS)
	instructions := parseSystemPromptForResponses(req.System, extraPrompt)

	// Max output tokens (minimum 12800)
	maxOutput := req.MaxTokens
	if maxOutput < 12800 {
		maxOutput = 12800
	}

	// Temperature forced to 1 for reasoning models
	temp := float64(1)

	// Reasoning config from config system
	reasoning := &ResponsesReasoning{
		Effort:  config.GetReasoningEffort(model),
		Summary: "detailed",
	}

	storeFalse := false
	parallelTrue := true

	payload := &ResponsesPayload{
		Model:             model,
		Input:             input,
		Instructions:      instructions,
		MaxOutputTokens:   maxOutput,
		Temperature:       &temp,
		Reasoning:         reasoning,
		Include:           []string{"reasoning.encrypted_content"},
		Store:             &storeFalse,
		ParallelToolCalls: &parallelTrue,
		Stream:            req.Stream,
		ServiceTier:       nil,
	}

	// Tools
	if len(req.Tools) > 0 {
		var tools []any
		for _, t := range req.Tools {
			var params any
			if t.InputSchema != nil {
				json.Unmarshal(t.InputSchema, &params)
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"name": t.Name,
				"description": t.Description,
				"parameters":  params,
			})
		}
		payload.Tools = tools
	}

	// Tool choice
	if req.ToolChoice != nil {
		payload.ToolChoice = translateToolChoice(req.ToolChoice)
	}

	// User ID parsing for safety_identifier and prompt_cache_key
	if req.Metadata != nil && req.Metadata.UserID != "" {
		parseUserIDIntoPayload(payload, req.Metadata.UserID)
	}

	return payload, nil
}

// translateMsgToResponsesInput converts Anthropic message blocks to Responses input items.
func translateMsgToResponsesInput(role string, blocks []ContentBlock, model string) []ResponsesInput {
	var items []ResponsesInput
	isCodex := strings.Contains(model, "codex")

	if role == "user" {
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

		// Tool results → function_call_output
		for _, tr := range toolResults {
			status := "completed"
			if tr.IsError != nil && *tr.IsError {
				status = "incomplete"
			}
			items = append(items, ResponsesInput{
				Type:   "function_call_output",
				CallID: tr.ToolUseID,
				Output: convertToolResultContentForResponses(tr.Content),
				Status: status,
			})
		}

		// Other content → message with user role
		if len(otherBlocks) > 0 {
			content := buildResponsesContent(otherBlocks)
			items = append(items, ResponsesInput{
				Type:    "message",
				Role:    "user",
				Content: content,
			})
		}
	} else if role == "assistant" {
		// Collect text, tool_use, and thinking blocks
		var textParts []string
		var hasToolUse bool

		for _, b := range blocks {
			switch b.Type {
			case "thinking":
				// Thinking blocks with @ in signature are Responses API reasoning items
				if strings.Contains(b.Signature, "@") {
					parts := strings.SplitN(b.Signature, "@", 2)
					item := ResponsesInput{
						Type:             "reasoning",
						ID:               parts[1],
						EncryptedContent: parts[0],
					}
					if b.Thinking != "" && b.Thinking != "Thinking..." {
						item.Summary = []SummaryItem{{Type: "summary_text", Text: b.Thinking}}
					}
					items = append(items, item)
				}
				// Other thinking blocks are skipped for Responses translation

			case "text":
				textParts = append(textParts, b.Text)

			case "tool_use":
				hasToolUse = true
				argsStr := "{}"
				if b.Input != nil {
					argsStr = string(b.Input)
				}
				items = append(items, ResponsesInput{
					Type:      "function_call",
					CallID:    b.ID,
					Name:      b.Name,
					Arguments: argsStr,
				})
			}
		}

		// Add text as a message (use output_text content type for assistant)
		if len(textParts) > 0 {
			text := strings.Join(textParts, "")
			var content any
			content = []map[string]string{{"type": "output_text", "text": text}}
			msgItem := ResponsesInput{
				Type:    "message",
				Role:    "assistant",
				Content: content,
			}
			// Codex phase assignment
			if isCodex && strings.Contains(model, "gpt-5.3-codex") {
				if hasToolUse {
					msgItem.Phase = "commentary"
				} else {
					msgItem.Phase = "final_answer"
				}
			}
			items = append(items, msgItem)
		}
	}

	return items
}

// buildResponsesContent builds content for a Responses input message.
func buildResponsesContent(blocks []ContentBlock) any {
	hasImages := false
	for _, b := range blocks {
		if b.Type == "image" {
			hasImages = true
			break
		}
	}

	if !hasImages {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	// Multimodal content
	var parts []any
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, map[string]string{"type": "input_text", "text": b.Text})
		case "image":
			if b.Source != nil {
				url := fmt.Sprintf("data:%s;base64,%s", b.Source.MediaType, b.Source.Data)
				parts = append(parts, map[string]any{
					"type":   "input_image",
					"url":    url,
					"detail": "auto",
				})
			}
		}
	}
	return parts
}

// parseSystemPromptForResponses builds the system instructions for the Responses API.
// It appends extraPrompt to the first block before joining with spaces (matching TS).
func parseSystemPromptForResponses(raw json.RawMessage, extraPrompt string) string {
	if raw == nil || len(raw) == 0 {
		return extraPrompt
	}
	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s + extraPrompt
	}
	// Try as array of blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for i, b := range blocks {
			if b.Type == "text" {
				if i == 0 {
					parts = append(parts, b.Text+extraPrompt)
				} else {
					parts = append(parts, b.Text)
				}
			}
		}
		result := strings.Join(parts, " ")
		if result != "" {
			return result
		}
	}
	return extraPrompt
}

// convertToolResultContentForResponses converts tool result content to the
// Responses API format. Preserves array structure (text + images) if content
// is an array; returns string as-is.
func convertToolResultContentForResponses(raw json.RawMessage) any {
	if raw == nil {
		return ""
	}
	// Try as string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try as array of content blocks
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var result []any
		for _, b := range blocks {
			switch b.Type {
			case "text":
				result = append(result, map[string]string{
					"type": "input_text",
					"text": b.Text,
				})
			case "image":
				if b.Source != nil {
					url := fmt.Sprintf("data:%s;base64,%s", b.Source.MediaType, b.Source.Data)
					result = append(result, map[string]any{
						"type":   "input_image",
						"url":    url,
						"detail": "auto",
					})
				}
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return string(raw)
}

// parseUserIDIntoPayload extracts safety_identifier and prompt_cache_key
// from the Anthropic metadata user_id field.
func parseUserIDIntoPayload(payload *ResponsesPayload, userID string) {
	// Format: "user_{id}_account_session_{key}"
	if m := safetyIdentifierRe.FindStringSubmatch(userID); len(m) > 1 {
		payload.SafetyIdentifier = m[1]
	}
	if m := promptCacheKeyRe.FindStringSubmatch(userID); len(m) > 1 {
		payload.PromptCacheKey = m[1]
	}
}

// translateResponsesResultToAnthropic converts a Responses API result to Anthropic format.
func translateResponsesResultToAnthropic(result *ResponsesResult) *AnthropicResponse {
	var content []ContentBlock

	for _, item := range result.Output {
		switch item.Type {
		case "reasoning":
			thinking := "Thinking..."
			if len(item.Summary) > 0 {
				var parts []string
				for _, s := range item.Summary {
					parts = append(parts, s.Text)
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
			content = append(content, ContentBlock{
				Type:      "thinking",
				Thinking:  thinking,
				Signature: sig,
			})

		case "function_call":
			input := parseToolInput(item.Arguments)
			content = append(content, ContentBlock{
				Type:  "tool_use",
				ID:    item.CallID,
				Name:  item.Name,
				Input: input,
			})

		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" && c.Text != "" {
					content = append(content, ContentBlock{
						Type: "text",
						Text: c.Text,
					})
				}
			}
		}
	}

	// Fallback to output_text if no content blocks
	if len(content) == 0 && result.OutputText != "" {
		content = append(content, ContentBlock{Type: "text", Text: result.OutputText})
	}
	if len(content) == 0 {
		content = append(content, ContentBlock{Type: "text", Text: ""})
	}

	// Stop reason
	stopReason := "end_turn"
	hasFuncCall := false
	for _, item := range result.Output {
		if item.Type == "function_call" {
			hasFuncCall = true
			break
		}
	}
	if result.Status == "completed" && hasFuncCall {
		stopReason = "tool_use"
	} else if result.Status == "incomplete" {
		if result.IncompleteDetails != nil && result.IncompleteDetails.Reason == "max_output_tokens" {
			stopReason = "max_tokens"
		}
	}

	// Usage
	usage := AnthropicUsage{}
	if result.Usage != nil {
		usage.InputTokens = result.Usage.InputTokens
		usage.OutputTokens = result.Usage.OutputTokens
		if result.Usage.InputTokensDetails != nil {
			usage.CacheReadInputTokens = result.Usage.InputTokensDetails.CachedTokens
			usage.InputTokens -= usage.CacheReadInputTokens
		}
	}

	return &AnthropicResponse{
		ID:         result.ID,
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      result.Model,
		StopReason: stopReason,
		Usage:      usage,
	}
}

// parseToolInput parses function call arguments JSON, with fallbacks.
func parseToolInput(args string) json.RawMessage {
	if args == "" {
		return json.RawMessage("{}")
	}
	// Validate JSON
	var parsed any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		// Wrap in raw_arguments
		wrapped, _ := json.Marshal(map[string]string{"raw_arguments": args})
		return wrapped
	}
	// Wrap arrays
	if _, isArr := parsed.([]any); isArr {
		wrapped, _ := json.Marshal(map[string]any{"arguments": parsed})
		return wrapped
	}
	return json.RawMessage(args)
}
