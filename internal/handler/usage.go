package handler

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// Usage handles GET /usage — returns Copilot quota/usage information.
func Usage(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/user", nil)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	req.Header = api.BuildGitHubHeaders(
		state.Global.GetGithubToken(),
		state.Global.GetVSCodeVersion(),
	)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("failed to fetch usage", "status", resp.StatusCode)
		api.ForwardError(w, api.NewHTTPError(resp))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, resp.Body)
}
