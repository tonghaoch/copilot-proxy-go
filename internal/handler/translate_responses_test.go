package handler

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTranslateToResponsesImageContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []map[string]any
	}{
		{
			name:    "base64 image",
			content: `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]`,
			want: []map[string]any{
				{"type": "input_image", "image_url": "data:image/png;base64,aGVsbG8=", "detail": "auto"},
			},
		},
		{
			name:    "URL image",
			content: `[{"type":"image","source":{"type":"url","url":"https://example.com/image.png?size=large&format=png"}}]`,
			want: []map[string]any{
				{"type": "input_image", "image_url": "https://example.com/image.png?size=large&format=png", "detail": "auto"},
			},
		},
		{
			name:    "mixed text and images",
			content: `[{"type":"text","text":"before"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"aGVsbG8="}},{"type":"image","source":{"type":"url","url":"https://example.com/image.jpg"}},{"type":"text","text":"after"}]`,
			want: []map[string]any{
				{"type": "input_text", "text": "before"},
				{"type": "input_image", "image_url": "data:image/jpeg;base64,aGVsbG8=", "detail": "auto"},
				{"type": "input_image", "image_url": "https://example.com/image.jpg", "detail": "auto"},
				{"type": "input_text", "text": "after"},
			},
		},
		{
			name:    "missing image source is skipped",
			content: `[{"type":"text","text":"no image"},{"type":"image"}]`,
			want: []map[string]any{
				{"type": "input_text", "text": "no image"},
			},
		},
	}

	for _, tt := range tests {
		for _, kind := range []string{"message", "tool_result", "merged_tool_result"} {
			t.Run(tt.name+"/"+kind, func(t *testing.T) {
				content := tt.content
				var messages []AnthropicMsg
				want := append([]map[string]any(nil), tt.want...)
				if kind != "message" {
					messages = append(messages, AnthropicMsg{
						Role:    "assistant",
						Content: json.RawMessage(`[{"type":"tool_use","id":"toolu_image","name":"Read","input":{}}]`),
					})
					content = `[{"type":"tool_result","tool_use_id":"toolu_image","content":` + content + `}`
					if kind == "merged_tool_result" {
						content += `,{"type":"text","text":"tool note"}`
						want = append(want, map[string]any{"type": "input_text", "text": "tool note"})
					}
					content += `]`
				}
				messages = append(messages, AnthropicMsg{Role: "user", Content: json.RawMessage(content)})
				req := &AnthropicRequest{Model: "gpt-5.3-codex", MaxTokens: 256, Messages: messages}
				mergeToolResultBlocks(req)
				input := responsesInputJSON(t, req)
				if len(input) != len(messages) {
					t.Fatalf("input count = %d, want %d", len(input), len(messages))
				}
				item := input[len(input)-1]
				field := "content"
				if kind != "message" {
					field = "output"
					if string(item["type"]) != `"function_call_output"` || string(item["call_id"]) != `"toolu_image"` {
						t.Fatalf("unexpected tool result: %s", item)
					}
				} else if string(item["type"]) != `"message"` || string(item["role"]) != `"user"` {
					t.Fatalf("unexpected user message: %s", item)
				}
				var got []map[string]any
				if err := json.Unmarshal(item[field], &got); err != nil {
					t.Fatalf("decode %s: %v", field, err)
				}
				// Compare the serialized shape, including the absence of unsupported fields such as url.
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %s, want %#v", field, item[field], want)
				}
			})
		}
	}
}

func TestTranslateToResponsesTextContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		field   string
		want    any
	}{
		{
			name:    "message string",
			content: `"hello"`,
			field:   "content",
			want:    "hello",
		},
		{
			name:    "message text blocks",
			content: `[{"type":"text","text":"first"},{"type":"text","text":"second"}]`,
			field:   "content",
			want:    "first\nsecond",
		},
		{
			name:    "tool result string",
			content: `[{"type":"tool_result","tool_use_id":"toolu_text","content":"result"}]`,
			field:   "output",
			want:    "result",
		},
		{
			name:    "tool result text blocks",
			content: `[{"type":"tool_result","tool_use_id":"toolu_text","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}]`,
			field:   "output",
			want: []any{
				map[string]any{"type": "input_text", "text": "first"},
				map[string]any{"type": "input_text", "text": "second"},
			},
		},
		{
			name:    "empty tool result",
			content: `[{"type":"tool_result","tool_use_id":"toolu_text","content":""}]`,
			field:   "output",
			want:    "",
		},
		{
			name:    "missing tool result content",
			content: `[{"type":"tool_result","tool_use_id":"toolu_text"}]`,
			field:   "output",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &AnthropicRequest{
				Model:     "gpt-5.3-codex",
				MaxTokens: 256,
				Messages:  []AnthropicMsg{{Role: "user", Content: json.RawMessage(tt.content)}},
			}
			input := responsesInputJSON(t, req)
			if len(input) != 1 {
				t.Fatalf("input count = %d, want 1", len(input))
			}
			var got any
			if err := json.Unmarshal(input[0][tt.field], &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s = %#v, want %#v", tt.field, got, tt.want)
			}
		})
	}
}

func responsesInputJSON(t *testing.T, req *AnthropicRequest) []map[string]json.RawMessage {
	t.Helper()
	payload, err := translateToResponsesWithModels(req, "", "high", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Input
}
