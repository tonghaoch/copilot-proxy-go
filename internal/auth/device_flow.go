package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type AccessTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
}

func RequestDeviceCode() (*DeviceCodeResponse, error) {
	data := url.Values{"client_id": {api.GitHubClientID}, "scope": {api.GitHubScope}}
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

func PollAccessToken(deviceCode string, interval int) (string, error) {
	pollInterval := time.Duration(interval+1) * time.Second
	for {
		time.Sleep(pollInterval)
		data := url.Values{
			"client_id": {api.GitHubClientID}, "device_code": {deviceCode},
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
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
