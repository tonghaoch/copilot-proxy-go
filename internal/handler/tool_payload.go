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

func stripCacheControlScope(v any) {
	switch val := v.(type) {
	case map[string]any:
		if cc, ok := val["cache_control"].(map[string]any); ok {
			delete(cc, "scope")
		}
		for _, child := range val {
			stripCacheControlScope(child)
		}
	case []any:
		for _, item := range val {
			stripCacheControlScope(item)
		}
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
