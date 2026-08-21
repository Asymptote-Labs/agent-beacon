package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/spf13/cobra"
)

type gettingStartedOptions struct {
	yes         bool
	noDashboard bool
	noHooks     bool
}

var gettingStartedOpts gettingStartedOptions

var gettingStartedCmd = &cobra.Command{
	Use:          "getting-started",
	Aliases:      []string{"start"},
	Short:        "Discover runtimes and finish setting up Beacon",
	SilenceUsage: true,
	RunE:         runGettingStarted,
}

var (
	gettingStartedDiscover = harness.DiscoverAll
	gettingStartedStatus   = lifecycle.GetStatus
	gettingStartedInstall  = func(target string) error {
		return installEndpointHookTarget(target, loadOrDefaultConfig())
	}
	gettingStartedDashboard = func(cmd *cobra.Command) error {
		endpointOpts.dashboardOpen = true
		return runEndpointDashboard(cmd, nil)
	}
	gettingStartedIsTTY = func() bool {
		return isCharDevice(os.Stdin) && isCharDevice(os.Stdout)
	}
)

func init() {
	gettingStartedCmd.Flags().BoolVarP(&gettingStartedOpts.yes, "yes", "y", false, "Accept recommended setup actions")
	gettingStartedCmd.Flags().BoolVar(&gettingStartedOpts.noDashboard, "no-dashboard", false, "Do not offer to start the local dashboard")
	gettingStartedCmd.Flags().BoolVar(&gettingStartedOpts.noHooks, "no-hooks", false, "Do not offer to install runtime hooks")
	gettingStartedCmd.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
	gettingStartedCmd.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and the system collector service")
	gettingStartedCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
	rootCmd.AddCommand(gettingStartedCmd)
}

