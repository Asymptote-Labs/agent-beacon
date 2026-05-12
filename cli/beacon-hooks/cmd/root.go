package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var platformFlag string

var rootCmd = &cobra.Command{
	Use:   "beacon-hooks",
	Short: "Beacon hooks for Claude Code, GitHub Copilot, Cursor, and Factory",
	Long: `Beacon hooks binary for Claude Code, GitHub Copilot, Cursor, and Factory integration.

This binary provides hook commands that are called by IDE plugin systems
to evaluate code changes for security violations.

Use --platform to specify the calling platform (default: claude).`,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&platformFlag, "platform", "claude", "Platform context: claude, copilot, cursor, or factory")
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
