package main

import (
	"os"

	"github.com/spf13/cobra"
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
