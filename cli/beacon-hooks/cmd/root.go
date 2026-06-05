package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var platformFlag string

var rootCmd = &cobra.Command{
	Use:   "beacon-hooks",
	Short: "Beacon hooks for Claude Code, GitHub Copilot, Cursor, VS Code, Devin, Factory, Grok, Hermes, Antigravity, and opencode",
	Long: `Beacon hooks binary for Claude Code, GitHub Copilot, Cursor, VS Code, Devin, Factory, Grok, Hermes, Antigravity, and opencode integration.

This binary provides hook commands that are called by IDE plugin systems
to evaluate code changes for security violations.

Use --platform to specify the calling platform (default: claude).`,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&platformFlag, "platform", "claude", "Platform context: claude, antigravity, copilot, cursor, vscode, devin, devin-cli, devin-desktop, factory, grok, hermes, or opencode")
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
