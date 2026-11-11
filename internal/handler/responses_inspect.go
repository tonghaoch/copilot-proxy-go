package handler

func detectVisionInResponses(payload map[string]any) bool {
	input, ok := payload["input"].([]any)
	return ok && containsImageRecursive(input)
}

func containsImageRecursive(items []any) bool {
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		if t == "input_image" {
			return true
		}
		if content, ok := m["content"].([]any); ok && containsImageRecursive(content) {
			return true
		}
		if t == "function_call_output" || t == "custom_tool_call_output" {
			if output, ok := m["output"].([]any); ok && containsImageRecursive(output) {
				return true
			}
		}
	}
	return false
}

func detectAgentInResponses(payload map[string]any) bool {
	input, ok := payload["input"].([]any)
	if !ok || len(input) == 0 {
		return false
	}
	last, ok := input[len(input)-1].(map[string]any)
	if !ok {
		return false
	}
	role, _ := last["role"].(string)
	return role == "assistant" || role == ""
}
