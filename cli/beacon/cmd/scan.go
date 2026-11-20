package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/detect"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

var scanOpts struct {
	userMode    bool
	systemMode  bool
	logPath     string
	rulesDir    string
	jsonOutput  bool
	minSeverity string
	session     string
	failOn      string
}

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Run threat-detection rules over local agent telemetry",
	Long: `Scans the local Beacon runtime telemetry log with the active threat-detection
rules and reports findings.

Rules come from the local store (~/.beacon/endpoint/rules) plus every built-in baseline
rule the store does not replace by id. Use --rules to scan with only an explicit rule
directory, and
'beacon rules add' / 'beacon rules pull' to manage the store.

Scanning is read-only and never touches the network.`,
	SilenceUsage: true,
	RunE:         runScan,
}

func runScan(cmd *cobra.Command, args []string) error {
	userMode := flagsUserMode(scanOpts.userMode, scanOpts.systemMode)

	var minRank int
	if s := strings.TrimSpace(scanOpts.minSeverity); s != "" {
		r, ok := detect.ParseSeverity(s)
		if !ok {
			return fmt.Errorf("invalid --min-severity %q (info|low|medium|high|critical)", s)
		}
		minRank = r
	}
	failRank := -1
	if s := strings.TrimSpace(scanOpts.failOn); s != "" {
		r, ok := detect.ParseSeverity(s)
		if !ok {
			return fmt.Errorf("invalid --fail-on %q (info|low|medium|high|critical)", s)
		}
		failRank = r
	}

	// Correlation needs the full ordered stream, so the whole log is read into memory.
	runtimeLog := lifecycle.ResolveRuntimeLog(userMode, scanOpts.logPath)
	report, err := detect.Scan(userMode, scanOpts.rulesDir, func() ([]asymptoteobserve.Event, error) {
		return dashboard.ReadScanEvents(runtimeLog.EffectiveLogPath, scanOpts.session)
	})
	if err != nil {
		// A *detect.NoRulesError names any skipped rules itself, so they are not also warned about.
		return err
	}
	detect.WarnSkipped(cmd.ErrOrStderr(), report.Skipped)
	findings := report.Findings
	// The --fail-on gate is a CI signal that must reflect every detection in the
	// scan, so count it before --min-severity (a display-only filter) drops
	// findings from the output slice.
	failCount := 0
	if failRank >= 0 {
		failCount = threatrules.CountAtOrAbove(findings, failRank)
	}
	if minRank > 0 {
		findings = threatrules.FilterBySeverity(findings, minRank)
	}

	out := cmd.OutOrStdout()
	if scanOpts.jsonOutput {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if findings == nil {
			findings = []threatrules.Finding{}
		}
		if err := enc.Encode(findings); err != nil {
			return err
		}
	} else {
		printFindings(cmd, findings, report.Scanned, runtimeLog.EffectiveLogPath)
	}

	var errs []error
	if failed := report.Failed; len(failed) > 0 {
		// The findings are what the rules found everywhere else; a rule that could not be
		// evaluated on an event has no verdict there, so the scan exits non-zero for a script.
		warnFailures(cmd, failed)
		errs = append(errs, fmt.Errorf("scan incomplete: %d rule(s) could not be evaluated on every event", len(failed)))
	}
	if failRank >= 0 && failCount > 0 {
		errs = append(errs, fmt.Errorf("scan found %d finding(s) at or above %q", failCount, scanOpts.failOn))
	}
	return errors.Join(errs...)
}

// warnFailures names each rule that could not be evaluated on some event, and the first such event.
func warnFailures(cmd *cobra.Command, failed []threatrules.RuleFailures) {
	w := cmd.ErrOrStderr()
	for _, f := range failed {
		event, session := f.First.EventID, f.First.SessionID
		if event == "" {
			event = "-"
		}
		if session == "" {
			session = "-"
		}
		fmt.Fprintf(w, "warning: rule %q could not be evaluated on %d event(s), so its verdict there is unknown (first: event %s, session %s): %v\n",
			f.RuleID, f.Events, event, session, f.First.Err)
	}
}

func printFindings(cmd *cobra.Command, findings []threatrules.Finding, scanned int, logPath string) {
	out := cmd.OutOrStdout()
	if len(findings) == 0 {
		fmt.Fprintf(out, "No findings (%d events scanned from %s).\n", scanned, logPath)
		return
	}
	for _, f := range findings {
		session := f.SessionID
		if session == "" {
			session = "-"
		}
		fmt.Fprintf(out, "[%s] %s  session=%s\n", strings.ToUpper(string(f.Severity)), f.RuleID, session)
		fmt.Fprintf(out, "    %s\n", f.Reason)
		for _, e := range f.Events {
			fmt.Fprintf(out, "    - %s  %s\n", eventWhen(e), summarizeEvent(e))
		}
	}
	fmt.Fprintf(out, "\n%d finding(s) across %d events.\n", len(findings), scanned)
}

func eventWhen(e asymptoteobserve.Event) string {
	if e.Timestamp == "" {
		return "(no ts)"
	}
	return e.Timestamp
}

// summarizeEvent renders a short, human one-liner of the matched event's salient field.
func summarizeEvent(e asymptoteobserve.Event) string {
	switch {
	case e.Command != nil && e.Command.Command != "":
		return fmt.Sprintf("%s: %s", e.Event.Action, truncate(e.Command.Command, 100))
	case e.File != nil && e.File.Path != "":
		return fmt.Sprintf("%s: %s", e.Event.Action, e.File.Path)
	case e.Prompt != nil && e.Prompt.Text != "":
		return fmt.Sprintf("%s: %s", e.Event.Action, truncate(e.Prompt.Text, 100))
	default:
		return e.Event.Action
	}
}

func truncate(s string, n int) string {
	return truncateRunes(strings.NewReplacer("\n", " ", "\r", " ").Replace(s), n)
}

func init() {
	scanCmd.Flags().BoolVar(&scanOpts.userMode, "user", true, "Use per-user endpoint paths")
	scanCmd.Flags().BoolVar(&scanOpts.systemMode, "system", false, "Use system endpoint paths")
	scanCmd.Flags().StringVar(&scanOpts.logPath, "log-path", "", "Runtime JSONL log path (default: resolved from config)")
	scanCmd.Flags().StringVar(&scanOpts.rulesDir, "rules", "", "Rule directory to scan with, instead of the store and baseline")
	scanCmd.Flags().BoolVar(&scanOpts.jsonOutput, "json", false, "Output findings as JSON")
	scanCmd.Flags().StringVar(&scanOpts.minSeverity, "min-severity", "", "Only report findings at or above this severity")
	scanCmd.Flags().StringVar(&scanOpts.session, "session", "", "Only scan events for this session id")
	scanCmd.Flags().StringVar(&scanOpts.failOn, "fail-on", "", "Exit non-zero if any finding is at or above this severity")
	rootCmd.AddCommand(scanCmd)
}
