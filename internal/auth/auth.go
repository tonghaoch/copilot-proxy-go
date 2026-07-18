package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// DeviceCodeResponse is returned by GitHub's device code endpoint.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// AccessTokenResponse is returned by GitHub's access token polling endpoint.
type AccessTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
}

// CopilotTokenResponse is returned by the Copilot token endpoint.
type CopilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	RefreshIn int    `json:"refresh_in"`
}

// RequestDeviceCode initiates the GitHub OAuth device code flow.
func RequestDeviceCode() (*DeviceCodeResponse, error) {
	data := url.Values{
		"client_id": {api.GitHubClientID},
		"scope":     {api.GitHubScope},
	}

	req, err := http.NewRequest(http.MethodPost, "https://github.com/login/device/code", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := api.HTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed with status %d", resp.StatusCode)
	}

	var result DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding device code response: %w", err)
	}
	return &result, nil
}

// PollAccessToken polls GitHub until the user authorizes the device code.
func PollAccessToken(deviceCode string, interval int) (string, error) {
	pollInterval := time.Duration(interval+1) * time.Second

	for {
		time.Sleep(pollInterval)

		data := url.Values{
			"client_id":   {api.GitHubClientID},
			"device_code": {deviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}

		req, err := http.NewRequest(http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(data.Encode()))
		if err != nil {
			return "", fmt.Errorf("creating poll request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")

		resp, err := api.HTTPClient().Do(req)
		if err != nil {
			return "", fmt.Errorf("polling access token: %w", err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var result AccessTokenResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return "", fmt.Errorf("decoding poll response: %w", err)
		}

		switch result.Error {
		case "":
			if result.AccessToken != "" {
				return result.AccessToken, nil
			}
		case "authorization_pending":
			continue
		case "slow_down":
			pollInterval += 5 * time.Second
			continue
		case "expired_token":
			return "", fmt.Errorf("device code expired, please try again")
		case "access_denied":
			return "", fmt.Errorf("authorization denied by user")
		default:
			return "", fmt.Errorf("unexpected error: %s", result.Error)
		}
	}
}

// FetchCopilotToken exchanges a GitHub token for a Copilot API token.
func FetchCopilotToken(githubToken, vsCodeVersion string) (*CopilotTokenResponse, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		return nil, fmt.Errorf("creating copilot token request: %w", err)
	}

	headers := api.BuildGitHubHeaders(githubToken, vsCodeVersion)
	req.Header = headers

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

// GetUser fetches the authenticated GitHub user's login.
func GetUser(githubToken, vsCodeVersion string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", fmt.Errorf("creating user request: %w", err)
	}

	headers := api.BuildGitHubHeaders(githubToken, vsCodeVersion)
	req.Header = headers

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

// SaveToken writes the GitHub token to disk.
func SaveToken(token string) error {
	return os.WriteFile(state.TokenPath(), []byte(token), 0600)
}

// LoadToken reads the GitHub token from disk.
func LoadToken() (string, error) {
	data, err := os.ReadFile(state.TokenPath())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// SetupAuth orchestrates the full authentication flow:
// 1. Use provided token, or load from file, or run device code flow
// 2. Store token in state and on disk
// 3. Fetch Copilot token
// 4. Start auto-refresh
func SetupAuth(providedToken string) error {
	if err := state.EnsurePaths(); err != nil {
		return fmt.Errorf("ensuring paths: %w", err)
	}

	githubToken := providedToken

	// Try loading from file if not provided
	if githubToken == "" {
		loaded, err := LoadToken()
		if err == nil && loaded != "" {
			githubToken = loaded
			slog.Info("loaded GitHub token from file")
		}
	}

	// Run device code flow if still no token
	if githubToken == "" {
		slog.Info("no GitHub token found, starting device code flow...")
		dc, err := RequestDeviceCode()
		if err != nil {
			return fmt.Errorf("requesting device code: %w", err)
		}

		fmt.Println()
		fmt.Printf("  Please visit: %s\n", dc.VerificationURI)
		fmt.Printf("  Enter code:   %s\n", dc.UserCode)
		fmt.Println()

		token, err := PollAccessToken(dc.DeviceCode, dc.Interval)
		if err != nil {
			return fmt.Errorf("polling access token: %w", err)
		}
		githubToken = token
		slog.Info("GitHub authorization successful")
	}

	// Save token to disk
	if err := SaveToken(githubToken); err != nil {
		slog.Warn("failed to save GitHub token", "error", err)
	}

	state.Global.SetGithubToken(githubToken)
	vsCodeVersion := state.Global.GetVSCodeVersion()

	if state.Global.GetShowToken() {
		slog.Info("GitHub token", "token", githubToken)
	}

	// Fetch initial Copilot token
	copilotToken, err := FetchCopilotToken(githubToken, vsCodeVersion)
	if err != nil {
		return fmt.Errorf("fetching copilot token: %w", err)
	}
	state.Global.SetCopilotToken(copilotToken.Token)

	if state.Global.GetShowToken() {
		slog.Info("Copilot token", "token", copilotToken.Token)
	}

	// Start auto-refresh
	StartTokenRefresh(copilotToken.ExpiresAt, copilotToken.RefreshIn)

	return nil
}

// refreshMu protects against concurrent token refreshes.
var refreshMu sync.Mutex

// lastRefresh tracks when the token was last refreshed to avoid redundant refreshes.
var lastRefresh time.Time

// RefreshCopilotTokenNow immediately refreshes the Copilot token.
// It is safe for concurrent callers — only one refresh will occur at a time,
// and if a refresh happened within the last 30 seconds, it is skipped.
// Returns nil if the token was refreshed (or recently refreshed), error otherwise.
func RefreshCopilotTokenNow() error {
	refreshMu.Lock()
	defer refreshMu.Unlock()

	// Skip if refreshed recently (avoid thundering herd)
	if time.Since(lastRefresh) < 30*time.Second {
		return nil
	}

	githubToken := state.Global.GetGithubToken()
	vsCodeVersion := state.Global.GetVSCodeVersion()

	slog.Info("immediate Copilot token refresh triggered")
	copilotToken, err := FetchCopilotToken(githubToken, vsCodeVersion)
	if err != nil {
		return fmt.Errorf("refreshing copilot token: %w", err)
	}

	state.Global.SetCopilotToken(copilotToken.Token)
	lastRefresh = time.Now()
	slog.Info("Copilot token refreshed successfully (immediate)")
	return nil
}

// StartTokenRefresh starts a goroutine that refreshes the Copilot token periodically.
// It uses expires_at (unix timestamp) to calculate refresh time, falling back to refresh_in.
func StartTokenRefresh(expiresAt int64, refreshIn int) {
	refreshDuration := calcRefreshDuration(expiresAt, refreshIn)

	go func() {
		for {
			slog.Info("next Copilot token refresh scheduled", "in", refreshDuration.Round(time.Second))
			time.Sleep(refreshDuration)

			githubToken := state.Global.GetGithubToken()
			vsCodeVersion := state.Global.GetVSCodeVersion()

			slog.Info("refreshing Copilot token...")
			copilotToken, err := FetchCopilotToken(githubToken, vsCodeVersion)
			if err != nil {
				slog.Error("failed to refresh Copilot token", "error", err)
				// Retry in 30 seconds on failure
				refreshDuration = 30 * time.Second
				continue
			}

			state.Global.SetCopilotToken(copilotToken.Token)

			// Update lastRefresh so RefreshCopilotTokenNow knows
			refreshMu.Lock()
			lastRefresh = time.Now()
			refreshMu.Unlock()

			if state.Global.GetShowToken() {
				slog.Info("refreshed Copilot token", "token", copilotToken.Token)
			} else {
				slog.Info("Copilot token refreshed successfully")
			}

			// Recalculate refresh interval
			refreshDuration = calcRefreshDuration(copilotToken.ExpiresAt, copilotToken.RefreshIn)
		}
	}()
}

// calcRefreshDuration determines how long to wait before refreshing the token.
// Prefers expires_at (refresh 2 minutes before expiry), falls back to refresh_in - 60s.
func calcRefreshDuration(expiresAt int64, refreshIn int) time.Duration {
	const minDuration = 30 * time.Second

	// Primary: use expires_at if available
	if expiresAt > 0 {
		untilExpiry := time.Until(time.Unix(expiresAt, 0))
		d := untilExpiry - 2*time.Minute // refresh 2 minutes before expiry
		if d < minDuration {
			d = minDuration
		}
		slog.Info("token refresh timing",
			"expires_at", time.Unix(expiresAt, 0).Format(time.RFC3339),
			"until_expiry", untilExpiry.Round(time.Second),
			"refresh_in_from_api", refreshIn,
			"chosen_wait", d.Round(time.Second))
		return d
	}

	// Fallback: use refresh_in
	d := time.Duration(refreshIn-60) * time.Second
	if d < minDuration {
		d = minDuration
	}
	return d
}
