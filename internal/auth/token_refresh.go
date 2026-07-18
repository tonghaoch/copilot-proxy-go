package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

var (
	refreshMu   sync.Mutex
	lastRefresh time.Time
)

func RefreshCopilotTokenNow() error {
	refreshMu.Lock()
	defer refreshMu.Unlock()
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

func StartTokenRefresh(expiresAt int64, refreshIn int) {
	StartTokenRefreshContext(context.Background(), expiresAt, refreshIn)
}

// StartTokenRefreshContext refreshes tokens until the application context is
// cancelled. Timers, rather than time.Sleep, make shutdown immediate.
func StartTokenRefreshContext(ctx context.Context, expiresAt int64, refreshIn int) {
	refreshDuration := calcRefreshDuration(expiresAt, refreshIn)
	go func() {
		for {
			slog.Info("next Copilot token refresh scheduled", "in", refreshDuration.Round(time.Second))
			timer := time.NewTimer(refreshDuration)
			select {
			case <-ctx.Done():
				timer.Stop()
				slog.Debug("token refresh stopped", "reason", ctx.Err())
				return
			case <-timer.C:
			}
			githubToken := state.Global.GetGithubToken()
			vsCodeVersion := state.Global.GetVSCodeVersion()
			slog.Info("refreshing Copilot token...")
			copilotToken, err := FetchCopilotToken(githubToken, vsCodeVersion)
			if err != nil {
				slog.Error("failed to refresh Copilot token", "error", err)
				refreshDuration = 30 * time.Second
				continue
			}
			state.Global.SetCopilotToken(copilotToken.Token)
			refreshMu.Lock()
			lastRefresh = time.Now()
			refreshMu.Unlock()
			if state.Global.GetShowToken() {
				slog.Info("refreshed Copilot token", "token", copilotToken.Token)
			} else {
				slog.Info("Copilot token refreshed successfully")
			}
			refreshDuration = calcRefreshDuration(copilotToken.ExpiresAt, copilotToken.RefreshIn)
		}
	}()
}

func calcRefreshDuration(expiresAt int64, refreshIn int) time.Duration {
	const minDuration = 30 * time.Second
	if expiresAt > 0 {
		untilExpiry := time.Until(time.Unix(expiresAt, 0))
		d := untilExpiry - 2*time.Minute
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
	d := time.Duration(refreshIn-60) * time.Second
	if d < minDuration {
		d = minDuration
	}
	return d
}
