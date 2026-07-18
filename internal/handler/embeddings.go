package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
)

// Embeddings handles POST /embeddings and /v1/embeddings.
// It normalizes OpenAI-compatible input before forwarding to Copilot.
func Embeddings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		api.ForwardError(w, api.InvalidRequest("invalid request body", err))
		return
	}
	model, _ := payload["model"].(string)
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

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		api.ForwardError(w, err)
		return
	}
	if _, ok := result["object"]; !ok {
		result["object"] = "list"
	}
	if _, ok := result["model"]; !ok {
		result["model"] = model
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	json.NewEncoder(w).Encode(result)
}
