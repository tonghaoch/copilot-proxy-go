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
	Object  string         `json:"object"`
	Data    []ModelEntry   `json:"data"`
	HasMore bool           `json:"has_more"`
}

// ModelEntry is a single model in the list response.
type ModelEntry struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Type        string `json:"type"`
	Created     int    `json:"created"`
	OwnedBy    string `json:"owned_by"`
	DisplayName string `json:"display_name,omitempty"`
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

	entries := make([]ModelEntry, len(models))
	for i, m := range models {
		entries[i] = ModelEntry{
			ID:          m.ID,
			Object:      "model",
			Type:        "model",
			Created:     0,
			OwnedBy:    m.OwnedBy,
			DisplayName: m.Name,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ModelsListResponse{
		Object:  "list",
		Data:    entries,
		HasMore: false,
	})
}
