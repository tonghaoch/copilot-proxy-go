package handler

import (
	"encoding/json"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// TokenResponse is the JSON response for the token endpoint.
type TokenResponse struct {
	Token string `json:"token"`
}

// Token handles GET /token — returns the current Copilot bearer token.
func Token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TokenResponse{
		Token: state.Global.GetCopilotToken(),
	})
}
