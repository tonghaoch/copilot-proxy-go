package handler

import "strings"

// normalizeResponsesToolDescriptions fills blank client-tool descriptions.
func normalizeResponsesToolDescriptions(payload map[string]any) {
	if tools, ok := payload["tools"].([]any); ok {
		normalizeToolListDescriptions(tools)
	}

	input, ok := payload["input"].([]any)
	if !ok {
		return
	}
	for _, itemAny := range input {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if tools, ok := item["tools"].([]any); ok {
			normalizeToolListDescriptions(tools)
		}
	}
}

func normalizeToolListDescriptions(tools []any) {
	for _, toolAny := range tools {
		tool, ok := toolAny.(map[string]any)
		if !ok {
			continue
		}

		toolType, _ := tool["type"].(string)
		if toolType == "function" || toolType == "custom" || toolType == "namespace" {
			description, _ := tool["description"].(string)
			if strings.TrimSpace(description) == "" {
				tool["description"] = fallbackToolDescription(toolType, tool)
			}
		}

		if nested, ok := tool["tools"].([]any); ok {
			normalizeToolListDescriptions(nested)
		}
	}
}

func fallbackToolDescription(toolType string, tool map[string]any) string {
	name, _ := tool["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "Tool provided by the client."
	}
	if toolType == "namespace" {
		return "Tools in the " + name + " namespace."
	}
	return "Invoke the " + name + " tool."
}

func convertApplyPatchTools(tools []any) []any {
	result := make([]any, 0, len(tools))
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			result = append(result, t)
			continue
		}
		toolType, _ := tool["type"].(string)
		toolName, _ := tool["name"].(string)
		if toolType == "custom" && toolName == "apply_patch" {
			result = append(result, map[string]any{
				"type": "function", "name": "apply_patch", "description": tool["description"],
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{"input": map[string]string{
						"type": "string", "description": "The entire contents of the apply_patch command",
					}},
					"required": []string{"input"},
				},
				"strict": false,
			})
		} else {
			result = append(result, t)
		}
	}
	return result
}

func convertLocalShellTools(tools []any) []any {
	result := make([]any, 0, len(tools))
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			result = append(result, t)
			continue
		}
		if toolType, _ := tool["type"].(string); toolType != "local_shell" {
			result = append(result, t)
			continue
		}
		result = append(result, map[string]any{
			"type": "function", "name": "local_shell", "description": "Run a shell command locally.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":    map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Command and arguments to execute."},
					"workdir":    map[string]string{"type": "string", "description": "Working directory for the command."},
					"timeout_ms": map[string]string{"type": "integer", "description": "Maximum execution time in milliseconds."},
				},
				"required": []string{"command"}, "additionalProperties": false,
			},
			"strict": false,
		})
	}
	return result
}

func removeWebSearchTools(tools []any) []any {
	result := make([]any, 0, len(tools))
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			result = append(result, t)
			continue
		}
		if toolType, _ := tool["type"].(string); toolType != "web_search" {
			result = append(result, t)
		}
	}
	return result
}
