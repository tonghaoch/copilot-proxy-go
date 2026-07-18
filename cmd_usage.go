package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/auth"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func checkUsageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check-usage",
		Short: "Display current Copilot quota and usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			setupLogging(false)
			if err := state.EnsurePaths(); err != nil {
				return err
			}

			token, err := auth.LoadToken()
			if err != nil || token == "" {
				return fmt.Errorf("no GitHub token found. Run 'auth' first")
			}
			state.Global.SetGithubToken(token)
			state.Global.SetVSCodeVersion(api.FallbackVSCodeVersion)

			req, err := http.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/user", nil)
			if err != nil {
				return err
			}
			req.Header = api.BuildGitHubHeadersFromState()
			resp, err := api.HTTPClient().Do(req)
			if err != nil {
				return fmt.Errorf("failed to fetch usage: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("usage request failed with status %d", resp.StatusCode)
			}

			var usage map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
				return err
			}
			printUsage(usage)
			return nil
		},
	}
}

func printUsage(usage map[string]any) {
	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────┐")
	fmt.Println("  │         Copilot Usage Summary       │")
	fmt.Println("  └─────────────────────────────────────┘")
	fmt.Println()

	if plan, ok := usage["copilot_plan"].(string); ok {
		fmt.Printf("  Plan: %s\n", plan)
	}
	if resetDate, ok := usage["quota_reset_date"].(string); ok {
		fmt.Printf("  Quota resets: %s\n", resetDate)
	}

	if snapshots, ok := usage["quota_snapshots"].(map[string]any); ok {
		for name, snap := range snapshots {
			s, ok := snap.(map[string]any)
			if !ok {
				continue
			}
			fmt.Printf("\n  %s:\n", name)
			if unlimited, _ := s["unlimited"].(bool); unlimited {
				fmt.Println("    Unlimited")
				continue
			}
			total, hasTotal := toInt(s["total"])
			remaining, hasRemaining := toInt(s["remaining"])
			if hasTotal && hasRemaining {
				used := total - remaining
				pctUsed, pctRemaining := float64(0), float64(0)
				if total > 0 {
					pctUsed = float64(used) / float64(total) * 100
					pctRemaining = float64(remaining) / float64(total) * 100
				}
				fmt.Printf("    %d/%d (%.0f%% used, %.0f%% remaining)\n", used, total, pctUsed, pctRemaining)
			} else {
				if hasRemaining {
					fmt.Printf("    Remaining: %d\n", remaining)
				}
				if pct, ok := s["percent_remaining"]; ok {
					fmt.Printf("    Percent remaining: %v%%\n", pct)
				}
			}
		}
	}
	fmt.Println()
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}
