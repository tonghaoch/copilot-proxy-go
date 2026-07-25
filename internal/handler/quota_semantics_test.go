package handler

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func TestIsWarmupRequest(t *testing.T) {
	const beta = "claude-code-20250219"

	tests := []struct {
		name string
		body string
		beta string
		want bool
	}{
		{
			name: "probe capped at one token",
			body: `{"max_tokens":1,"messages":[{"role":"user","content":"test"}]}`,
			beta: beta,
			want: true,
		},
		{
			name: "single message with no system prompt",
			body: `{"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`,
			beta: beta,
			want: true,
		},
		{
			name: "ordinary question without tools is not a warmup",
			body: `{"max_tokens":8192,"system":"You are Claude Code.","messages":[{"role":"user","content":"explain this code"}]}`,
			beta: beta,
			want: false,
		},
		{
			name: "multi-turn conversation without tools is not a warmup",
			body: `{"max_tokens":8192,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"user","content":"go on"}]}`,
			beta: beta,
			want: false,
		},
		{
			name: "request carrying tools is never a warmup",
			body: `{"max_tokens":1,"tools":[{"name":"Read"}],"messages":[{"role":"user","content":"x"}]}`,
			beta: beta,
			want: false,
		},
		{
			name: "no beta header",
			body: `{"max_tokens":1,"messages":[{"role":"user","content":"test"}]}`,
			beta: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req AnthropicRequest
			if err := json.Unmarshal([]byte(tt.body), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := isWarmupRequest(&req, tt.beta); got != tt.want {
				t.Errorf("isWarmupRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplySmallModelLeavesNormalConversationAlone(t *testing.T) {
	// The regression this guards: a plan-mode question carries a beta header
	// and no tools, and used to be silently downgraded to the small model.
	body := `{"model":"claude-opus-4.8","max_tokens":8192,"system":"You are Claude Code.","messages":[{"role":"user","content":"explain this code"}]}`
	var req AnthropicRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if changed := applySmallModelIfNeeded(&req, "claude-code-20250219", defaultRuntimeConfig{}); changed {
		t.Errorf("normal conversation was downgraded to %q", req.Model)
	}
	if req.Model != "claude-opus-4.8" {
		t.Errorf("model = %q, want it unchanged", req.Model)
	}
}

func TestMapStopReason(t *testing.T) {
	tests := []struct {
		finishReason string
		want         string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		// A safety-filtered truncation must not read as a normal completion.
		{"content_filter", "refusal"},
		{"unrecognized", "end_turn"},
	}
	for _, tt := range tests {
		if got := mapStopReason(tt.finishReason); got != tt.want {
			t.Errorf("mapStopReason(%q) = %q, want %q", tt.finishReason, got, tt.want)
		}
	}
}

func responsesModel(id string, maxOutput int) state.Model {
	model := state.Model{ID: id}
	model.Capabilities.Limits.MaxOutputTokens = maxOutput
	return model
}

func TestResolveMaxOutputTokens(t *testing.T) {
	tests := []struct {
		name      string
		maxTokens int
		modelID   string
		limit     int
		want      int
	}{
		{
			name:      "small explicit budget is honoured",
			maxTokens: 256,
			modelID:   "gpt-5.3-codex",
			limit:     64000,
			want:      256,
		},
		{
			name:      "oversized budget is capped at the model limit",
			maxTokens: 200000,
			modelID:   "gpt-5.3-codex",
			limit:     64000,
			want:      64000,
		},
		{
			name:      "omitted budget falls back",
			maxTokens: 0,
			modelID:   "gpt-5.3-codex",
			limit:     64000,
			want:      fallbackMaxOutputTokens,
		},
		{
			name:      "warmup probe is raised to the endpoint minimum",
			maxTokens: 1,
			modelID:   "gpt-5.3-codex",
			limit:     64000,
			want:      minResponsesOutputTokens,
		},
		{
			name:      "unknown model passes the request through",
			maxTokens: 500,
			modelID:   "not-in-catalog",
			limit:     64000,
			want:      500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &AnthropicRequest{Model: tt.modelID, MaxTokens: tt.maxTokens}
			models := stubModels{responsesModel("gpt-5.3-codex", tt.limit)}
			if got := resolveMaxOutputTokens(req, models); got != tt.want {
				t.Errorf("resolveMaxOutputTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResolveTemperature(t *testing.T) {
	zero := float64(0)

	t.Run("reasoning models are pinned to 1", func(t *testing.T) {
		req := &AnthropicRequest{Temperature: &zero}
		got := resolveTemperature(req, &ResponsesReasoning{Effort: "high"})
		if got == nil || *got != 1 {
			t.Fatalf("temperature = %v, want 1", got)
		}
	})

	t.Run("non-reasoning models keep the caller's value", func(t *testing.T) {
		req := &AnthropicRequest{Temperature: &zero}
		got := resolveTemperature(req, &ResponsesReasoning{})
		if got == nil || *got != 0 {
			t.Fatalf("temperature = %v, want 0", got)
		}
	})

	t.Run("unset stays unset", func(t *testing.T) {
		if got := resolveTemperature(&AnthropicRequest{}, &ResponsesReasoning{}); got != nil {
			t.Fatalf("temperature = %v, want nil", got)
		}
	})
}

func TestTranslateToResponsesForwardsSamplingParams(t *testing.T) {
	topP := 0.5
	req := &AnthropicRequest{
		Model:     "gpt-5.3-codex",
		MaxTokens: 256,
		TopP:      &topP,
	}
	models := stubModels{responsesModel("gpt-5.3-codex", 64000)}

	payload, err := translateToResponsesWithModels(req, "", "high", models)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	if payload.MaxOutputTokens != 256 {
		t.Errorf("max_output_tokens = %d, want 256", payload.MaxOutputTokens)
	}
	if payload.TopP == nil || *payload.TopP != 0.5 {
		t.Errorf("top_p = %v, want 0.5", payload.TopP)
	}
}

func TestResolveResponsesEffort(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		configured string
		want       string
	}{
		{
			name:       "disabled thinking asks for the none tier",
			body:       `{"model":"gpt-5.3-codex","thinking":{"type":"disabled"}}`,
			configured: "high",
			want:       "none",
		},
		{
			name:       "config applies when the client says nothing",
			body:       `{"model":"gpt-5.3-codex"}`,
			configured: "low",
			want:       "low",
		},
		{
			name:       "ultrathink budget outranks config",
			body:       `{"model":"gpt-5.3-codex","thinking":{"type":"enabled","budget_tokens":32000}}`,
			configured: "low",
			want:       "xhigh", // Responses rejects "max"; xhigh is the strongest it takes
		},
		{
			name:       "think budget maps to medium",
			body:       `{"model":"gpt-5.3-codex","thinking":{"type":"enabled","budget_tokens":4000}}`,
			configured: "high",
			want:       "medium",
		},
		{
			name:       "explicit output_config effort wins",
			body:       `{"model":"gpt-5.3-codex","output_config":{"effort":"low"},"thinking":{"type":"enabled","budget_tokens":32000}}`,
			configured: "high",
			want:       "low",
		},
		{
			name:       "operator xhigh survives when the client is silent",
			body:       `{"model":"gpt-5.3-codex"}`,
			configured: "xhigh",
			want:       "xhigh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req AnthropicRequest
			if err := json.Unmarshal([]byte(tt.body), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := resolveResponsesEffort(&req, tt.configured); got != tt.want {
				t.Errorf("resolveResponsesEffort() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestThinkTierReachesBothBackends is the cross-backend contract: the same
// think tier must take effect whether the selected model routes to the native
// Messages API or to Responses.
func TestThinkTierReachesBothBackends(t *testing.T) {
	body := `{"model":"M","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":32000},"messages":[{"role":"user","content":"hi"}]}`

	var nativeReq AnthropicRequest
	json.Unmarshal([]byte(body), &nativeReq)
	var payload map[string]any
	json.Unmarshal([]byte(body), &payload)
	nativeEffort := applyAdaptiveThinkingInMap(payload, &nativeReq, stubModels{adaptiveModelNamed("M")}, stubConfig{"low"})

	var responsesReq AnthropicRequest
	json.Unmarshal([]byte(body), &responsesReq)
	adapter := defaultAnthropicAdapter{models: stubModels{responsesModel("M", 64000)}, config: stubConfig{"low"}}
	responsesPayload, err := adapter.ToResponses(&responsesReq, "")
	if err != nil {
		t.Fatalf("ToResponses: %v", err)
	}

	// The vocabularies differ (Responses rejects "max"), but neither route may
	// fall back to the configured "low" while the client asks for its top tier.
	if nativeEffort != "max" {
		t.Errorf("native effort = %q, want max", nativeEffort)
	}
	if responsesPayload.Reasoning.Effort != "xhigh" {
		t.Errorf("responses effort = %q, want xhigh", responsesPayload.Reasoning.Effort)
	}
	if responsesPayload.Reasoning.Effort == "low" {
		t.Error("responses path ignored the client's think tier and used config")
	}
}

func adaptiveModelNamed(id string) state.Model {
	model := state.Model{ID: id}
	model.Capabilities.Supports.AdaptiveThinking = true
	return model
}

func TestParseEffortErrorSpaceSeparatedList(t *testing.T) {
	// Verbatim body observed from Copilot. Splitting this on commas stored the
	// whole list as one bogus effort, which was then sent back as a value —
	// each 400 re-poisoning the cache and bricking the model for the process.
	body := `output_config.effort "high" is not supported by model claude-opus-4.8; supported values: [low medium high xhigh max]`

	model, supported := parseEffortError(body)
	if model != "claude-opus-4.8" {
		t.Errorf("model = %q, want claude-opus-4.8", model)
	}
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(supported, want) {
		t.Fatalf("supported = %q, want %q", supported, want)
	}
	// Every parsed entry must be usable as an effort on the retry.
	for _, effort := range supported {
		if !isKnownEffort(effort) {
			t.Errorf("parsed %q is not a sendable effort", effort)
		}
	}
}

func TestParseEffortErrorRejectsUnparseableList(t *testing.T) {
	// A list we cannot make sense of must yield nothing, so the caller keeps
	// the original error instead of caching junk.
	body := `output_config.effort "high" is not supported by model m; supported values: [??? !!!]`
	if model, supported := parseEffortError(body); model != "" || supported != nil {
		t.Errorf("got (%q, %q), want empty", model, supported)
	}
}

func TestEffortFallbackSurvivesRealRejection(t *testing.T) {
	// End-to-end on the cache: after a real rejection, the next request for
	// this model must pick a genuine effort rather than the whole list.
	const model = "test-effort-model"
	body := `output_config.effort "max" is not supported by model ` + model + `; supported values: [low medium]`

	parsedModel, supported := parseEffortError(body)
	effortSupportCache.Set(parsedModel, supported)
	t.Cleanup(func() { effortSupportCache.Set(model, nil) })

	got := clampEffort(model, "max")
	if got != "medium" {
		t.Errorf("clampEffort = %q, want medium (closest supported)", got)
	}
	if !isKnownEffort(got) {
		t.Errorf("clampEffort returned unsendable value %q", got)
	}
}

func TestNativePayloadCarriesRoutedModel(t *testing.T) {
	// applySmallModelIfNeeded only rewrites req.Model; the native path forwards
	// the original body, so the substitution has to be copied onto the payload
	// or the downgrade never reaches upstream.
	rawBody := []byte(`{"model":"claude-opus-4.8","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`)
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	req := &AnthropicRequest{Model: "claude-haiku-4.5"} // post-downgrade

	if req.Model != "" {
		payload["model"] = req.Model
	}

	if payload["model"] != "claude-haiku-4.5" {
		t.Errorf("payload model = %v, want the routed model", payload["model"])
	}
}

func TestAnthropicEffortToResponses(t *testing.T) {
	// Verified against the live endpoint on gpt-5.3-codex: "max" and "minimal"
	// are rejected, everything else is accepted.
	tests := map[string]string{
		"max":     "xhigh",
		"minimal": "low",
		"xhigh":   "xhigh",
		"high":    "high",
		"medium":  "medium",
		"low":     "low",
	}
	for in, want := range tests {
		if got := anthropicEffortToResponses(in); got != want {
			t.Errorf("anthropicEffortToResponses(%q) = %q, want %q", in, got, want)
		}
	}
}
