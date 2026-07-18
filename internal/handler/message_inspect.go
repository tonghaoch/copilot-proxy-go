package handler

import (
	"regexp"
	"strings"
)

var (
	claudeSonnet4Re = regexp.MustCompile(`^claude-sonnet-4-.*`)
	claudeOpus4Re   = regexp.MustCompile(`^claude-opus-4-.*`)
)

func isClaude(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

func normalizeModelName(model string) string {
	if !isClaude(model) {
		return model
	}
	if claudeSonnet4Re.MatchString(model) {
		return "claude-sonnet-4"
	}
	if claudeOpus4Re.MatchString(model) {
		return "claude-opus-4"
	}
	parts := strings.Split(model, "-")
	var result []string
	for _, p := range parts {
		if len(p) >= 8 && isAllDigits(p) {
			continue
		}
		result = append(result, p)
	}
	return strings.Join(result, "-")
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func hasVision(messages []AnthropicMsg) bool {
	for _, msg := range messages {
		for _, block := range ParseMessageContent(msg.Content) {
			if block.Type == "image" {
				return true
			}
		}
	}
	return false
}

func mapStopReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	default:
		return "end_turn"
	}
}
