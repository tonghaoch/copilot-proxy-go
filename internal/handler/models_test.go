package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func TestModelsExposesUpstreamLimitsAndPolicy(t *testing.T) {
	m := state.Model{ID: "claude-opus-5.5", Name: "Claude Opus 5.5", Policy: state.ModelPolicy{State: "enabled"}}
	m.Capabilities.Limits = state.ModelLimits{
		MaxContextWindowTokens: 328000,
		MaxOutputTokens:        128000,
		MaxNonStreamingOutput:  16000,
		MaxPromptTokens:        200000,
		Vision:                 state.VisionLimit{MaxPromptImages: 1, MaxPromptImageSize: 3145728},
	}
	state.Global.SetModels([]state.Model{m})

	recorder := httptest.NewRecorder()
	Models(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var resp struct {
		Data []struct {
			ID     string            `json:"id"`
			Policy string            `json:"policy"`
			Limits state.ModelLimits `json:"limits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(resp.Data))
	}
	got := resp.Data[0]
	if got.ID != "claude-opus-5-5" {
		t.Fatalf("id = %q", got.ID)
	}
	if got.Policy != "enabled" {
		t.Fatalf("policy = %q", got.Policy)
	}
	if got.Limits.MaxNonStreamingOutput != 16000 || got.Limits.MaxPromptTokens != 200000 {
		// single-tier: prompt budget is upstream max_prompt_tokens
		t.Fatalf("limits dropped upstream fields: %+v", got.Limits)
	}
	if got.Limits.Vision.MaxPromptImages != 1 || got.Limits.Vision.MaxPromptImageSize != 3145728 {
		t.Fatalf("vision limits dropped: %+v", got.Limits.Vision)
	}
}

func TestModelsListsEachContextFlavour(t *testing.T) {
	m := state.Model{ID: "claude-haiku-5.5", Name: "Claude Haiku 5.5"}
	m.Capabilities.Limits = state.ModelLimits{MaxContextWindowTokens: 1000000, MaxPromptTokens: 936000, MaxOutputTokens: 128000}
	m.Billing.TokenPrices = map[string]state.PriceTier{
		"default":      {MaxPromptTokens: 100000},
		"long_context": {MaxPromptTokens: 936000},
	}
	state.Global.SetModels([]state.Model{m})

	recorder := httptest.NewRecorder()
	Models(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	var resp struct {
		Data []ModelEntry `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("want default and long-context entries, got %+v", resp.Data)
	}
	if resp.Data[0].ID != "claude-haiku-5-5" || resp.Data[0].ContextWindow != 100000 {
		t.Fatalf("default entry = %+v", resp.Data[0])
	}
	if resp.Data[1].ID != "claude-haiku-5-5[1m]" || resp.Data[1].ContextWindow != 936000 {
		t.Fatalf("long entry = %+v", resp.Data[1])
	}
}
