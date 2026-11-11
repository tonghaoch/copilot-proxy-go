package handler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

// responsesToAnthropic rewrites a Responses API request (what Codex sends) as
// an Anthropic Messages request, so the Messages router can pick a backend for
// any model. Tool mappings restore custom types and namespaces in the reply.
func responsesToAnthropic(payload map[string]any, model string) (*AnthropicRequest, responseTools, error) {
	req := &AnthropicRequest{
		Model:     model,
		Stream:    rsBool(payload["stream"]),
		MaxTokens: rsInt(payload["max_output_tokens"]),
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = fallbackMaxOutputTokens
	}
	if t, ok := payload["temperature"].(float64); ok {
		req.Temperature = &t
	}
	if p, ok := payload["top_p"].(float64); ok {
		req.TopP = &p
	}
	if reasoning, ok := payload["reasoning"].(map[string]any); ok {
		if effort := rsStr(reasoning["effort"]); effort != "" {
			req.chatReasoningEffort = effort
			if effort == "none" {
				req.Thinking = &ThinkingConfig{Type: "disabled"}
			} else {
				req.OutputConfig = &OutputConfig{Effort: rsEffort(effort)}
			}
		}
	}

	c := &responsesConverter{tools: responseTools{}}
	if s := rsStr(payload["instructions"]); s != "" {
		c.system = append(c.system, s)
	}
	switch input := payload["input"].(type) {
	case string:
		c.add("user", ContentBlock{Type: "text", Text: input})
	case []any:
		for _, raw := range input {
			if item, ok := raw.(map[string]any); ok {
				if err := c.item(item); err != nil {
					return nil, nil, err
				}
			}
		}
	}

	// Anthropic models reject a conversation that ends with an assistant turn
	// (prefill). Codex sends that shape after each reply, so close it with a user turn.
	if n := len(c.turns); n > 0 && c.turns[n-1].role == "assistant" {
		c.add("user", ContentBlock{Type: "text", Text: "Continue."})
	}
	msgs, err := c.messages()
	if err != nil {
		return nil, nil, err
	}
	req.Messages = msgs
	if len(c.system) > 0 {
		system, err := json.Marshal(strings.Join(c.system, "\n\n"))
		if err != nil {
			return nil, nil, err
		}
		req.System = system
	}

	if tools, ok := payload["tools"].([]any); ok {
		if err := c.addTools(req, tools, ""); err != nil {
			return nil, nil, err
		}
	}
	if input, ok := payload["input"].([]any); ok {
		for _, raw := range input {
			item, ok := raw.(map[string]any)
			if !ok || rsStr(item["type"]) != "additional_tools" {
				continue
			}
			if tools, ok := item["tools"].([]any); ok {
				if err := c.addTools(req, tools, ""); err != nil {
					return nil, nil, err
				}
			}
		}
	}

	if choice := rsToolChoice(payload["tool_choice"]); choice != nil {
		raw, err := json.Marshal(choice)
		if err != nil {
			return nil, nil, err
		}
		req.ToolChoice = raw
	}
	return req, c.tools, nil
}

type responseTool struct {
	name      string
	namespace string
	custom    bool
}

type responseTools map[string]responseTool

func (tools responseTools) lookup(name string) responseTool {
	if tool, ok := tools[name]; ok {
		return tool
	}
	return responseTool{name: name}
}

// Namespaces need a stable, Anthropic-compatible name for history replay.
func rsToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	sum := sha256.Sum256([]byte(namespace + "\x00" + name))
	return fmt.Sprintf("ns_%x", sum[:16])
}

