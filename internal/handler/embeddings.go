package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
)

// Embeddings handles POST /embeddings and /v1/embeddings.
// It normalizes OpenAI-compatible input before forwarding to Copilot.
func Embeddings(w http.ResponseWriter, r *http.Request) {
	tracked := trackRequest(w, r, "embeddings")
	defer tracked.Finish()
	w = tracked.Writer
	rec := tracked.Record
	rec.Backend = "embeddings"
	rec.RequestType = "normal"

	var payload map[string]any
	body, err := decodeRequestBody(w, r, &payload)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	model, _ := payload["model"].(string)
	rec.Model = model
	rec.RoutedModel = model
	switch input := payload["input"].(type) {
	case string:
		payload["input"] = []string{input}
	case []any:
		// Copilot already accepts OpenAI array forms, including token arrays.
	default:
		api.ForwardError(w, api.InvalidRequest("input must be a string or array", nil))
		return
	}
	body, err = json.Marshal(payload)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	slog.Info("embeddings request")

	resp, err := service.ProxyEmbeddings(r.Context(), body)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	// Keep large embedding vectors as raw JSON. Decoding them into []any would
	// allocate one interface and one float value for every dimension.
	var result map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		api.ForwardError(w, err)
		return
	}
	if _, ok := result["object"]; !ok {
		result["object"] = json.RawMessage(`"list"`)
	}
	if _, ok := result["model"]; !ok {
		result["model"], _ = json.Marshal(model)
	}
	if rawUsage := result["usage"]; len(rawUsage) > 0 {
		var usage struct {
			PromptTokens int `json:"prompt_tokens"`
		}
		if json.Unmarshal(rawUsage, &usage) == nil {
			rec.InputTokens = int64(usage.PromptTokens)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	json.NewEncoder(w).Encode(result)
}
