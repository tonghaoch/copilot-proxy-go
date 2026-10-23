package handler

import (
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func modelWithContext(id string, ctx int) state.Model {
	m := state.Model{ID: id}
	m.Capabilities.Limits.MaxContextWindowTokens = ctx
	return m
}

func TestToClaudeCodeName(t *testing.T) {
	tests := []struct {
		name string
		in   state.Model
		want string
	}{
		{"opus 4.8 with 1M gets [1m]", modelWithContext("claude-opus-4.8", 1000000), "claude-opus-4-8[1m]"},
		{"sonnet 4.5 at 200K stays plain", modelWithContext("claude-sonnet-4.5", 200000), "claude-sonnet-4-5"},
		{"haiku 4.5 at 200K stays plain", modelWithContext("claude-haiku-4.5", 200000), "claude-haiku-4-5"},
		{"reasoning suffix preserved, no 1m", modelWithContext("claude-opus-4.7-high", 200000), "claude-opus-4-7-high"},
		{"reasoning suffix preserved, with 1m", modelWithContext("claude-opus-4.7-high", 1000000), "claude-opus-4-7-high[1m]"},
		{"no minor version", modelWithContext("claude-sonnet-4", 200000), "claude-sonnet-4"},
		{"non-claude 1M context gets suffix", modelWithContext("gpt-5.5", 1050000), "gpt-5.5[1m]"},
		{"astra 1M context gets suffix", modelWithContext("gpt-6-astra", 1050000), "gpt-6-astra[1m]"},
		{"exactly 1M gets suffix", modelWithContext("gpt-6-luna", 1000000), "gpt-6-luna[1m]"},
		{"400K must not advertise 1M", modelWithContext("gpt-5.3-codex", 400000), "gpt-5.3-codex"},
		{"below 1M stays plain", modelWithContext("test-model", 999999), "test-model"},
		{"unknown context stays plain", modelWithContext("gpt-6-astra", 0), "gpt-6-astra"},
		{"claude below 1M stays plain", modelWithContext("claude-opus-4.8", 400000), "claude-opus-4-8"},
		{"haiku 5.5 at 200K gets [1m] from config override", modelWithContext("claude-haiku-5.5", 200000), "claude-haiku-5-5[1m]"},
		{"future claude family is dashed, not passed through", modelWithContext("claude-fable-5.1", 200000), "claude-fable-5-1"},
		{"future claude family at 1M gets [1m]", modelWithContext("claude-fable-5.1", 1000000), "claude-fable-5-1[1m]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToClaudeCodeName(tt.in); got != tt.want {
				t.Fatalf("ToClaudeCodeName(%q, ctx=%d) = %q, want %q",
					tt.in.ID, tt.in.Capabilities.Limits.MaxContextWindowTokens, got, tt.want)
			}
		})
	}
}

func TestResolveCopilotModel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"claude-opus-4-8", "claude-opus-4.8"},
		{"claude-opus-4-8[1m]", "claude-opus-4.8"}, // defensive strip
		{"claude-sonnet-4", "claude-sonnet-4"},
		{"claude-opus-4-7-high", "claude-opus-4.7-high"},
		{"gpt-5.5", "gpt-5.5"}, // non-claude unchanged
		{"gpt-6-astra[1m]", "gpt-6-astra"},
	}
	for _, tt := range tests {
		if got := ResolveCopilotModel(tt.in); got != tt.want {
			t.Fatalf("ResolveCopilotModel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
