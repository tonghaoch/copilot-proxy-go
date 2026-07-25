package handler

import (
	"encoding/json"
	"testing"
)

// blockAt returns the nth content block of the first message as a map.
func blockAt(t *testing.T, payload map[string]any, index int) map[string]any {
	t.Helper()
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages missing or wrong type: %T", payload["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("message 0 wrong type: %T", messages[0])
	}
	blocks, ok := message["content"].([]any)
	if !ok {
		t.Fatalf("content is not a block array: %T", message["content"])
	}
	if index >= len(blocks) {
		t.Fatalf("want block %d, content only has %d", index, len(blocks))
	}
	block, ok := blocks[index].(map[string]any)
	if !ok {
		t.Fatalf("block %d wrong type: %T", index, blocks[index])
	}
	return block
}

func TestFilterThinkingBlocksPreservesCacheControl(t *testing.T) {
	// Claude Code marks prompt-cache breakpoints with cache_control. Dropping
	// them costs cache hits on every subsequent turn of a long session.
	raw := `{"model":"claude-opus-4.8","messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"done","cache_control":{"type":"ephemeral"}}
		]}
	]}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	filterThinkingBlocksInMap(payload)

	block := blockAt(t, payload, 0)
	cacheControl, ok := block["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("cache_control was dropped; block = %v", block)
	}
	if cacheControl["type"] != "ephemeral" {
		t.Errorf("cache_control.type = %v, want ephemeral", cacheControl["type"])
	}
}

func TestFilterThinkingBlocksPreservesRedactedThinkingData(t *testing.T) {
	// redacted_thinking carries an opaque data payload. Stripping it leaves an
	// empty block that upstream rejects.
	raw := `{"model":"claude-opus-4.8","messages":[
		{"role":"assistant","content":[
			{"type":"redacted_thinking","data":"ENCRYPTED_BLOB"},
			{"type":"text","text":"ok"}
		]}
	]}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	filterThinkingBlocksInMap(payload)

	block := blockAt(t, payload, 0)
	if block["type"] != "redacted_thinking" {
		t.Fatalf("block 0 type = %v, want redacted_thinking", block["type"])
	}
	if block["data"] != "ENCRYPTED_BLOB" {
		t.Errorf("redacted_thinking data = %v, want ENCRYPTED_BLOB", block["data"])
	}
}

func TestFilterThinkingBlocksPreservesUnknownFields(t *testing.T) {
	// Anything Anthropic adds that we do not model must still reach upstream.
	raw := `{"model":"claude-opus-4.8","messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"hi","some_future_field":{"nested":true}}
		]}
	]}`
	var payload map[string]any
	json.Unmarshal([]byte(raw), &payload)

	filterThinkingBlocksInMap(payload)

	if _, ok := blockAt(t, payload, 0)["some_future_field"]; !ok {
		t.Errorf("unknown field was dropped; block = %v", blockAt(t, payload, 0))
	}
}

func TestFilterThinkingBlocksStillDropsUnusableThinking(t *testing.T) {
	tests := []struct {
		name  string
		block string
	}{
		{"placeholder text", `{"type":"thinking","thinking":"Thinking...","signature":"sig"}`},
		{"empty text", `{"type":"thinking","thinking":"","signature":"sig"}`},
		{"missing signature", `{"type":"thinking","thinking":"real reasoning"}`},
		{"responses-encoded signature", `{"type":"thinking","thinking":"real","signature":"blob@item_1"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"messages":[{"role":"assistant","content":[` + tt.block + `,{"type":"text","text":"ok"}]}]}`
			var payload map[string]any
			json.Unmarshal([]byte(raw), &payload)

			filterThinkingBlocksInMap(payload)

			block := blockAt(t, payload, 0)
			if block["type"] == "thinking" {
				t.Errorf("unusable thinking block survived: %v", block)
			}
		})
	}
}

func TestFilterThinkingBlocksKeepsValidThinking(t *testing.T) {
	raw := `{"messages":[{"role":"assistant","content":[
		{"type":"thinking","thinking":"genuine reasoning","signature":"validsig"}
	]}]}`
	var payload map[string]any
	json.Unmarshal([]byte(raw), &payload)

	filterThinkingBlocksInMap(payload)

	if block := blockAt(t, payload, 0); block["type"] != "thinking" {
		t.Errorf("valid thinking block was dropped; got %v", block)
	}
}

func TestFilterThinkingBlocksLeavesStringContentAlone(t *testing.T) {
	raw := `{"messages":[{"role":"assistant","content":"plain string"}]}`
	var payload map[string]any
	json.Unmarshal([]byte(raw), &payload)

	filterThinkingBlocksInMap(payload)

	messages := payload["messages"].([]any)
	message := messages[0].(map[string]any)
	if message["content"] != "plain string" {
		t.Errorf("string content = %v, want it untouched", message["content"])
	}
}

func TestFilterThinkingBlocksSubstitutesEmptyContent(t *testing.T) {
	// Every block filtered out — upstream still needs a non-empty content array.
	raw := `{"messages":[{"role":"assistant","content":[
		{"type":"thinking","thinking":"Thinking...","signature":""}
	]}]}`
	var payload map[string]any
	json.Unmarshal([]byte(raw), &payload)

	filterThinkingBlocksInMap(payload)

	block := blockAt(t, payload, 0)
	if block["type"] != "text" {
		t.Errorf("want a placeholder text block, got %v", block)
	}
}

func TestFilterThinkingBlocksIgnoresUserMessages(t *testing.T) {
	// Thinking blocks only need filtering on assistant turns.
	raw := `{"messages":[{"role":"user","content":[
		{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}
	]}]}`
	var payload map[string]any
	json.Unmarshal([]byte(raw), &payload)

	filterThinkingBlocksInMap(payload)

	if _, ok := blockAt(t, payload, 0)["cache_control"]; !ok {
		t.Errorf("user message was altered; block = %v", blockAt(t, payload, 0))
	}
}
