package handler

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/config"
)

const compactPrefix = "You are a helpful AI assistant tasked with summarizing conversations"

// isCompactRequest detects Claude Code's "compact" requests that summarize conversations.
func isCompactRequest(req *AnthropicRequest) bool {
	systemText := ParseSystemPrompt(req.System)
	return strings.HasPrefix(systemText, compactPrefix)
}

// isWarmupRequest detects Claude Code warmup/probe requests.
// These have an anthropic-beta header but no tools.
func isWarmupRequest(req *AnthropicRequest, betaHeader string) bool {
	return betaHeader != "" && len(req.Tools) == 0
}

// applySmallModelIfNeeded checks for compact/warmup requests and routes them
// to the configured small model to save premium quota.
// Returns true if the model was changed.
func applySmallModelIfNeeded(req *AnthropicRequest, betaHeader string) bool {
	cfg := config.Get()

	if cfg.CompactUseSmallModel && isCompactRequest(req) {
		req.Model = cfg.SmallModel
		return true
	}

	if isWarmupRequest(req, betaHeader) && !isCompactRequest(req) {
		req.Model = cfg.SmallModel
		return true
	}

	return false
}

// mergeToolResultBlocks merges text blocks into tool_result blocks within
// user messages to avoid consuming premium requests on skill invocations,
// edit hooks, and plan/todo reminders.
func mergeToolResultBlocks(req *AnthropicRequest) {
	if isCompactRequest(req) {
		return // Skip for compact requests
	}

	for i := range req.Messages {
		if req.Messages[i].Role != "user" {
			continue
		}

		blocks := ParseMessageContent(req.Messages[i].Content)

		var toolResults []int
		var textBlocks []int
		for j, b := range blocks {
			switch b.Type {
			case "tool_result":
				toolResults = append(toolResults, j)
			case "text":
				textBlocks = append(textBlocks, j)
			}
		}

		if len(toolResults) == 0 || len(textBlocks) == 0 {
			continue
		}

		if len(toolResults) == len(textBlocks) {
			// Pairwise merge: each text into the corresponding tool_result
			for k := 0; k < len(toolResults); k++ {
				tri := toolResults[k]
				txi := textBlocks[k]
				mergeTextIntoToolResult(&blocks[tri], blocks[txi].Text)
			}
		} else {
			// Merge all text into the last tool_result
			lastTR := toolResults[len(toolResults)-1]
			var allText []string
			for _, txi := range textBlocks {
				if blocks[txi].Text != "" {
					allText = append(allText, blocks[txi].Text)
				}
			}
			if len(allText) > 0 {
				mergeTextIntoToolResult(&blocks[lastTR], strings.Join(allText, "\n"))
			}
		}

		// Remove text blocks (keep only non-text)
		var filtered []ContentBlock
		for _, b := range blocks {
			if b.Type != "text" {
				filtered = append(filtered, b)
			}
		}

		if len(filtered) > 0 {
			newContent, _ := json.Marshal(filtered)
			req.Messages[i].Content = newContent
		}
	}
}

// mergeTextIntoToolResult appends text to a tool_result's content.
func mergeTextIntoToolResult(tr *ContentBlock, text string) {
	existing := getToolResultText(tr.Content)
	if existing != "" {
		text = existing + "\n" + text
	}
	textJSON, _ := json.Marshal(text)
	tr.Content = textJSON
}

// SubagentInfo holds parsed subagent marker data.
type SubagentInfo struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

var systemReminderRe = regexp.MustCompile(`<system-reminder>([\s\S]*?)</system-reminder>`)

const subagentPrefix = "__SUBAGENT_MARKER__"

// detectSubagentMarker scans the first user message for a subagent marker
// in <system-reminder> tags. Returns non-nil if found.
func detectSubagentMarker(messages []AnthropicMsg) *SubagentInfo {
	// Find first user message
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}

		blocks := ParseMessageContent(msg.Content)
		for _, b := range blocks {
			if b.Type != "text" || b.Text == "" {
				continue
			}

			// Find all <system-reminder> blocks
			matches := systemReminderRe.FindAllStringSubmatch(b.Text, -1)
			for _, match := range matches {
				content := strings.TrimSpace(match[1])
				if !strings.HasPrefix(content, subagentPrefix) {
					continue
				}

				jsonStr := strings.TrimPrefix(content, subagentPrefix)
				jsonStr = strings.TrimSpace(jsonStr)

				var info SubagentInfo
				if err := json.Unmarshal([]byte(jsonStr), &info); err != nil {
					continue
				}

				if info.SessionID != "" && info.AgentID != "" && info.AgentType != "" {
					return &info
				}
			}
		}

		// Only check first user message
		break
	}

	return nil
}
