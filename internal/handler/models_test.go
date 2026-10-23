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
		t.Fatalf("limits dropped upstream fields: %+v", got.Limits)
	}
	if got.Limits.Vision.MaxPromptImages != 1 || got.Limits.Vision.MaxPromptImageSize != 3145728 {
		t.Fatalf("vision limits dropped: %+v", got.Limits.Vision)
	}
}
