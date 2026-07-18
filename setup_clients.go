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

func responsesCapableModelIDs(models []state.Model) []string {
	seen := make(map[string]struct{}, len(models))
	ids := make([]string, 0, len(models))
	for _, m := range models {
		for _, endpoint := range m.SupportedEndpoints {
			if endpoint != "/responses" {
				continue
			}
			if _, dup := seen[m.ID]; dup {
				break
			}
			seen[m.ID] = struct{}{}
			ids = append(ids, m.ID)
			break
		}
	}
	sort.Strings(ids)
	return ids
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
	ids := responsesCapableModelIDs(models)
	model, err := runSelect("Select Codex model", ids, palettePrimary)
	if err != nil {
		return fmt.Errorf("model selection cancelled: %w", err)
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d/v1", port)
	shellType := shell.Detect()
	contextWindow := 0
	if m := state.Global.FindModel(model); m != nil {
		contextWindow = m.Capabilities.Limits.MaxContextWindowTokens
	}
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
		name := handler.ToClaudeCodeName(m)
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		ids = append(ids, name)
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

	copilotSmall := handler.ResolveCopilotModel(smallModel)
	if err := config.SetSmallModel(copilotSmall); err != nil {
		slog.Warn("failed to persist small model to config", "error", err)
	} else {
		slog.Info("config.smallModel updated", "model", copilotSmall)
	}

	vars := []shell.EnvVar{
		{Key: "ANTHROPIC_BASE_URL", Value: fmt.Sprintf("http://localhost:%d", port)},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "copilot-proxy"},
		{Key: "ANTHROPIC_MODEL", Value: primaryModel},
		{Key: "ANTHROPIC_SMALL_FAST_MODEL", Value: smallModel},
		{Key: "ANTHROPIC_DEFAULT_SONNET_MODEL", Value: primaryModel},
		{Key: "ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: smallModel},
		{Key: "DISABLE_NON_ESSENTIAL_MODEL_CALLS", Value: "1"},
		{Key: "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", Value: "1"},
	}
	printGeneratedScript(shell.GenerateExportScript(shell.Detect(), vars, "claude"))
	return nil
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
