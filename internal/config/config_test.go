package config

import "testing"

func TestGetReturnsDeepCopy(t *testing.T) {
	mu.Lock()
	previous := current
	current = &Config{
		Auth:                  AuthConfig{APIKeys: []string{"secret"}},
		ExtraPrompts:          map[string]string{"model": "prompt"},
		ModelReasoningEfforts: map[string]string{"model": "high"},
	}
	mu.Unlock()
	defer func() {
		mu.Lock()
		current = previous
		mu.Unlock()
	}()

	copy := Get()
	copy.Auth.APIKeys[0] = "changed"
	copy.ExtraPrompts["model"] = "changed"
	copy.ModelReasoningEfforts["model"] = "low"

	got := Get()
	if got.Auth.APIKeys[0] != "secret" || got.ExtraPrompts["model"] != "prompt" || got.ModelReasoningEfforts["model"] != "high" {
		t.Fatalf("Get exposed mutable config state: %+v", got)
	}
}

func TestIs1MContextModelUsesConfigList(t *testing.T) {
	mu.Lock()
	previous := current
	current = &Config{Extended1MModels: map[string]bool{"claude-haiku-5.5": true, "claude-sonnet-4.6": false}}
	mu.Unlock()
	defer func() {
		mu.Lock()
		current = previous
		mu.Unlock()
	}()

	if !Is1MContextModel("claude-haiku-5.5") {
		t.Fatal("listed model should be 1M")
	}
	if Is1MContextModel("claude-sonnet-4.6") {
		t.Fatal("model set to false should not be 1M")
	}
	if Is1MContextModel("claude-opus-4.8") {
		t.Fatal("unlisted model should not be 1M")
	}
}

func TestDefaultConfigEnablesHaiku1M(t *testing.T) {
	if !defaultConfig().Extended1MModels["claude-haiku-5.5"] {
		t.Fatal("default config should enable 1M for claude-haiku-5.5")
	}
}