func runGettingStarted(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	interactive := gettingStartedOpts.yes || gettingStartedIsTTY()
	reader := bufio.NewReader(cmd.InOrStdin())

	fmt.Fprintln(out, "Beacon getting started")
	fmt.Fprintln(out, "======================")

	status := gettingStartedStatus(endpointUserMode(), endpointOpts.logPath)
	printGettingStartedHealth(out, status)

	discovered := gettingStartedDiscover()
	hookTargets := printGettingStartedRuntimes(out, discovered)
	var problems []string
	if len(hookTargets) > 0 && !gettingStartedOpts.noHooks {
		if !interactive {
			fmt.Fprintf(out, "\nRecommended action: beacon endpoint hooks install --harness %s\n", strings.Join(hookTargets, ","))
		} else if gettingStartedOpts.yes || askYesNo(reader, out, "Install recommended runtime hooks now?", true) {
			fmt.Fprintln(out)
			for _, target := range hookTargets {
				if err := gettingStartedInstall(target); err != nil {
					problems = append(problems, target+": "+err.Error())
					fmt.Fprintf(errOut, "Could not install %s hooks: %v\n", target, err)
					fmt.Fprintf(errOut, "Fix: beacon endpoint hooks install --harness %s\n", target)
				}
			}
		}
	}

	fmt.Fprintln(out, "\nNext steps")
	fmt.Fprintln(out, "  1. Restart any detected agent runtimes that were already open.")
	fmt.Fprintln(out, "  2. Use an agent normally to generate an event.")
	fmt.Fprintf(out, "  3. Check health: beacon endpoint doctor%s\n", modeFlag())
	fmt.Fprintf(out, "  4. Watch events: tail -f %s\n", status.LogPath)
	fmt.Fprintln(out, "  5. Open the dashboard: beacon endpoint dashboard --open")

	if !gettingStartedOpts.noDashboard {
		if !interactive {
			fmt.Fprintln(out, "\nRun 'beacon endpoint dashboard --open' when you are ready to explore events.")
		} else if gettingStartedOpts.yes || askYesNo(reader, out, "Open the local dashboard now?", true) {
			if err := gettingStartedDashboard(cmd); err != nil {
				problems = append(problems, "dashboard: "+err.Error())
				fmt.Fprintf(errOut, "Could not start the dashboard: %v\n", err)
				fmt.Fprintln(errOut, "Fix: beacon endpoint dashboard --open")
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("getting started finished with %d unresolved problem(s)", len(problems))
	}
	return nil
}

func printGettingStartedHealth(out io.Writer, status lifecycle.Status) {
	fmt.Fprintln(out, "\nEndpoint health")
	printCheckLine(out, status.Service.Running, "Collector service is running", "Collector service is not running", "beacon endpoint repair"+modeFlag())
	collectorReady := status.Collector.GRPCReady && status.Collector.HTTPReady
	printCheckLine(out, collectorReady, "OTLP receivers are ready", "OTLP receivers are not ready", "beacon endpoint doctor"+modeFlag())
	if status.LastEvent != "" {
		fmt.Fprintln(out, "  ✓ Runtime log contains events")
	} else {
		fmt.Fprintln(out, "  ! No runtime event has been observed yet")
		fmt.Fprintln(out, "    Next: restart an agent runtime and use it once")
	}
	for _, check := range status.Diagnostics {
		if check.Status == "ok" {
			continue
		}
		fmt.Fprintf(out, "  ! %s: %s\n", check.Name, check.Message)
		if check.Action != "" {
			fmt.Fprintf(out, "    Fix: %s\n", check.Action)
		}
	}
}

func printCheckLine(out io.Writer, ok bool, success, failure, action string) {
	if ok {
		fmt.Fprintf(out, "  ✓ %s\n", success)
		return
	}
	fmt.Fprintf(out, "  ! %s\n", failure)
	if action != "" {
		fmt.Fprintf(out, "    Fix: %s\n", action)
	}
}

func printGettingStartedRuntimes(out io.Writer, discovered []harness.Harness) []string {
	fmt.Fprintln(out, "\nDetected runtimes")
	var hooks []string
	seen := map[string]bool{}
	count := 0
	for _, runtime := range discovered {
		if !runtime.Detected {
			continue
		}
		count++
		fmt.Fprintf(out, "  %-18s telemetry=%s\n", runtime.DisplayName, runtime.TelemetryStatus)
		if target, ok := gettingStartedHookTarget(runtime); ok && runtime.TelemetryStatus != harness.TelemetryEnabled && !seen[target] {
			hooks = append(hooks, target)
			seen[target] = true
		}
	}
	if count == 0 {
		fmt.Fprintln(out, "  No supported agent runtimes detected yet.")
	}
	return hooks
}

func gettingStartedHookTarget(runtime harness.Harness) (string, bool) {
	targets := map[string]string{
		"antigravity_cli": "antigravity",
		"cursor":          "cursor",
		"factory":         "factory",
		"opencode":        "opencode",
		"hermes":          "hermes",
		"devin-cli":       "devin-cli",
		"devin-desktop":   "devin-desktop",
	}
	target, ok := targets[runtime.Name]
	return target, ok
}

func askYesNo(reader *bufio.Reader, out io.Writer, question string, defaultYes bool) bool {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	fmt.Fprintf(out, "\n%s %s ", question, suffix)
	answer, err := reader.ReadString('\n')
	if err != nil && len(answer) == 0 {
		return defaultYes
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return defaultYes
	}
}

func modeFlag() string {
	if endpointUserMode() {
		return ""
	}
	return " --system"
}

func maybeOfferGettingStarted(cmd *cobra.Command) error {
	if !gettingStartedIsTTY() || isCIEnvironment() || !endpointUserMode() || endpointOpts.noStart {
		fmt.Fprintln(cmd.OutOrStdout(), "Next: beacon getting-started")
		return nil
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	if !askYesNo(reader, cmd.OutOrStdout(), "Run guided setup now?", true) {
		fmt.Fprintln(cmd.OutOrStdout(), "Next: beacon getting-started")
		return nil
	}
	return runGettingStarted(cmd, nil)
}
