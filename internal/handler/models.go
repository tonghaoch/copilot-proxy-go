package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

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
	ID              string             `json:"id"`
	Object          string             `json:"object"`
	Type            string             `json:"type"`
	Created         int                `json:"created"`
	OwnedBy         string             `json:"owned_by"`
	DisplayName     string             `json:"display_name,omitempty"`
	ContextWindow   int                `json:"context_window,omitempty"`
	MaxOutputTokens int                `json:"max_output_tokens,omitempty"`
	Limits          *state.ModelLimits `json:"limits,omitempty"`
	Policy          string             `json:"policy,omitempty"`
}

// Models handles GET /models and /v1/models.
func Models(w http.ResponseWriter, r *http.Request) {
	defaultHandler.Models(w, r)
}

func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	models := h.state.GetModels()

	// Fallback: fetch models if not cached yet
	if len(models) == 0 {
		slog.Info("models not cached, fetching...")
		fetched, err := h.copilot.FetchModels(r.Context())
		if err != nil {
			slog.Error("failed to fetch models", "error", err)
			http.Error(w, `{"error": "failed to fetch models"}`, http.StatusInternalServerError)
			return
		}
		h.state.SetModels(fetched)
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
		limits := m.Capabilities.Limits
		entries = append(entries, ModelEntry{
			ID:              publicID,
			Object:          "model",
			Type:            "model",
			Created:         0,
			OwnedBy:         m.Vendor,
			DisplayName:     m.Name,
			ContextWindow:   limits.MaxContextWindowTokens,
			MaxOutputTokens: limits.MaxOutputTokens,
			Limits:          &limits,
			Policy:          m.Policy.State,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ModelsListResponse{
		Object:  "list",
		Data:    entries,
		HasMore: false,
	})
}
