package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/detect"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
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

Rules come from the local store (~/.beacon/endpoint/rules) when present, otherwise from
the built-in baseline. Use --rules to scan with an explicit rule directory, and
'beacon rules add' / 'beacon rules pull' to manage the store.

Scanning is read-only and never touches the network.`,
	SilenceUsage: true,
	RunE:         runScan,
}

// severityRank orders severities for --min-severity / --fail-on comparisons.
var severityRank = map[asymptoteobserve.Severity]int{
	asymptoteobserve.SeverityInfo:     0,
	asymptoteobserve.SeverityLow:      1,
	asymptoteobserve.SeverityMedium:   2,
	asymptoteobserve.SeverityHigh:     3,
	asymptoteobserve.SeverityCritical: 4,
}

func runScan(cmd *cobra.Command, args []string) error {
	userMode := scanOpts.userMode
	if scanOpts.systemMode {
		userMode = false
	}

	var minRank int
	if s := strings.TrimSpace(scanOpts.minSeverity); s != "" {
		r, ok := severityRank[asymptoteobserve.Severity(s)]
		if !ok {
			return fmt.Errorf("invalid --min-severity %q (info|low|medium|high|critical)", s)
		}
		minRank = r
	}
	failRank := -1
	if s := strings.TrimSpace(scanOpts.failOn); s != "" {
		r, ok := severityRank[asymptoteobserve.Severity(s)]
		if !ok {
			return fmt.Errorf("invalid --fail-on %q (info|low|medium|high|critical)", s)
		}
		failRank = r
	}

	// Load and compile the active rule set.
	loaded, err := detect.LoadActive(userMode, strings.TrimSpace(scanOpts.rulesDir))
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}
	if len(loaded) == 0 {
		return fmt.Errorf("no rules to run (store is empty and baseline missing)")
	}
	compiled := make([]*threatrules.CompiledRule, 0, len(loaded))
	for _, lr := range loaded {
		c, err := threatrules.Compile(lr.Rule)
		if err != nil {
			return fmt.Errorf("compile rule %q: %w", lr.Rule.ID, err)
		}
		compiled = append(compiled, c)
	}

	// Stream the runtime log into memory (correlation needs the full ordered stream).
	runtimeLog := lifecycle.ResolveRuntimeLog(userMode, scanOpts.logPath)
	sessionFilter := strings.TrimSpace(scanOpts.session)
	var events []asymptoteobserve.Event
	err = dashboard.StreamEvents(runtimeLog.EffectiveLogPath, func(e schema.Event) error {
		if sessionFilter != "" && (e.Session == nil || e.Session.ID != sessionFilter) {
			return nil
		}
		events = append(events, e)
		return nil
	})
	if err != nil {
		return fmt.Errorf("read telemetry %s: %w", runtimeLog.EffectiveLogPath, err)
	}

	findings, err := threatrules.ScanEvents(compiled, events)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	// The --fail-on gate is a CI signal that must reflect every detection in the
	// scan, so count it before --min-severity (a display-only filter) drops
	// findings from the output slice.
	failCount := 0
	if failRank >= 0 {
		failCount = countAtOrAbove(findings, failRank)
	}
	if minRank > 0 {
		findings = filterBySeverity(findings, minRank)
	}
	// Highest severity first, then rule id, for stable, useful ordering.
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := severityRank[findings[i].Severity], severityRank[findings[j].Severity]
		if ri != rj {
			return ri > rj
		}
		return findings[i].RuleID < findings[j].RuleID
	})

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
		printFindings(cmd, findings, len(events), runtimeLog.EffectiveLogPath)
	}

	if failRank >= 0 && failCount > 0 {
		return fmt.Errorf("scan found %d finding(s) at or above %q", failCount, scanOpts.failOn)
	}
	return nil
}

func filterBySeverity(findings []threatrules.Finding, minRank int) []threatrules.Finding {
	out := findings[:0]
	for _, f := range findings {
		if severityRank[f.Severity] >= minRank {
			out = append(out, f)
		}
	}
	return out
}

func countAtOrAbove(findings []threatrules.Finding, rank int) int {
	n := 0
	for _, f := range findings {
		if severityRank[f.Severity] >= rank {
			n++
		}
	}
	return n
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
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func init() {
	scanCmd.Flags().BoolVar(&scanOpts.userMode, "user", true, "Use per-user endpoint paths")
	scanCmd.Flags().BoolVar(&scanOpts.systemMode, "system", false, "Use system endpoint paths")
	scanCmd.Flags().StringVar(&scanOpts.logPath, "log-path", "", "Runtime JSONL log path (default: resolved from config)")
	scanCmd.Flags().StringVar(&scanOpts.rulesDir, "rules", "", "Rule directory to scan with (default: store, else baseline)")
	scanCmd.Flags().BoolVar(&scanOpts.jsonOutput, "json", false, "Output findings as JSON")
	scanCmd.Flags().StringVar(&scanOpts.minSeverity, "min-severity", "", "Only report findings at or above this severity")
	scanCmd.Flags().StringVar(&scanOpts.session, "session", "", "Only scan events for this session id")
	scanCmd.Flags().StringVar(&scanOpts.failOn, "fail-on", "", "Exit non-zero if any finding is at or above this severity")
	rootCmd.AddCommand(scanCmd)
}
