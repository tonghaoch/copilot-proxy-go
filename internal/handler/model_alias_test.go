package handler

import (
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func modelWithContext(id string, ctx int) state.Model {
	m := state.Model{ID: id}
	m.Capabilities.Limits.MaxContextWindowTokens = ctx
	m.Capabilities.Limits.MaxPromptTokens = ctx
	return m
}

func tieredModel(id string, def, long int) state.Model {
	m := modelWithContext(id, long)
	m.Billing.TokenPrices = map[string]state.PriceTier{
		"default":      {MaxPromptTokens: def},
		"long_context": {MaxPromptTokens: long},
	}
	return m
}

func TestModelFlavours(t *testing.T) {
	oneMillionClaude := tieredModel("claude-haiku-5.5", 100000, 936000)
	oneMillionClaude.Capabilities.Limits.MaxContextWindowTokens = 1000000
	tests := []struct {
		name string
		in   state.Model
		want []ModelFlavour
	}{
		{"tiered claude gets default and [1m]", oneMillionClaude, []ModelFlavour{
			{ID: "claude-haiku-5-5", ContextWindow: 100000},
			{ID: "claude-haiku-5-5[1m]", ContextWindow: 936000, Long: true},
		}},
		{"tiered claude below 1M does not claim 1M", tieredModel("claude-haiku-5.5", 100000, 400000), []ModelFlavour{
			{ID: "claude-haiku-5-5", ContextWindow: 100000},
			{ID: "claude-haiku-5-5[long]", ContextWindow: 400000, Long: true},
		}},
		{"tiered non-claude gets [long]", tieredModel("gpt-6-luna", 272000, 872000), []ModelFlavour{
			{ID: "gpt-6-luna", ContextWindow: 272000},
			{ID: "gpt-6-luna[long]", ContextWindow: 872000, Long: true},
		}},
		{"single-tier claude at 1M gets [1m]", modelWithContext("claude-opus-4.8", 1000000), []ModelFlavour{
			{ID: "claude-opus-4-8[1m]", ContextWindow: 1000000, Long: true},
		}},
		{"single-tier non-claude at 1M gets [1m]", modelWithContext("gpt-6-astra", 1050000), []ModelFlavour{
			{ID: "gpt-6-astra[1m]", ContextWindow: 1050000, Long: true},
		}},
		{"single-tier claude below 1M stays plain", modelWithContext("claude-opus-4.8", 400000), []ModelFlavour{
			{ID: "claude-opus-4-8", ContextWindow: 400000},
		}},
		{"single-tier non-claude below 1M stays plain", modelWithContext("test-model", 999999), []ModelFlavour{
			{ID: "test-model", ContextWindow: 999999},
		}},
		{"single-tier claude at 200K stays plain", modelWithContext("claude-sonnet-4.5", 200000), []ModelFlavour{
			{ID: "claude-sonnet-4-5", ContextWindow: 200000},
		}},
		{"reasoning suffix preserved", modelWithContext("claude-opus-4.7-high", 200000), []ModelFlavour{
			{ID: "claude-opus-4-7-high", ContextWindow: 200000},
		}},
		{"future claude family is dashed", modelWithContext("claude-fable-5.1", 200000), []ModelFlavour{
			{ID: "claude-fable-5-1", ContextWindow: 200000},
		}},
		{"single-tier non-claude passes through", modelWithContext("gpt-5.3-codex", 272000), []ModelFlavour{
			{ID: "gpt-5.3-codex", ContextWindow: 272000},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ModelFlavours(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %+v, want %+v", got, tt.want)
				}
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
		{"gpt-6-luna[long]", "gpt-6-luna"},
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

func TestResolveAvailableClaudeMajor(t *testing.T) {
	models := []state.Model{
		{ID: "claude-sonnet-5.9"},
		{ID: "claude-sonnet-5.10"},
		{ID: "claude-sonnet-5.11", Policy: state.ModelPolicy{State: "disabled"}},
		{ID: "claude-sonnet-5.12-high"},
		{ID: "claude-opus-5.99"},
	}
	if got := resolveAvailableClaudeMajor("claude-sonnet-5", models); got != "claude-sonnet-5.10" {
		t.Fatalf("major alias = %q", got)
	}
	for _, exact := range []string{"claude-sonnet-5.9", "claude-sonnet-5-high", "gpt-5"} {
		if got := resolveAvailableClaudeMajor(exact, models); got != "" {
			t.Fatalf("exact model %q unexpectedly resolved to %q", exact, got)
		}
	}
}
