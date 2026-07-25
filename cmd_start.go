package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/auth"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/logger"
	"github.com/tonghaoch/copilot-proxy-go/internal/server"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func startCmd() *cobra.Command {
	var (
		host        string
		port        int
		githubToken string
		accountType string
		showToken   bool
		verbose     bool
		claudeCode  bool
		codex       bool
		proxyEnv    bool
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the Copilot API proxy server",
		RunE: func(cmd *cobra.Command, args []string) error {
			appCtx, cancelApp := context.WithCancel(cmd.Context())
			defer cancelApp()
			if claudeCode && codex {
				return fmt.Errorf("--claude-code and --codex cannot be used together; run one setup at a time")
			}

			setupLogging(verbose)
			state.Global.SetAccountType(accountType)
			state.Global.SetShowToken(showToken)
			state.Global.SetVerbose(verbose)

			slog.Info("copilot-proxy-go v" + version)

			if err := state.EnsurePaths(); err != nil {
				return fmt.Errorf("failed to create app directories: %w", err)
			}

			if err := config.Load(); err != nil {
				slog.Warn("failed to load config, using defaults: " + err.Error())
			}
			config.MergeDefaults()

			if proxyEnv {
				setupProxy()
			}

			vsVer := api.FetchVSCodeVersion()
			state.Global.SetVSCodeVersion(vsVer)
			slog.Info("VS Code version: " + vsVer)

			if err := auth.SetupAuthContext(appCtx, githubToken); err != nil {
				return fmt.Errorf("authentication failed: %w", err)
			}

			copilotClient := service.NewClientWithOptions(service.ClientOptions{
				HTTPClient:   api.HTTPClient(),
				RefreshToken: auth.RefreshCopilotTokenNow,
				BuildHeaders: func() http.Header {
					return api.BuildCopilotHeaders(state.Global.GetCopilotToken(), state.Global.GetVSCodeVersion())
				},
				BuildURL: func(path string) string {
					return api.GetBaseURL(state.Global.GetAccountType()) + path
				},
			})

			slog.Info("fetching models...")
			models, err := copilotClient.FetchModels(appCtx)
			if err != nil {
				return fmt.Errorf("failed to fetch models: %w", err)
			}
			state.Global.SetModels(models)
			slog.Info("models loaded", "count", len(models))

			if claudeCode {
				if err := runClaudeCodeSetup(port, models); err != nil {
					slog.Warn("claude-code setup failed", "error", err)
				}
			}
			if codex {
				if err := runCodexSetup(port, models); err != nil {
					slog.Warn("codex setup failed", "error", err)
				}
			}

			listenHost := host
			if listenHost == "" {
				listenHost = "127.0.0.1"
			}
			fmt.Println()
			fmt.Printf("  Copilot API proxy is running on http://%s:%d\n", listenHost, port)
			fmt.Printf("  Dashboard: http://%s:%d/dashboard?endpoint=http://%s:%d/usage\n", listenHost, port, listenHost, port)
			fmt.Println()

			endpoints := handler.New(handler.Dependencies{
				State: state.Global, Metrics: state.Metrics, Copilot: copilotClient, HTTP: api.HTTPClient(),
			})
			srv := server.NewWithHandler(server.Options{
				Host: host,
				Port: port,
			}, endpoints)

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			go func() {
				<-sigCh
				cancelApp()
				slog.Info("shutting down (30s timeout for in-flight requests)...")
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				srv.Shutdown(ctx)
				logger.CloseAll()
			}()

			err = srv.ListenAndServe()
			if err == http.ErrServerClosed {
				return nil
			}
			return err
		},
	}

	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "host/IP to bind to (use 0.0.0.0 for all interfaces)")
	cmd.Flags().IntVarP(&port, "port", "p", 4141, "port to listen on")
	cmd.Flags().StringVarP(&githubToken, "github-token", "g", "", "GitHub OAuth token (skips device code flow)")
	cmd.Flags().StringVarP(&accountType, "account-type", "a", "individual", "Copilot account type: individual, business, enterprise")
	cmd.Flags().BoolVar(&showToken, "show-token", false, "print tokens to console")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")
	cmd.Flags().BoolVarP(&claudeCode, "claude-code", "c", false, "interactive model selection + env var generation for Claude Code")
	cmd.Flags().BoolVar(&codex, "codex", false, "interactive model selection + command generation for Codex CLI")
	cmd.Flags().BoolVar(&proxyEnv, "proxy-env", false, "enable HTTP proxy from environment variables")

	return cmd
}
