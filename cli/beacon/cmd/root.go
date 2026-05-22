package cmd

import (
	"fmt"
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
		fmt.Println("Beacon Endpoint Agent")
		fmt.Println()
		fmt.Println("Start with:")
		fmt.Println("  beacon endpoint install")
		fmt.Println("  beacon endpoint status")
		fmt.Println("  beacon endpoint wazuh print-config")
		fmt.Println()
		cmd.Usage()
	},
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
