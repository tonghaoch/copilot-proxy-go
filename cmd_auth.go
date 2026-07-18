package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/tonghaoch/copilot-proxy-go/internal/auth"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func authCmd() *cobra.Command {
	var (
		verbose   bool
		showToken bool
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Run GitHub OAuth device-code flow to generate a token",
		RunE: func(cmd *cobra.Command, args []string) error {
			setupLogging(verbose)
			state.Global.SetShowToken(showToken)

			if err := state.EnsurePaths(); err != nil {
				return err
			}

			if force {
				os.Remove(state.TokenPath())
				slog.Info("cleared existing token, forcing re-authentication")
			}

			slog.Info("starting authentication...")
			if err := auth.SetupAuth(""); err != nil {
				return fmt.Errorf("authentication failed: %w", err)
			}

			fmt.Println("\n  Authentication successful! Token saved.")
			fmt.Printf("  Token path: %s\n\n", state.TokenPath())
			return nil
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")
	cmd.Flags().BoolVar(&showToken, "show-token", false, "print token to console")
	cmd.Flags().BoolVar(&force, "force", false, "force re-authentication even if token exists")
	return cmd
}