func (c *responsesConverter) addTools(req *AnthropicRequest, tools []any, namespace string) error {
	for _, raw := range convertLocalShellTools(tools) {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind := rsStr(tool["type"])
		name := rsStr(tool["name"])
		if kind == "namespace" {
			if name == "" || namespace != "" {
				return api.InvalidRequest("invalid tool namespace", nil)
			}
			nested, _ := tool["tools"].([]any)
			if err := c.addTools(req, nested, name); err != nil {
				return err
			}
			continue
		}
		if kind != "function" && kind != "custom" {
			continue
		}
		if name == "" {
			return api.InvalidRequest("tool name is required", nil)
		}
		alias := rsToolName(namespace, name)
		info := responseTool{name: name, namespace: namespace, custom: kind == "custom"}
		if existing, ok := c.tools[alias]; ok {
			if existing != info {
				return api.InvalidRequest("tool names collide after translation", nil)
			}
			continue
		}
		c.tools[alias] = info
		schema := rsSchema(tool["parameters"])
		if info.custom {
			schema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)
		}
		description := rsStr(tool["description"])
		if namespace != "" {
			description = fmt.Sprintf("Tool %s.%s. %s", namespace, name, description)
		}
		req.Tools = append(req.Tools, AnthropicTool{
			Name:        alias,
			Description: description,
			InputSchema: schema,
		})
	}
	return nil
}

// rsEffort maps Responses reasoning effort onto Anthropic's vocabulary.
func rsEffort(effort string) string {
	if effort == "minimal" {
		return "low"
	}
	return effort
}

// rsToolChoice maps Responses tool_choice onto Anthropic's tool_choice object.
// "auto" is Anthropic's default, so it returns nil.
func rsToolChoice(raw any) map[string]any {
	switch choice := raw.(type) {
	case string:
		switch choice {
		case "required":
			return map[string]any{"type": "any"}
		case "none":
			return map[string]any{"type": "none"}
		}
	case map[string]any:
		if rsStr(choice["type"]) == "function" || rsStr(choice["type"]) == "custom" {
			return map[string]any{"type": "tool", "name": rsToolName(rsStr(choice["namespace"]), rsStr(choice["name"]))}
		}
		if rsStr(choice["type"]) == "local_shell" {
			return map[string]any{"type": "tool", "name": "local_shell"}
		}
	}
	return nil
}

// responsesConverter accumulates Anthropic turns from Responses input items.
// Consecutive items with the same role share one turn, because Anthropic
// expects alternating roles.
type responsesConverter struct {
	system []string
	turns  []rsTurn
	tools  responseTools
}

type rsTurn struct {
	role   string
	blocks []ContentBlock
}

func (c *responsesConverter) add(role string, block ContentBlock) {
	if n := len(c.turns); n == 0 || c.turns[n-1].role != role {
		c.turns = append(c.turns, rsTurn{role: role})
	}
	last := &c.turns[len(c.turns)-1]
	if block.Type == "tool_result" && role == "user" {
		// Anthropic wants tool_result blocks ahead of any text in the same turn.
		at := 0
		for at < len(last.blocks) && last.blocks[at].Type == "tool_result" {
			at++
		}
		last.blocks = append(last.blocks, ContentBlock{})
		copy(last.blocks[at+1:], last.blocks[at:])
		last.blocks[at] = block
		return
	}
	last.blocks = append(last.blocks, block)
}

func (c *responsesConverter) item(item map[string]any) error {
	switch rsStr(item["type"]) {
	case "", "message":
		role := rsStr(item["role"])
		if role == "system" || role == "developer" {
			for _, b := range rsContentBlocks(item["content"]) {
				if b.Type == "text" {
					c.system = append(c.system, b.Text)
				}
			}
			return nil
		}
		if role != "assistant" {
			role = "user"
		}
		for _, b := range rsContentBlocks(item["content"]) {
			c.add(role, b)
		}
	case "function_call":
		c.add("assistant", ContentBlock{
			Type:  "tool_use",
			ID:    rsStr(item["call_id"]),
			Name:  rsToolName(rsStr(item["namespace"]), rsStr(item["name"])),
			Input: rsArgs(rsStr(item["arguments"])),
		})
	case "custom_tool_call":
		input, _ := json.Marshal(map[string]string{"input": rsStr(item["input"])})
		c.add("assistant", ContentBlock{
			Type:  "tool_use",
			ID:    rsStr(item["call_id"]),
			Name:  rsToolName(rsStr(item["namespace"]), rsStr(item["name"])),
			Input: input,
		})
	case "function_call_output", "custom_tool_call_output":
		content, err := rsOutputContent(item["output"])
		if err != nil {
			return err
		}
		c.add("user", ContentBlock{
			Type:      "tool_result",
			ToolUseID: rsStr(item["call_id"]),
			Content:   content,
		})
	case "reasoning":
		// Anthropic only accepts thinking blocks with their signature, so unsigned reasoning is not replayed.
		sig := rsStr(item["encrypted_content"])
		if sig == "" {
			return nil
		}
		var parts []string
		if summary, ok := item["summary"].([]any); ok {
			for _, s := range summary {
				if part, ok := s.(map[string]any); ok {
					parts = append(parts, rsStr(part["text"]))
				}
			}
		}
		c.add("assistant", ContentBlock{Type: "thinking", Thinking: strings.Join(parts, "\n"), Signature: sig})
	}
	return nil
}

