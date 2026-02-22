package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// Config represents the application configuration stored in config.json.
type Config struct {
	Auth                  AuthConfig        `json:"auth"`
	ExtraPrompts          map[string]string `json:"extraPrompts"`
	SmallModel            string            `json:"smallModel"`
	ModelReasoningEfforts map[string]string `json:"modelReasoningEfforts"`
	UseFunctionApplyPatch bool              `json:"useFunctionApplyPatch"`
	CompactUseSmallModel  bool              `json:"compactUseSmallModel"`
}

type AuthConfig struct {
	APIKeys []string `json:"apiKeys"`
}

var (
	current *Config
	mu      sync.RWMutex
)

// defaultExtraPrompts are auto-merged into user config on startup.
var defaultExtraPrompts = map[string]string{
	"gpt-5-mini": `When exploring a codebase or searching for information, batch your tool calls for efficiency. Use multi_tool_use.parallel to run multiple tool calls simultaneously when they are independent of each other.`,
	"gpt-5.1-codex-max": `When exploring a codebase or searching for information, batch your tool calls for efficiency. Use multi_tool_use.parallel to run multiple tool calls simultaneously when they are independent of each other.`,
	"gpt-5.3-codex": `You have two channels for communication:
1. "commentary" channel: Use this for thinking out loud, explaining your approach, and providing updates to the user. These messages are shown to the user in real-time.
2. "final" channel: Use this for the final, polished response or code output.

Guidelines:
- Provide frequent updates via commentary so the user knows what you're doing
- Match the user's tone and personality in your commentary
- Use the final channel only when you have a complete, ready-to-use response`,
}

// defaultConfig returns the default configuration.
func defaultConfig() *Config {
	return &Config{
		Auth:                  AuthConfig{APIKeys: []string{}},
		ExtraPrompts:          make(map[string]string),
		SmallModel:            "gpt-5-mini",
		ModelReasoningEfforts: map[string]string{"gpt-5-mini": "low"},
		UseFunctionApplyPatch: true,
		CompactUseSmallModel:  true,
	}
}

// Load reads the config from disk, creating it with defaults if it doesn't exist.
func Load() error {
	configPath := state.ConfigPath()

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create default config
			cfg := defaultConfig()
			if err := save(cfg); err != nil {
				return err
			}
			mu.Lock()
			current = cfg
			mu.Unlock()
			slog.Info("created default config", "path", configPath)
			return nil
		}
		return err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("failed to parse config, using defaults", "error", err)
		cfg = *defaultConfig()
	}

	// Apply defaults for missing fields
	if cfg.SmallModel == "" {
		cfg.SmallModel = "gpt-5-mini"
	}
	if cfg.ExtraPrompts == nil {
		cfg.ExtraPrompts = make(map[string]string)
	}
	if cfg.ModelReasoningEfforts == nil {
		cfg.ModelReasoningEfforts = map[string]string{"gpt-5-mini": "low"}
	}

	mu.Lock()
	current = &cfg
	mu.Unlock()

	return nil
}

// MergeDefaults merges default extraPrompts into the config without
// overwriting existing entries, then saves back to disk.
func MergeDefaults() {
	mu.Lock()
	defer mu.Unlock()

	if current == nil {
		return
	}

	changed := false
	for k, v := range defaultExtraPrompts {
		if _, exists := current.ExtraPrompts[k]; !exists {
			current.ExtraPrompts[k] = v
			changed = true
		}
	}

	if changed {
		if err := save(current); err != nil {
			slog.Warn("failed to save config after merge", "error", err)
		} else {
			slog.Info("merged default extraPrompts into config")
		}
	}
}

func save(cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(state.ConfigPath(), data, 0600)
}

// Get returns the current config. Thread-safe.
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	if current == nil {
		return defaultConfig()
	}
	return current
}

// GetExtraPrompt returns the extra prompt for a model, if any.
func GetExtraPrompt(model string) string {
	cfg := Get()
	return cfg.ExtraPrompts[model]
}

// GetReasoningEffort returns the reasoning effort for a model.
// Defaults to "high" if not configured.
func GetReasoningEffort(model string) string {
	cfg := Get()
	if effort, ok := cfg.ModelReasoningEfforts[model]; ok {
		return effort
	}
	return "high"
}

// GetAPIKeys returns the configured API keys (normalized).
func GetAPIKeys() []string {
	cfg := Get()
	return normalizeAPIKeys(cfg.Auth.APIKeys)
}

// normalizeAPIKeys trims, deduplicates, and filters invalid API keys.
func normalizeAPIKeys(keys []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, k := range keys {
		k = trimSpace(k)
		if k == "" {
			continue
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		result = append(result, k)
	}
	return result
}

func trimSpace(s string) string {
	// Manual trim to avoid importing strings just for this
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
