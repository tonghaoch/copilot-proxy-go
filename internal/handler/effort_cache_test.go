package handler

import (
	"reflect"
	"testing"
)

func TestParseEffortError(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantModel     string
		wantSupported []string
	}{
		{
			name:          "real Copilot 400 body",
			body:          `output_config.effort "high" is not supported by model claude-opus-4.8; supported values: [medium]`,
			wantModel:     "claude-opus-4.8",
			wantSupported: []string{"medium"},
		},
		{
			name:          "multiple supported values",
			body:          `output_config.effort "max" is not supported by model claude-sonnet-4; supported values: [low, medium, high]`,
			wantModel:     "claude-sonnet-4",
			wantSupported: []string{"low", "medium", "high"},
		},
		{
			name: "wrapped inside larger JSON body",
			body: `{"error":{"message":"output_config.effort \"high\" is not supported by model claude-opus-4.8; supported values: [medium]"}}`,
			// JSON escapes the quotes — regex still matches the inner text once unescaped on the wire.
			wantModel:     "claude-opus-4.8",
			wantSupported: []string{"medium"},
		},
		{
			name:          "unrelated error",
			body:          `model not found`,
			wantModel:     "",
			wantSupported: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotSupported := parseEffortError(tt.body)
			if gotModel != tt.wantModel {
				t.Errorf("model: got %q, want %q", gotModel, tt.wantModel)
			}
			if !reflect.DeepEqual(gotSupported, tt.wantSupported) {
				t.Errorf("supported: got %v, want %v", gotSupported, tt.wantSupported)
			}
		})
	}
}

func TestPickClosestEffort(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		supported []string
		want      string
	}{
		{"exact match wins", "high", []string{"low", "medium", "high"}, "high"},
		{"downgrade high → medium", "high", []string{"medium"}, "medium"},
		{"downgrade max → high when medium also present", "max", []string{"high", "medium"}, "high"},
		{"upgrade low → medium when only medium available", "low", []string{"medium"}, "medium"},
		{"prefer weaker over stronger", "medium", []string{"high", "low"}, "low"},
		{"unknown requested falls back to first supported", "weird", []string{"medium", "low"}, "medium"},
		{"empty supported returns empty", "high", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickClosestEffort(tt.requested, tt.supported)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSupportCacheRoundtrip(t *testing.T) {
	c := &supportCache{entries: map[string][]string{}}

	if got := c.Get("claude-opus-4.8"); got != nil {
		t.Fatalf("expected nil for unknown model, got %v", got)
	}

	c.Set("claude-opus-4.8", []string{"medium"})
	got := c.Get("claude-opus-4.8")
	if !reflect.DeepEqual(got, []string{"medium"}) {
		t.Errorf("got %v, want [medium]", got)
	}

	// Mutating returned slice must not affect cache.
	got[0] = "low"
	if again := c.Get("claude-opus-4.8"); !reflect.DeepEqual(again, []string{"medium"}) {
		t.Errorf("cache leaked mutation: got %v", again)
	}
}

func TestClampEffort(t *testing.T) {
	// Swap out the package-level cache so we don't pollute it for other tests.
	orig := effortSupportCache
	defer func() { effortSupportCache = orig }()
	effortSupportCache = &supportCache{entries: map[string][]string{}}

	// No cache entry → passthrough.
	if got := clampEffort("claude-opus-4.8", "high"); got != "high" {
		t.Errorf("uncached: got %q, want high", got)
	}

	effortSupportCache.Set("claude-opus-4.8", []string{"medium"})
	if got := clampEffort("claude-opus-4.8", "high"); got != "medium" {
		t.Errorf("cached: got %q, want medium", got)
	}
}

func TestSetOutputConfigEffort(t *testing.T) {
	t.Run("creates output_config when absent", func(t *testing.T) {
		p := map[string]any{}
		setOutputConfigEffort(p, "high")
		got, ok := p["output_config"].(map[string]any)
		if !ok {
			t.Fatalf("output_config missing or wrong type: %T", p["output_config"])
		}
		if got["effort"] != "high" {
			t.Errorf("effort: got %v, want high", got["effort"])
		}
	})

	t.Run("preserves existing keys on update", func(t *testing.T) {
		p := map[string]any{
			"output_config": map[string]any{"other": "keep_me"},
		}
		setOutputConfigEffort(p, "medium")
		got, ok := p["output_config"].(map[string]any)
		if !ok {
			t.Fatalf("output_config missing or wrong type: %T", p["output_config"])
		}
		if got["effort"] != "medium" {
			t.Errorf("effort: got %v, want medium", got["effort"])
		}
		if got["other"] != "keep_me" {
			t.Errorf("other key dropped: got %v, want keep_me", got["other"])
		}
	})

	t.Run("second call overwrites effort but preserves other keys", func(t *testing.T) {
		// Simulates the first-write-then-retry flow: first call creates the
		// map with effort=high, second call (retry) updates to medium.
		p := map[string]any{}
		setOutputConfigEffort(p, "high")
		setOutputConfigEffort(p, "medium")
		got, ok := p["output_config"].(map[string]any)
		if !ok {
			t.Fatalf("output_config missing or wrong type: %T", p["output_config"])
		}
		if got["effort"] != "medium" {
			t.Errorf("effort: got %v, want medium", got["effort"])
		}
	})

	t.Run("empty effort is a no-op", func(t *testing.T) {
		p := map[string]any{}
		setOutputConfigEffort(p, "")
		if _, exists := p["output_config"]; exists {
			t.Error("output_config should not be set for empty effort")
		}
	})
}
