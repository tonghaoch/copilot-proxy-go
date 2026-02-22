package handler

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
)

// Embeddings handles POST /embeddings and /v1/embeddings.
// It proxies the request directly to the Copilot embeddings endpoint.
func Embeddings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	slog.Info("embeddings request")

	resp, err := service.ProxyEmbeddings(body)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
