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
		if t, _ := m["type"].(string); t == "input_image" {
			return true
		}
		if content, ok := m["content"].([]any); ok && containsImageRecursive(content) {
			return true
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
