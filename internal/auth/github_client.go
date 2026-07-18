package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

type CopilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	RefreshIn int    `json:"refresh_in"`
}

func FetchCopilotToken(githubToken, vsCodeVersion string) (*CopilotTokenResponse, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		return nil, fmt.Errorf("creating copilot token request: %w", err)
	}
	req.Header = api.BuildGitHubHeaders(githubToken, vsCodeVersion)
	resp, err := api.HTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching copilot token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("copilot token request failed (%d): %s", resp.StatusCode, string(body))
	}
	var result CopilotTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding copilot token response: %w", err)
	}
	return &result, nil
}

func GetUser(githubToken, vsCodeVersion string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", fmt.Errorf("creating user request: %w", err)
	}
	req.Header = api.BuildGitHubHeaders(githubToken, vsCodeVersion)
	resp, err := api.HTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("user request failed with status %d", resp.StatusCode)
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("decoding user response: %w", err)
	}
	return user.Login, nil
}
