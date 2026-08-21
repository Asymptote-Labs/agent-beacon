package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var platformFlag string

// The endpoint settings a hook needs, as flags.
//
// They already existed as environment variables, and still do -- an already-installed hook carries
// them as an inline `VAR=value cmd` prefix and must keep working. Flags exist because that prefix is
// a POSIX shell construct: it is not valid in cmd.exe or PowerShell, so on Windows there is no way
// to put the values in the environment from inside a hook command string at all.
//
// Passing them as arguments removes the shell from the equation entirely. One argv, no quoting rules
// beyond the path itself, and the same command works whichever shell a runtime happens to invoke it
// with -- which is also a simplification on POSIX, where the only reason a shell was involved was to
// carry these three values.
var (
	logFlag    string
	configFlag string
	cliFlag    string
)

var rootCmd = &cobra.Command{
	Use:   "beacon-hooks",
	Short: "Beacon hooks for Claude Code, Codex, GitHub Copilot, Cursor, VS Code, Devin, Factory, Grok, Hermes, Antigravity, and opencode",
	Long: `Beacon hooks binary for Claude Code, Codex, GitHub Copilot, Cursor, VS Code, Devin, Factory, Grok, Hermes, Antigravity, and opencode integration.

This binary provides hook commands that are called by IDE plugin systems
to evaluate code changes for security violations.

Use --platform to specify the calling platform (default: claude).`,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&platformFlag, "platform", "claude", "Platform context: claude, codex, antigravity, copilot, cursor, vscode, devin, devin-cli, devin-desktop, factory, grok, hermes, or opencode")
	rootCmd.PersistentFlags().StringVar(&logFlag, "log", "", "Endpoint runtime log to append to (same value as BEACON_ENDPOINT_LOG)")
	rootCmd.PersistentFlags().StringVar(&configFlag, "config", "", "Endpoint config to read (same value as BEACON_ENDPOINT_CONFIG)")
	rootCmd.PersistentFlags().StringVar(&cliFlag, "cli", "", "Path to the beacon CLI for inventory heartbeats (same value as BEACON_ENDPOINT_CLI)")
	rootCmd.PersistentPreRun = func(*cobra.Command, []string) { applyEndpointFlagsToEnv() }
}

// applyEndpointFlagsToEnv puts flag values where the rest of the binary already looks for them.
//
// Deliberately a translation into the environment rather than a config struct threaded through every
// reader. These values are read in eight places across three packages -- logging, cloudshuttle, the
// event commands -- and every one of them reads the environment today. Rewriting all of them to
// accept a parameter would be a large mechanical change whose only purpose is plumbing, and it would
// leave two ways to answer the same question during the transition. One source of truth is worth
// more here than avoiding a Setenv in a process that exists for a few milliseconds and does one job.
//
// It also means a child process inherits them, which is what the inventory heartbeat subprocess
// needs anyway.
//
// A flag beats an inherited variable. Both can be present -- an already-installed POSIX hook carries
// the env prefix, and a repair rewrites the command to use flags -- and when they disagree the flag
// is the more specific statement of intent, because something wrote it into this exact command.
func applyEndpointFlagsToEnv() {
	for _, mapping := range []struct{ value, key string }{
		{logFlag, "BEACON_ENDPOINT_LOG"},
		{configFlag, "BEACON_ENDPOINT_CONFIG"},
		{cliFlag, "BEACON_ENDPOINT_CLI"},
	} {
		if value := strings.TrimSpace(mapping.value); value != "" {
			_ = os.Setenv(mapping.key, value)
		}
	}

	// Any of them implies endpoint mode, which is the condition several readers gate on before
	// falling back to a default log path. Beacon only ever passes these when it installed the hook
	// for an endpoint, so the presence of one is the same signal BEACON_ENDPOINT_MODE=1 carried --
	// and inferring it here means the emitted command does not have to spend an argument saying so.
	if strings.TrimSpace(logFlag) == "" && strings.TrimSpace(configFlag) == "" && strings.TrimSpace(cliFlag) == "" {
		return
	}
	if os.Getenv("BEACON_ENDPOINT_MODE") == "" {
		_ = os.Setenv("BEACON_ENDPOINT_MODE", "1")
	}
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
