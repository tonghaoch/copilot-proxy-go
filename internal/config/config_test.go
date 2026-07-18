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
