package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
	"golang.org/x/term"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
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
		maybePrintRootUpdateNotice(cmd)
		printRootSplash(cmd)
	},
}

const (
	beaconPurple = "\x1b[38;5;141m"
	beaconOrange = "\x1b[38;5;208m"
	resetColor   = "\x1b[0m"

	beaconCLIChangelogURL = "https://docs.beacon.sh/changelog/cli"
	beaconReleasesURL     = "https://github.com/asymptote-labs/agent-beacon/releases/latest"
)

var (
	rootNow                = time.Now
	rootStartUpdateRefresh = startDetachedUpdateRefresh
	rootDetectInstall      = selfupdate.DetectInstall
)

var beaconBanner = []string{
	"██████╗ ███████╗ █████╗  ██████╗ ██████╗ ███╗   ██╗",
	"██╔══██╗██╔════╝██╔══██╗██╔════╝██╔═══██╗████╗  ██║",
	"██████╔╝█████╗  ███████║██║     ██║   ██║██╔██╗ ██║",
	"██╔══██╗██╔══╝  ██╔══██║██║     ██║   ██║██║╚██╗██║",
	"██████╔╝███████╗██║  ██║╚██████╗╚██████╔╝██║ ╚████║",
	"╚═════╝ ╚══════╝╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚═╝  ╚═══╝",
}

// maybePrintRootUpdateNotice follows the Codex/Claude Code pattern: the notice comes from the
// cached result of an earlier lookup, so bare `beacon` never waits on the network. When the
// cache is older than updatecheck.CacheTTL it starts a detached `beacon version refresh` that
// updates the cache for the next run. BEACON_UPDATE_CHECK=0 or CI turns all of it off.
func maybePrintRootUpdateNotice(cmd *cobra.Command) {
	in, inOK := cmd.InOrStdin().(*os.File)
	out, outOK := cmd.OutOrStdout().(*os.File)
	if !inOK || !outOK || !isTerminal(in) || !isTerminal(out) {
		return
	}
	showCachedUpdateNotice(out, shouldUseColor(out), os.Getenv)
}

func showCachedUpdateNotice(out io.Writer, color bool, getenv func(string) string) {
	current := version.GetVersion()
	if updatecheck.Disabled(getenv) || !updatecheck.CanCheckVersion(current) {
		return
	}

	path := updateCachePath()
	cache := updatecheck.LoadCache(path)
	if cache.Stale(rootNow()) {
		_ = rootStartUpdateRefresh()
	}
	result := cache.Notice(current)
	if !result.UpdateAvailable {
		return
	}
	printRootUpdateNotice(out, result, color, rootDetectInstall())
}

func updateCachePath() string {
	return filepath.Join(endpointconfig.BaseDir(true), updatecheck.CacheFile)
}

// startDetachedUpdateRefresh runs `beacon version refresh` in the background with no standard
// streams, so it outlives this process and never writes to the terminal.
func startDetachedUpdateRefresh() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	child := exec.Command(exe, "version", "refresh")
	child.Stdin, child.Stdout, child.Stderr = nil, nil, nil
	// Its own session, so closing the terminal right after `beacon` does not kill the lookup.
	child.SysProcAttr = service.DetachedProcAttrs()
	if err := child.Start(); err != nil {
		return err
	}
	return child.Process.Release()
}

func printRootUpdateNotice(out io.Writer, result updatecheck.Result, color bool, install selfupdate.Install) {
	if color {
		fmt.Fprint(out, beaconOrange)
	}
	fmt.Fprintf(out, "Update available · %s → %s\n", result.CurrentVersion, result.LatestVersion)
	if color {
		fmt.Fprint(out, resetColor)
	}
	fmt.Fprintf(out, "Release notes: %s\n", beaconCLIChangelogURL)
	switch install.Kind {
	case selfupdate.InstallHomebrew:
		fmt.Fprintln(out, "Update with: brew update && brew upgrade beacon")
	case selfupdate.InstallSystemPkg:
		fmt.Fprintln(out, "Update with: sudo beacon endpoint update --apply")
	default:
		// Tarballs, install.sh, go install, the MSI and source builds have no in-place
		// updater: `beacon endpoint update` only applies to the system package.
		fmt.Fprintf(out, "Download: %s\n", releaseDownloadURL(result))
	}
	fmt.Fprintln(out)
}

func releaseDownloadURL(result updatecheck.Result) string {
	if result.ReleaseURL != "" {
		return result.ReleaseURL
	}
	return beaconReleasesURL
}

func printRootSplash(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	cmd.SetOut(out)
	printBeaconBanner(out, shouldUseColor(out))
	fmt.Fprintln(out, "Self-improving memory for coding agents.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Start with:")
	fmt.Fprintln(out, "  beacon endpoint install")
	fmt.Fprintln(out, "  beacon traces")
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
	return isTerminal(file)
}

// isTerminal reports whether a file is an interactive terminal rather than a pipe,
// a redirect, or a file.
//
// Onboarding and the trace browser depend on this to stay out of the way of every
// non-interactive path: package postinstall scripts, MDM helpers, CI, and the
// packaging smoke tests all reach `endpoint install` without a terminal, and a
// full-screen prompt there would hang a fleet deployment.
//
// This used to test os.ModeCharDevice, which is also set on /dev/null and /dev/zero.
// `beacon endpoint install < /dev/null` therefore looked interactive, launched the
// wizard against an input that was already at EOF, and failed the install with
// "onboarding cancelled" -- the exact deployment shape the check exists to protect.
// A real isatty has no such hole.
func isTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
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
