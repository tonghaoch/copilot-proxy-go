package handler

import (
	"encoding/json"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// stubModels serves a single model to code that only needs a lookup.
type stubModels struct{ model state.Model }

func (s stubModels) GetModels() []state.Model { return []state.Model{s.model} }
func (s stubModels) SetModels([]state.Model)  {}
func (s stubModels) FindModel(id string) *state.Model {
	if id != s.model.ID {
		return nil
	}
	found := s.model
	return &found
}

// stubConfig returns a fixed reasoning effort, standing in for config.json.
type stubConfig struct{ effort string }

func (stubConfig) Snapshot() *config.Config        { return &config.Config{} }
func (stubConfig) APIKeys() []string               { return nil }
func (stubConfig) ExtraPrompt(string) string       { return "" }
func (c stubConfig) ReasoningEffort(string) string { return c.effort }

func adaptiveThinkingModel() state.Model {
	model := state.Model{ID: "claude-opus-4.8"}
	model.Capabilities.Supports.AdaptiveThinking = true
	return model
}

// effortIn reads back what was written to payload["output_config"]["effort"].
func effortIn(payload map[string]any) string {
	outputConfig, ok := payload["output_config"].(map[string]any)
	if !ok {
		return ""
	}
	effort, _ := outputConfig["effort"].(string)
	return effort
}

func TestApplyAdaptiveThinkingRespectsClient(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		configEffort string
		wantThinking bool   // adaptive thinking injected into the payload
		wantEffort   string // "" means no effort written
	}{
		{
			name:         "explicitly disabled stays disabled",
			body:         `{"model":"claude-opus-4.8","thinking":{"type":"disabled"}}`,
			configEffort: "high",
			wantThinking: false,
			wantEffort:   "",
		},
		{
			name:         "unspecified falls back to configured effort",
			body:         `{"model":"claude-opus-4.8"}`,
			configEffort: "high",
			wantThinking: true,
			wantEffort:   "high",
		},
		{
			name:         "budget_tokens outranks config",
			body:         `{"model":"claude-opus-4.8","thinking":{"type":"enabled","budget_tokens":31999}}`,
			configEffort: "low",
			wantThinking: true,
			wantEffort:   "high",
		},
		{
			name:         "explicit output_config effort outranks budget",
			body:         `{"model":"claude-opus-4.8","output_config":{"effort":"low"},"thinking":{"type":"enabled","budget_tokens":31999}}`,
			configEffort: "high",
			wantThinking: true,
			wantEffort:   "low",
		},
		{
			name:         "configured xhigh passes through untouched",
			body:         `{"model":"claude-opus-4.8"}`,
			configEffort: "xhigh",
			wantThinking: true,
			wantEffort:   "xhigh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal([]byte(tt.body), &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			var req AnthropicRequest
			if err := json.Unmarshal([]byte(tt.body), &req); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}

			got := applyAdaptiveThinkingInMap(payload, &req, stubModels{adaptiveThinkingModel()}, stubConfig{tt.configEffort})

			_, thinkingSet := payload["thinking"].(map[string]string)
			if thinkingSet != tt.wantThinking {
				t.Errorf("adaptive thinking injected = %v, want %v (payload thinking = %v)",
					thinkingSet, tt.wantThinking, payload["thinking"])
			}
			if got != tt.wantEffort {
				t.Errorf("returned effort = %q, want %q", got, tt.wantEffort)
			}
			if effort := effortIn(payload); effort != tt.wantEffort {
				t.Errorf("payload output_config.effort = %q, want %q", effort, tt.wantEffort)
			}
		})
	}
}

func TestApplyAdaptiveThinkingPreservesDisabledPayload(t *testing.T) {
	body := `{"model":"claude-opus-4.8","thinking":{"type":"disabled"}}`
	var payload map[string]any
	json.Unmarshal([]byte(body), &payload)
	var req AnthropicRequest
	json.Unmarshal([]byte(body), &req)

	applyAdaptiveThinkingInMap(payload, &req, stubModels{adaptiveThinkingModel()}, stubConfig{"high"})

	// The client's own thinking block must reach upstream untouched.
	thinking, ok := payload["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking should remain the client's object, got %T", payload["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Errorf("thinking.type = %v, want disabled", thinking["type"])
	}
	if _, exists := payload["output_config"]; exists {
		t.Errorf("output_config should not be added when thinking is disabled, got %v", payload["output_config"])
	}
}

func TestApplyAdaptiveThinkingSkipsUnsupportedModel(t *testing.T) {
	body := `{"model":"gpt-5-mini","thinking":{"type":"enabled","budget_tokens":10000}}`
	var payload map[string]any
	json.Unmarshal([]byte(body), &payload)
	var req AnthropicRequest
	json.Unmarshal([]byte(body), &req)

	if got := applyAdaptiveThinkingInMap(payload, &req, stubModels{adaptiveThinkingModel()}, stubConfig{"high"}); got != "" {
		t.Errorf("effort = %q, want empty for a model without adaptive thinking", got)
	}
}

func TestEffortFromThinkingBudget(t *testing.T) {
	tests := []struct {
		budget int
		want   string
	}{
		{1024, "low"},
		{3999, "low"},
		{4000, "medium"}, // "think"
		{9999, "medium"},
		{10000, "high"}, // "think hard"
		{31999, "high"},
		{32000, "max"}, // "ultrathink"
		{64000, "max"},
	}
	for _, tt := range tests {
		if got := effortFromThinkingBudget(tt.budget); got != tt.want {
			t.Errorf("effortFromThinkingBudget(%d) = %q, want %q", tt.budget, got, tt.want)
		}
	}
}
