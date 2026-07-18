package handler

import (
	"encoding/json"
	"net"
	"net/http"
)

// TokenResponse is the JSON response for the token endpoint.
type TokenResponse struct {
	Token string `json:"token"`
}

// Token handles GET /token — returns the current Copilot bearer token.
func Token(w http.ResponseWriter, r *http.Request) {
	defaultHandler.Token(w, r)
}

func (h *Handler) Token(w http.ResponseWriter, r *http.Request) {
	// When authentication is intentionally disabled, keep this sensitive
	// endpoint local-only. Other API endpoints can still be exposed explicitly.
	if len(h.config.APIKeys()) == 0 && !isLoopbackRemote(r.RemoteAddr) {
		http.Error(w, `{"error":{"message":"token endpoint is local-only without API authentication","type":"forbidden"}}`, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TokenResponse{
		Token: h.state.GetCopilotToken(),
	})
}

func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
