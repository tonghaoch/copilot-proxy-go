package config

import "testing"

func TestGetReturnsDeepCopy(t *testing.T) {
	ttl := "1h"
	mu.Lock()
	previous := current
	current = &Config{
		Auth:                  AuthConfig{APIKeys: []string{"secret"}},
		ExtraPrompts:          map[string]string{"model": "prompt"},
		ModelReasoningEfforts: map[string]string{"model": "high"},
		CacheTTL:              &ttl,
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
	*copy.CacheTTL = "5m"

	got := Get()
	if got.Auth.APIKeys[0] != "secret" || got.ExtraPrompts["model"] != "prompt" || got.ModelReasoningEfforts["model"] != "high" || got.EffectiveCacheTTL() != "1h" {
		t.Fatalf("Get exposed mutable config state: %+v", got)
	}
}

func TestValidateCacheTTL(t *testing.T) {
	for _, tt := range []struct {
		value string
		valid bool
	}{
		{"", true},
		{"5m", true},
		{"1h", true},
		{"2h", false},
	} {
		cfg := &Config{CacheTTL: &tt.value}
		if got := cfg.ValidateCacheTTL() == nil; got != tt.valid {
			t.Errorf("cacheTTL %q valid = %v, want %v", tt.value, got, tt.valid)
		}
	}
	if err := (&Config{}).ValidateCacheTTL(); err != nil {
		t.Fatal(err)
	}
}
