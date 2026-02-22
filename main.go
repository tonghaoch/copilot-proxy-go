package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/auth"
	"github.com/tonghaoch/copilot-proxy-go/internal/server"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

var version = "dev"

func main() {
	rootCmd := &cobra.Command{
		Use:     "copilot-proxy-go",
		Short:   "Turn GitHub Copilot into an OpenAI/Anthropic API compatible server",
		Version: version,
	}

	rootCmd.AddCommand(startCmd())
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func startCmd() *cobra.Command {
	var (
		port        int
		githubToken string
		accountType string
		showToken   bool
		verbose     bool
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the Copilot API proxy server",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Configure logging
			logLevel := slog.LevelInfo
			if verbose {
				logLevel = slog.LevelDebug
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: logLevel,
			})))

			// Set global state
			state.Global.SetAccountType(accountType)
			state.Global.SetShowToken(showToken)
			state.Global.SetVerbose(verbose)

			slog.Info("copilot-proxy-go", "version", version)

			// Fetch VS Code version in background
			slog.Info("fetching VS Code version...")
			vsCodeVersion := api.FetchVSCodeVersion()
			state.Global.SetVSCodeVersion(vsCodeVersion)

			// Authenticate
			if err := auth.SetupAuth(githubToken); err != nil {
				return fmt.Errorf("authentication failed: %w", err)
			}

			// Fetch and cache models
			slog.Info("fetching available models...")
			models, err := service.FetchModels()
			if err != nil {
				return fmt.Errorf("failed to fetch models: %w", err)
			}
			state.Global.SetModels(models)

			modelIDs := make([]string, len(models))
			for i, m := range models {
				modelIDs[i] = m.ID
			}
			slog.Info("models loaded", "count", len(models), "ids", modelIDs)

			// Start server
			fmt.Println()
			fmt.Printf("  Copilot API proxy is running on http://localhost:%d\n", port)
			fmt.Println()

			srv := server.New(port)
			return srv.ListenAndServe()
		},
	}

	cmd.Flags().IntVarP(&port, "port", "p", 4141, "port to listen on")
	cmd.Flags().StringVarP(&githubToken, "github-token", "g", "", "GitHub OAuth token (skips device code flow)")
	cmd.Flags().StringVarP(&accountType, "account-type", "a", "individual", "Copilot account type: individual, business, enterprise")
	cmd.Flags().BoolVar(&showToken, "show-token", false, "print tokens to console")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")

	return cmd
}
