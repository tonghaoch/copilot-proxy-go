package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// SetupAuth orchestrates token loading, device authentication, Copilot token
// exchange, and background refresh.
func SetupAuth(providedToken string) error {
	return SetupAuthContext(context.Background(), providedToken)
}

func SetupAuthContext(ctx context.Context, providedToken string) error {
	if err := state.EnsurePaths(); err != nil {
		return fmt.Errorf("ensuring paths: %w", err)
	}

	githubToken := providedToken
	if githubToken == "" {
		loaded, err := LoadToken()
		if err == nil && loaded != "" {
			githubToken = loaded
			slog.Info("loaded GitHub token from file")
		}
	}
	if githubToken == "" {
		var err error
		githubToken, err = authenticateDevice()
		if err != nil {
			return err
		}
	}

	if err := SaveToken(githubToken); err != nil {
		slog.Warn("failed to save GitHub token", "error", err)
	}
	state.Global.SetGithubToken(githubToken)
	if state.Global.GetShowToken() {
		slog.Info("GitHub token", "token", githubToken)
	}

	copilotToken, err := FetchCopilotToken(githubToken, state.Global.GetVSCodeVersion())
	if err != nil {
		return fmt.Errorf("fetching copilot token: %w", err)
	}
	state.Global.SetCopilotToken(copilotToken.Token)
	if state.Global.GetShowToken() {
		slog.Info("Copilot token", "token", copilotToken.Token)
	}
	StartTokenRefreshContext(ctx, copilotToken.ExpiresAt, copilotToken.RefreshIn)
	return nil
}

func authenticateDevice() (string, error) {
	slog.Info("no GitHub token found, starting device code flow...")
	dc, err := RequestDeviceCode()
	if err != nil {
		return "", fmt.Errorf("requesting device code: %w", err)
	}
	fmt.Println()
	fmt.Printf("  Please visit: %s\n", dc.VerificationURI)
	fmt.Printf("  Enter code:   %s\n", dc.UserCode)
	fmt.Println()
	token, err := PollAccessToken(dc.DeviceCode, dc.Interval)
	if err != nil {
		return "", fmt.Errorf("polling access token: %w", err)
	}
	slog.Info("GitHub authorization successful")
	return token, nil
}
