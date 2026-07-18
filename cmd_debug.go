package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

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
				"version": version, "runtime": "go", "go_version": runtime.Version(),
				"platform": runtime.GOOS, "arch": runtime.GOARCH, "app_dir": state.AppDir(),
				"token_path": state.TokenPath(), "config_path": state.ConfigPath(),
				"token_exists": tokenExists, "config_exists": configExists,
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
