package handler

import (
	"encoding/json"
	"strings"
)

func getToolResultText(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
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

func visitCacheControls(payload map[string]any, visit func(map[string]any)) {
	visitBlock := func(v any) {
		if block, ok := v.(map[string]any); ok {
			if cc, ok := block["cache_control"].(map[string]any); ok {
				visit(cc)
			}
		}
	}
	visitBlock(payload)
	for _, field := range []string{"tools", "system"} {
		if blocks, ok := payload[field].([]any); ok {
			for _, block := range blocks {
				visitBlock(block)
			}
		}
	}
	if messages, ok := payload["messages"].([]any); ok {
		for _, raw := range messages {
			if message, ok := raw.(map[string]any); ok {
				if blocks, ok := message["content"].([]any); ok {
					for _, block := range blocks {
						visitBlock(block)
					}
				}
			}
		}
	}
}

func stripCacheControlScope(payload map[string]any) {
	visitCacheControls(payload, func(cc map[string]any) {
		delete(cc, "scope")
	})
}

// applyCacheTTL sets ttl on every cache_control breakpoint, unless the client
// already chose a ttl on any of them: Anthropic requires 1h breakpoints to
// precede 5m ones, so mixing our default into an explicit layout could
// produce an invalid request. Main agents and subagents both pass through
// here, so both keep their prefixes warm for an hour by default.
func applyCacheTTL(payload map[string]any, ttl string) {
	if ttl == "" {
		return
	}
	var breakpoints []map[string]any
	explicit := false
	visitCacheControls(payload, func(cc map[string]any) {
		if _, has := cc["ttl"]; has {
			explicit = true
		}
		breakpoints = append(breakpoints, cc)
	})
	if explicit {
		return
	}
	for _, cc := range breakpoints {
		cc["ttl"] = ttl
	}
}

var unsupportedTools = map[string]bool{
	"image_generation": true,
	"web_search":       true,
	"web_fetch":        true,
	"code_execution":   true,
}

func stripUnsupportedToolsInMap(payload map[string]any) {
	rawTools, ok := payload["tools"].([]any)
	if !ok {
		return
	}
	filtered := make([]any, 0, len(rawTools))
	for _, t := range rawTools {
		if tm, ok := t.(map[string]any); ok {
			if name, _ := tm["name"].(string); unsupportedTools[name] {
				continue
			}
		}
		filtered = append(filtered, t)
	}
	if len(filtered) == len(rawTools) {
		return
	}
	if len(filtered) == 0 {
		delete(payload, "tools")
	} else {
		payload["tools"] = filtered
	}
}

func filterUnsupportedTools(req *AnthropicRequest) {
	if len(req.Tools) == 0 {
		return
	}
	filtered := req.Tools[:0]
	for _, t := range req.Tools {
		if !unsupportedTools[t.Name] {
			filtered = append(filtered, t)
		}
	}
	req.Tools = filtered
}
