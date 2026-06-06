package handler

import (
	"encoding/json"
	"testing"
)

func TestStripUnsupportedToolsInMap(t *testing.T) {
	raw := `{
		"model": "claude-opus-4-8",
		"tools": [
			{"type": "image_generation_v1", "name": "image_generation"},
			{"name": "Read", "input_schema": {"type": "object"}}
		]
	}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	stripUnsupportedToolsInMap(payload)

	tools, ok := payload["tools"].([]any)
	if !ok {
		t.Fatalf("tools missing or wrong type: %T", payload["tools"])
	}
	if len(tools) != 1 {
		t.Fatalf("want 1 surviving tool, got %d", len(tools))
	}
	if name, _ := tools[0].(map[string]any)["name"].(string); name != "Read" {
		t.Fatalf("want surviving tool Read, got %q", name)
	}
}

func TestStripUnsupportedToolsInMap_RemovesEmptyArray(t *testing.T) {
	raw := `{"tools": [{"name": "image_generation"}]}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	stripUnsupportedToolsInMap(payload)

	if _, present := payload["tools"]; present {
		t.Fatalf("expected tools key removed when no tools survive")
	}
}

func TestFilterUnsupportedTools(t *testing.T) {
	req := &AnthropicRequest{
		Tools: []AnthropicTool{
			{Name: "image_generation"},
			{Name: "Read"},
			{Name: "Bash"},
		},
	}

	filterUnsupportedTools(req)

	if len(req.Tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(req.Tools))
	}
	for _, tool := range req.Tools {
		if tool.Name == "image_generation" {
			t.Fatalf("image_generation should have been filtered out")
		}
	}
}