// messages marshals the accumulated turns into Anthropic message objects.
func (c *responsesConverter) messages() ([]AnthropicMsg, error) {
	msgs := make([]AnthropicMsg, 0, len(c.turns))
	for _, turn := range c.turns {
		raw, err := json.Marshal(turn.blocks)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, AnthropicMsg{Role: turn.role, Content: raw})
	}
	return msgs, nil
}

// rsContentBlocks converts Responses message content (a string or a list of
// typed parts) into Anthropic content blocks.
func rsContentBlocks(content any) []ContentBlock {
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []ContentBlock{{Type: "text", Text: v}}
	case []any:
		var blocks []ContentBlock
		for _, raw := range v {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch rsStr(part["type"]) {
			case "", "input_text", "output_text", "text":
				if text := rsStr(part["text"]); text != "" {
					blocks = append(blocks, ContentBlock{Type: "text", Text: text})
				}
			case "input_image":
				if src := rsImageSource(part["image_url"]); src != nil {
					blocks = append(blocks, ContentBlock{Type: "image", Source: src})
				}
			}
		}
		return blocks
	}
	return nil
}

// rsImageSource reads an image_url given either as a string or as {"url": ...}.
// Data URIs become base64 sources; anything else is passed as a URL.
func rsImageSource(image any) *ImageSource {
	url := rsStr(image)
	if m, ok := image.(map[string]any); ok {
		url = rsStr(m["url"])
	}
	if url == "" {
		return nil
	}
	if rest, ok := strings.CutPrefix(url, "data:"); ok {
		mediaType, data, found := strings.Cut(rest, ";base64,")
		if found {
			return &ImageSource{Type: "base64", MediaType: mediaType, Data: data}
		}
	}
	return &ImageSource{Type: "url", URL: url}
}

// rsOutputContent preserves text and image parts in a tool result.
func rsOutputContent(output any) (json.RawMessage, error) {
	switch v := output.(type) {
	case string:
		return json.Marshal(v)
	case []any:
		for _, raw := range v {
			part, ok := raw.(map[string]any)
			if !ok {
				return nil, api.InvalidRequest("unsupported tool output content", nil)
			}
			switch rsStr(part["type"]) {
			case "", "input_text", "output_text", "text":
			case "input_image":
				if rsImageSource(part["image_url"]) == nil {
					return nil, api.InvalidRequest("tool output images require an image_url on translated backends", nil)
				}
			default:
				return nil, api.InvalidRequest("unsupported tool output content", nil)
			}
		}
		blocks := rsContentBlocks(v)
		if blocks == nil {
			blocks = []ContentBlock{}
		}
		return json.Marshal(blocks)
	}
	return nil, api.InvalidRequest("tool output must be text or supported content parts", nil)
}

// rsArgs returns tool arguments as a JSON object, since tool_use input must be one.
func rsArgs(args string) json.RawMessage {
	if strings.HasPrefix(strings.TrimSpace(args), "{") && json.Valid([]byte(args)) {
		return json.RawMessage(args)
	}
	return json.RawMessage("{}")
}

// rsSchema returns a tool's JSON schema, defaulting to an empty object schema.
func rsSchema(params any) json.RawMessage {
	if params != nil {
		if raw, err := json.Marshal(params); err == nil {
			return raw
		}
	}
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func rsStr(v any) string {
	s, _ := v.(string)
	return s
}

func rsInt(v any) int {
	n, _ := v.(float64)
	return int(n)
}

func rsBool(v any) bool {
	b, _ := v.(bool)
	return b
}
