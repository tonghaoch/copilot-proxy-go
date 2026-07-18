package handler

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
