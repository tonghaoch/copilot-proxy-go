package main

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/shell"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// codexModelWindows maps every model flavour Codex can use to its prompt
// budget. All chat models qualify: /v1/responses converts requests for
// models without a native Responses endpoint.
func codexModelWindows(models []state.Model) ([]string, map[string]int) {
	windows := map[string]int{}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		if m.Capabilities.Type != "chat" {
			continue
		}
		for _, f := range handler.ModelFlavours(m) {
			if _, dup := windows[f.ID]; dup {
				continue
			}
			windows[f.ID] = f.ContextWindow
			ids = append(ids, f.ID)
		}
	}
	sort.Strings(ids)
	return ids, windows
}

func codexAPIKey() string {
	keys := config.GetAPIKeys()
	if len(keys) > 0 {
		return keys[0]
	}
	return "copilot-proxy"
}

func buildCodexCommand(shellType shell.ShellType, model, baseURL string, contextWindow int) string {
	overrides := []string{
		"model=" + strconv.Quote(model),
		"model_provider=" + strconv.Quote("copilot-proxy"),
		"model_providers.copilot-proxy.name=" + strconv.Quote("Copilot Proxy"),
		"model_providers.copilot-proxy.base_url=" + strconv.Quote(baseURL),
		"model_providers.copilot-proxy.env_key=" + strconv.Quote("CODEX_API_KEY"),
		"model_providers.copilot-proxy.wire_api=" + strconv.Quote("responses"),
	}
	if contextWindow > 0 {
		overrides = append(overrides, "model_context_window="+strconv.Itoa(contextWindow))
	}
	parts := []string{"codex"}
	for _, override := range overrides {
		parts = append(parts, "-c", shell.QuoteArg(shellType, override))
	}
	return strings.Join(parts, " ")
}

func runCodexSetup(port int, models []state.Model) error {
	ids, windows := codexModelWindows(models)
	model, err := runSelect("Select Codex model", ids, palettePrimary)
	if err != nil {
		return fmt.Errorf("model selection cancelled: %w", err)
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d/v1", port)
	shellType := shell.Detect()
	contextWindow := windows[model]
	vars := []shell.EnvVar{
		{Key: "CODEX_API_KEY", Value: codexAPIKey()},
		{Key: "NO_PROXY", Value: "localhost,127.0.0.1,::1"},
		{Key: "no_proxy", Value: "localhost,127.0.0.1,::1"},
	}
	script := shell.GenerateExportScript(shellType, vars, buildCodexCommand(shellType, model, baseURL, contextWindow))
	printGeneratedScript(script)
	return nil
}

func runClaudeCodeSetup(port int, models []state.Model) error {
	seen := make(map[string]struct{}, len(models))
	ids := make([]string, 0, len(models))
	for _, m := range models {
		for _, f := range handler.ModelFlavours(m) {
			if _, dup := seen[f.ID]; dup {
				continue
			}
			seen[f.ID] = struct{}{}
			ids = append(ids, f.ID)
		}
	}
	sort.Strings(ids)

	primaryModel, err := runSelect("Select primary model", ids, palettePrimary)
	if err != nil {
		return fmt.Errorf("model selection cancelled: %w", err)
	}
	smallModel, err := runSelect("Select small/fast model", ids, paletteSmall)
	if err != nil {
		return fmt.Errorf("model selection cancelled: %w", err)
	}
	opusModel, err := runSelect("Select Opus model", ids, paletteOpus)
	if err != nil {
		return fmt.Errorf("model selection cancelled: %w", err)
	}
	fableModel, err := runSelect("Select Fable model", ids, paletteFable)
	if err != nil {
		return fmt.Errorf("model selection cancelled: %w", err)
	}

	copilotSmall := handler.ResolveCopilotModel(smallModel)
	if err := config.SetSmallModel(copilotSmall); err != nil {
		slog.Warn("failed to persist small model to config", "error", err)
	} else {
		slog.Info("config.smallModel updated", "model", copilotSmall)
	}

	printGeneratedScript(buildClaudeCodeCommand(shell.Detect(), port, primaryModel, smallModel, opusModel, fableModel))
	return nil
}

func buildClaudeCodeCommand(shellType shell.ShellType, port int, primaryModel, smallModel, opusModel, fableModel string) string {
	vars := []shell.EnvVar{
		{Key: "ANTHROPIC_BASE_URL", Value: fmt.Sprintf("http://localhost:%d", port)},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: codexAPIKey()},
		{Key: "ANTHROPIC_MODEL", Value: primaryModel},
		{Key: "ANTHROPIC_SMALL_FAST_MODEL", Value: smallModel},
		{Key: "ANTHROPIC_DEFAULT_OPUS_MODEL", Value: opusModel},
		{Key: "ANTHROPIC_DEFAULT_SONNET_MODEL", Value: primaryModel},
		{Key: "ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: smallModel},
		{Key: "ANTHROPIC_DEFAULT_FABLE_MODEL", Value: fableModel},
		{Key: "DISABLE_NON_ESSENTIAL_MODEL_CALLS", Value: "1"},
		{Key: "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", Value: "1"},
	}
	return shell.GenerateExportScript(shellType, vars, "claude")
}

func printGeneratedScript(script string) {
	fmt.Println()
	fmt.Println("  Generated command:")
	fmt.Println()
	fmt.Printf("  %s\n", script)
	fmt.Println()
	if err := shell.CopyToClipboard(script); err != nil {
		fmt.Println("  (Could not copy to clipboard — paste the command above)")
	} else {
		fmt.Println("  Copied to clipboard!")
	}
	fmt.Println()
}
