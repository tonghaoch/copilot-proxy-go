package handler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var (
	claudeSonnet4Re = regexp.MustCompile(`^claude-sonnet-4-.*`)
	claudeOpus4Re   = regexp.MustCompile(`^claude-opus-4-.*`)
)

// initiatorStr returns "agent" or "user".
func initiatorStr(isAgent bool) string {
	if isAgent {
		return "agent"
	}
	return "user"
}

// isClaude returns true if the model name indicates a Claude model.
func isClaude(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

// normalizeModelName strips version suffixes from Claude model names.
// Uses specific regexes for known models, falls back to generic date stripping.
// e.g. "claude-sonnet-4-20250514" → "claude-sonnet-4"
func normalizeModelName(model string) string {
	if !isClaude(model) {
		return model
	}
	// Specific model patterns
	if claudeSonnet4Re.MatchString(model) {
		return "claude-sonnet-4"
	}
	if claudeOpus4Re.MatchString(model) {
		return "claude-opus-4"
	}
	// Generic fallback: strip date suffixes like -20250514
	parts := strings.Split(model, "-")
	var result []string
	for _, p := range parts {
		// Skip if it looks like a date (8+ digits)
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

// hasVision checks if any message content contains image blocks.
func hasVision(messages []AnthropicMsg) bool {
	for _, msg := range messages {
		blocks := ParseMessageContent(msg.Content)
		for _, b := range blocks {
			if b.Type == "image" {
				return true
			}
		}
	}
	return false
}

// mapStopReason maps OpenAI finish_reason to Anthropic stop_reason.
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

// SSE helpers

// writeSSE writes an Anthropic SSE event to the response writer.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, eventType string, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(jsonData))
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// writeSSEError writes an error event to the SSE stream.
func writeSSEError(w http.ResponseWriter, flusher http.Flusher, message string) {
	writeSSE(w, flusher, "error", StreamErrorEvent{
		Type: "error",
		Error: StreamErrBody{
			Type:    "api_error",
			Message: message,
		},
	})
}

// readSSE reads Server-Sent Events from a reader and calls the handler
// for each event. Works for both OpenAI format (data-only) and Responses
// format (event + data).
func readSSE(body io.Reader, handler func(eventType, data string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	var eventType string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return nil
			}
			if err := handler(eventType, data); err != nil {
				return err
			}
			eventType = "" // reset after handling
		}
	}
	return scanner.Err()
}

// getToolResultText extracts text content from a tool_result's Content field,
// which can be a string or an array of content blocks.
func getToolResultText(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	// Try string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try array of content blocks
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}
