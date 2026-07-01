package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// ModelsListResponse is the OpenAI-compatible models list response.
type ModelsListResponse struct {
	Object  string       `json:"object"`
	Data    []ModelEntry `json:"data"`
	HasMore bool         `json:"has_more"`
}

// ModelEntry is a single model in the list response.
type ModelEntry struct {
	ID              string `json:"id"`
	Object          string `json:"object"`
	Type            string `json:"type"`
	Created         int    `json:"created"`
	OwnedBy         string `json:"owned_by"`
	DisplayName     string `json:"display_name,omitempty"`
	ContextWindow   int    `json:"context_window,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}

// Models handles GET /models and /v1/models.
func Models(w http.ResponseWriter, r *http.Request) {
	models := state.Global.GetModels()

	// Fallback: fetch models if not cached yet
	if len(models) == 0 {
		slog.Info("models not cached, fetching...")
		fetched, err := service.FetchModels()
		if err != nil {
			slog.Error("failed to fetch models", "error", err)
			http.Error(w, `{"error": "failed to fetch models"}`, http.StatusInternalServerError)
			return
		}
		state.Global.SetModels(fetched)
		models = fetched
	}

	entries := make([]ModelEntry, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, m := range models {
		publicID := ToClaudeCodeName(m)
		if _, dup := seen[publicID]; dup {
			continue
		}
		seen[publicID] = struct{}{}
		entries = append(entries, ModelEntry{
			ID:              publicID,
			Object:          "model",
			Type:            "model",
			Created:         0,
			OwnedBy:         m.Vendor,
			DisplayName:     m.Name,
			ContextWindow:   m.Capabilities.Limits.MaxContextWindowTokens,
			MaxOutputTokens: m.Capabilities.Limits.MaxOutputTokens,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ModelsListResponse{
		Object:  "list",
		Data:    entries,
		HasMore: false,
	})
}
