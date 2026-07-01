package main

import (
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/shell"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func TestResponsesCapableModelIDs(t *testing.T) {
	models := []state.Model{
		{ID: "z-model", SupportedEndpoints: []string{"/responses"}},
		{ID: "chat-only", SupportedEndpoints: []string{"/chat/completions"}},
		{ID: "a-model", SupportedEndpoints: []string{"/v1/messages", "/responses"}},
		{ID: "z-model", SupportedEndpoints: []string{"/responses"}},
	}

	got := responsesCapableModelIDs(models)
	want := []string{"a-model", "z-model"}
	if len(got) != len(want) {
		t.Fatalf("expected %d models, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
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
