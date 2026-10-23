package main

import (
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/shell"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func TestCodexModelWindows(t *testing.T) {
	chat := func(id string, ctx int) state.Model {
		m := state.Model{ID: id}
		m.Capabilities.Type = "chat"
		m.Capabilities.Limits.MaxPromptTokens = ctx
		return m
	}
	tiered := chat("gpt-6-luna", 872000)
	tiered.Billing.TokenPrices = map[string]state.PriceTier{
		"default":      {MaxPromptTokens: 272000},
		"long_context": {MaxPromptTokens: 872000},
	}
	embed := state.Model{ID: "text-embedding-3-small"}
	embed.Capabilities.Type = "embeddings"

	ids, windows := codexModelWindows([]state.Model{tiered, chat("claude-haiku-5.5", 100000), embed, chat("gpt-6-luna", 1)})
	want := []string{"claude-haiku-5-5", "gpt-6-luna", "gpt-6-luna[long]"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if windows["gpt-6-luna"] != 272000 || windows["gpt-6-luna[long]"] != 872000 {
		t.Fatalf("windows = %v", windows)
	}
}

func TestBuildClaudeCodeCommandContextSuffix(t *testing.T) {
	primary := state.Model{ID: "gpt-6-astra", Capabilities: state.ModelCapabilities{
		Limits: state.ModelLimits{MaxContextWindowTokens: 1050000},
	}}
	for _, smallContext := range []int{400000, 1000000} {
		small := state.Model{ID: "test-small", Capabilities: state.ModelCapabilities{
			Limits: state.ModelLimits{MaxContextWindowTokens: smallContext},
		}}
		wantSmall := "test-small"
		if smallContext >= 1000000 {
			wantSmall += "[1m]"
		}
		cmd := buildClaudeCodeCommand(shell.Zsh, 4141, handler.ToClaudeCodeName(primary), handler.ToClaudeCodeName(small), "opus", "fable")
		for _, check := range []string{
			"ANTHROPIC_MODEL='gpt-6-astra[1m]'",
			"ANTHROPIC_DEFAULT_OPUS_MODEL='opus'",
			"ANTHROPIC_DEFAULT_SONNET_MODEL='gpt-6-astra[1m]'",
			"ANTHROPIC_SMALL_FAST_MODEL='" + wantSmall + "'",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL='" + wantSmall + "'",
			"ANTHROPIC_DEFAULT_FABLE_MODEL='fable'",
		} {
			if !strings.Contains(cmd, check) {
				t.Errorf("expected command to contain %q, got %q", check, cmd)
			}
		}
		if got := handler.ResolveCopilotModel(handler.ToClaudeCodeName(small)); got != small.ID {
			t.Errorf("persisted small model = %q, want clean Copilot ID %q", got, small.ID)
		}
	}
}

func TestBuildCodexCommand(t *testing.T) {
	cmd := buildCodexCommand(shell.Bash, "gpt-5.3-codex", "http://127.0.0.1:4141/v1", 400000)

	checks := []string{
		"codex",
		"model=\"gpt-5.3-codex\"",
		"model_provider=\"copilot-proxy\"",
		"model_providers.copilot-proxy.name=\"Copilot Proxy\"",
		"model_providers.copilot-proxy.base_url=\"http://127.0.0.1:4141/v1\"",
		"model_providers.copilot-proxy.env_key=\"CODEX_API_KEY\"",
		"model_providers.copilot-proxy.wire_api=\"responses\"",
		"model_context_window=400000",
	}
	for _, check := range checks {
		if !strings.Contains(cmd, check) {
			t.Fatalf("expected command to contain %q, got %q", check, cmd)
		}
	}

	// A zero/unknown window omits the override entirely.
	noWin := buildCodexCommand(shell.Bash, "gpt-5.3-codex", "http://127.0.0.1:4141/v1", 0)
	if strings.Contains(noWin, "model_context_window") {
		t.Fatalf("expected no model_context_window override for zero window, got %q", noWin)
	}
}
