package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var rootCmd = &cobra.Command{
	Use:   "beacon",
	Short: "Beacon Endpoint Agent - local AI runtime telemetry",
	Long: `Beacon Endpoint Agent discovers local AI agent runtimes, configures
local telemetry, and writes Wazuh-compatible JSON logs without requiring an
Beacon-hosted backend.`,
	Version: version.GetVersion(),
	Run: func(cmd *cobra.Command, args []string) {
		printRootSplash(cmd)
	},
}

const (
	beaconPurple = "\x1b[38;5;141m"
	resetColor   = "\x1b[0m"
)

var beaconBanner = []string{
	"██████╗ ███████╗ █████╗  ██████╗ ██████╗ ███╗   ██╗",
	"██╔══██╗██╔════╝██╔══██╗██╔════╝██╔═══██╗████╗  ██║",
	"██████╔╝█████╗  ███████║██║     ██║   ██║██╔██╗ ██║",
	"██╔══██╗██╔══╝  ██╔══██║██║     ██║   ██║██║╚██╗██║",
	"██████╔╝███████╗██║  ██║╚██████╗╚██████╔╝██║ ╚████║",
	"╚═════╝ ╚══════╝╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚═╝  ╚═══╝",
}

func printRootSplash(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	printBeaconBanner(out, shouldUseColor(out))
	fmt.Fprintln(out, "Open-source telemetry layer for AI agents.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Start with:")
	fmt.Fprintln(out, "  beacon endpoint install")
	fmt.Fprintln(out, "  beacon endpoint status")
	fmt.Fprintln(out, "  beacon endpoint wazuh print-config")
	fmt.Fprintln(out)
	_ = cmd.Usage()
}

func printBeaconBanner(out io.Writer, color bool) {
	if color {
		fmt.Fprint(out, beaconPurple)
	}
	for _, line := range beaconBanner {
		fmt.Fprintln(out, line)
	}
	if color {
		fmt.Fprint(out, resetColor)
	}
}

func shouldUseColor(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate shell completion scripts",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return rootCmd.GenBashCompletion(os.Stdout)
		case "zsh":
			return rootCmd.GenZshCompletion(os.Stdout)
		case "fish":
			return rootCmd.GenFishCompletion(os.Stdout, true)
		case "powershell":
			return rootCmd.GenPowerShellCompletion(os.Stdout)
		default:
			return fmt.Errorf("unsupported shell %q", args[0])
		}
	},
}

var docsCmd = &cobra.Command{
	Use:          "docs --output <dir>",
	Short:        "Generate command reference markdown",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		output, err := cmd.Flags().GetString("output")
		if err != nil {
			return err
		}
		if output == "" {
			return fmt.Errorf("--output is required")
		}
		if err := os.MkdirAll(output, 0755); err != nil {
			return err
		}
		return doc.GenMarkdownTree(rootCmd, output)
	},
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	// Set version template
	rootCmd.SetVersionTemplate(`{{printf "beacon version %s\n" .Version}}`)

	// Add version flag shorthand
	rootCmd.Flags().BoolP("version", "v", false, "Print the version number")
	docsCmd.Flags().String("output", "", "Output directory for markdown command docs")
	rootCmd.AddCommand(completionCmd)
	rootCmd.AddCommand(docsCmd)
}
