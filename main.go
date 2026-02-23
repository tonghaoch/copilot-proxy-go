package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/auth"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/logger"
	"github.com/tonghaoch/copilot-proxy-go/internal/server"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/shell"
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
	rootCmd.AddCommand(authCmd())
	rootCmd.AddCommand(checkUsageCmd())
	rootCmd.AddCommand(debugCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// --- start command ---

func startCmd() *cobra.Command {
	var (
		port             int
		githubToken      string
		accountType      string
		showToken        bool
		verbose          bool
		manualApprove    bool
		rateLimitSeconds int
		rateLimitWait    bool
		claudeCode       bool
		proxyEnv         bool
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the Copilot API proxy server",
		RunE: func(cmd *cobra.Command, args []string) error {
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

			// Proxy support
			if proxyEnv {
				setupProxy()
			}

			// Signal handler
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			go func() {
				<-sigCh
				slog.Info("shutting down...")
				logger.CloseAll()
				os.Exit(0)
			}()

			// VS Code version
			vsVer := api.FetchVSCodeVersion()
			state.Global.SetVSCodeVersion(vsVer)
			slog.Info("VS Code version: " + vsVer)

			// Auth
			if err := auth.SetupAuth(githubToken); err != nil {
				return fmt.Errorf("authentication failed: %w", err)
			}

			// Models
			slog.Info("fetching models...")
			models, err := service.FetchModels()
			if err != nil {
				return fmt.Errorf("failed to fetch models: %w", err)
			}
			state.Global.SetModels(models)

			fmt.Fprintf(os.Stderr, "\n  Available models (%d):\n", len(models))
			for _, m := range models {
				fmt.Fprintf(os.Stderr, "    • %s\n", m.ID)
			}
			fmt.Fprintln(os.Stderr)

			// Claude Code interactive setup
			if claudeCode {
				if err := runClaudeCodeSetup(port, models); err != nil {
					slog.Warn("claude-code setup failed", "error", err)
				}
			}

			// Start server
			fmt.Println()
			fmt.Printf("  Copilot API proxy is running on http://localhost:%d\n", port)
			fmt.Printf("  Dashboard: http://localhost:%d/dashboard?endpoint=http://localhost:%d/usage\n", port, port)
			fmt.Println()

			srv := server.New(server.Options{
				Port:             port,
				ManualApprove:    manualApprove,
				RateLimitSeconds: rateLimitSeconds,
				RateLimitWait:    rateLimitWait,
			})
			return srv.ListenAndServe()
		},
	}

	cmd.Flags().IntVarP(&port, "port", "p", 4141, "port to listen on")
	cmd.Flags().StringVarP(&githubToken, "github-token", "g", "", "GitHub OAuth token (skips device code flow)")
	cmd.Flags().StringVarP(&accountType, "account-type", "a", "individual", "Copilot account type: individual, business, enterprise")
	cmd.Flags().BoolVar(&showToken, "show-token", false, "print tokens to console")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")
	cmd.Flags().BoolVar(&manualApprove, "manual", false, "require manual CLI approval for each request")
	cmd.Flags().IntVarP(&rateLimitSeconds, "rate-limit", "r", 0, "minimum seconds between requests (0 = disabled)")
	cmd.Flags().BoolVarP(&rateLimitWait, "wait", "w", false, "wait instead of rejecting on rate limit")
	cmd.Flags().BoolVarP(&claudeCode, "claude-code", "c", false, "interactive model selection + env var generation for Claude Code")
	cmd.Flags().BoolVar(&proxyEnv, "proxy-env", false, "enable HTTP proxy from environment variables")

	return cmd
}

// --- auth command ---

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
				// Delete existing token to force re-authentication
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

// --- check-usage command ---

func checkUsageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check-usage",
		Short: "Display current Copilot quota and usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			setupLogging(false)

			if err := state.EnsurePaths(); err != nil {
				return err
			}

			// Load token
			token, err := auth.LoadToken()
			if err != nil || token == "" {
				return fmt.Errorf("no GitHub token found. Run 'auth' first")
			}
			state.Global.SetGithubToken(token)
			state.Global.SetVSCodeVersion(api.FallbackVSCodeVersion)

			// Fetch usage
			req, err := http.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/user", nil)
			if err != nil {
				return err
			}
			req.Header = api.BuildGitHubHeadersFromState()

			resp, err := http.DefaultClient.Do(req)
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

			// Pretty print
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
					} else {
						total, hasTotal := toInt(s["total"])
						remaining, hasRemaining := toInt(s["remaining"])
						if hasTotal && hasRemaining {
							used := total - remaining
							pctUsed := float64(0)
							pctRemaining := float64(0)
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
			}
			fmt.Println()
			return nil
		},
	}

	return cmd
}

// --- debug command ---

func debugCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Print diagnostic information",
		RunE: func(cmd *cobra.Command, args []string) error {
			tokenExists := false
			if _, err := os.Stat(state.TokenPath()); err == nil {
				tokenExists = true
			}

			configExists := false
			if _, err := os.Stat(state.ConfigPath()); err == nil {
				configExists = true
			}

			info := map[string]any{
				"version":       version,
				"runtime":       "go",
				"go_version":    runtime.Version(),
				"platform":      runtime.GOOS,
				"arch":          runtime.GOARCH,
				"app_dir":       state.AppDir(),
				"token_path":    state.TokenPath(),
				"config_path":   state.ConfigPath(),
				"token_exists":  tokenExists,
				"config_exists": configExists,
			}

			if jsonOutput {
				data, _ := json.MarshalIndent(info, "", "  ")
				fmt.Println(string(data))
			} else {
				fmt.Println()
				fmt.Println("  copilot-proxy-go debug info")
				fmt.Println("  ───────────────────────────")
				fmt.Printf("  Version:       %s\n", version)
				fmt.Printf("  Runtime:       Go %s\n", runtime.Version())
				fmt.Printf("  Platform:      %s/%s\n", runtime.GOOS, runtime.GOARCH)
				fmt.Printf("  App dir:       %s\n", state.AppDir())
				fmt.Printf("  Token path:    %s (exists: %v)\n", state.TokenPath(), tokenExists)
				fmt.Printf("  Config path:   %s (exists: %v)\n", state.ConfigPath(), configExists)
				fmt.Println()
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")

	return cmd
}

// --- helpers ---

// toInt converts an any value (typically float64 from JSON) to int.
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

func setupLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(&cleanHandler{level: level}))
}

// cleanHandler is a minimal slog handler that prints "HH:MM:SS message key=val ..."
// without the noisy level prefix.
type cleanHandler struct {
	level slog.Level
}

func (h *cleanHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *cleanHandler) Handle(_ context.Context, r slog.Record) error {
	ts := r.Time.Format("15:04:05")
	var b strings.Builder
	b.WriteString(ts)
	b.WriteByte(' ')
	b.WriteString(r.Message)

	r.Attrs(func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(fmt.Sprintf("%v", a.Value.Any()))
		return true
	})

	fmt.Fprintln(os.Stderr, b.String())
	return nil
}

func (h *cleanHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *cleanHandler) WithGroup(name string) slog.Handler       { return h }

func setupProxy() {
	transport := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	http.DefaultClient.Transport = transport

	proxyVars := []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}
	for _, v := range proxyVars {
		if val := os.Getenv(v); val != "" {
			slog.Info(fmt.Sprintf("proxy: %s=%s", v, val))
		}
	}
}

func runClaudeCodeSetup(port int, models []state.Model) error {
	// Display model list for selection
	fmt.Println()
	fmt.Println("  Select primary model:")
	for i, m := range models {
		fmt.Printf("    %d. %s\n", i+1, m.ID)
	}
	fmt.Print("\n  Enter number: ")

	var primaryIdx int
	if _, err := fmt.Scan(&primaryIdx); err != nil || primaryIdx < 1 || primaryIdx > len(models) {
		return fmt.Errorf("invalid selection")
	}
	primaryModel := models[primaryIdx-1].ID

	fmt.Println("\n  Select small/fast model:")
	for i, m := range models {
		fmt.Printf("    %d. %s\n", i+1, m.ID)
	}
	fmt.Print("\n  Enter number: ")

	var smallIdx int
	if _, err := fmt.Scan(&smallIdx); err != nil || smallIdx < 1 || smallIdx > len(models) {
		return fmt.Errorf("invalid selection")
	}
	smallModel := models[smallIdx-1].ID

	baseURL := fmt.Sprintf("http://localhost:%d", port)

	vars := []shell.EnvVar{
		{Key: "ANTHROPIC_BASE_URL", Value: baseURL},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "copilot-proxy"},
		{Key: "ANTHROPIC_MODEL", Value: primaryModel},
		{Key: "ANTHROPIC_SMALL_FAST_MODEL", Value: smallModel},
		{Key: "ANTHROPIC_DEFAULT_SONNET_MODEL", Value: primaryModel},
		{Key: "ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: smallModel},
		{Key: "DISABLE_NON_ESSENTIAL_MODEL_CALLS", Value: "1"},
		{Key: "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", Value: "1"},
	}

	shellType := shell.Detect()
	script := shell.GenerateExportScript(shellType, vars, "claude")

	fmt.Println()
	fmt.Println("  Generated command:")
	fmt.Println()
	fmt.Printf("  %s\n", script)
	fmt.Println()

	if err := shell.CopyToClipboard(script); err != nil {
		fmt.Println("  (Could not copy to clipboard — paste the command above)")
	} else {
		fmt.Println("  Copied to clipboard!")
	}
	fmt.Println()

	return nil
}

