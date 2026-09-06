package cmd

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	endpointinventory "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

type tokenUsageOptions struct {
	userMode   bool
	systemMode bool
	logPath    string
	jsonOutput bool
	since      string
	until      string
	session    string
	model      string
	harness    string
	repository string
	runID      string
	bucket     string
	top        int
	coverage   bool
}

var tokenUsageOpts tokenUsageOptions

var tokenUsageCmd = &cobra.Command{
	Use:          "token-usage",
	Short:        "Report token usage and attribution from Beacon runtime logs",
	SilenceUsage: true,
	RunE:         runTokenUsage,
}

func runTokenUsage(cmd *cobra.Command, args []string) error {
	userMode := tokenUsageOpts.userMode
	if tokenUsageOpts.systemMode {
		userMode = false
	}
	runtimeLog := lifecycle.ResolveRuntimeLog(userMode, tokenUsageOpts.logPath)
	query := dashboard.EventQuery{
		Session:    tokenUsageOpts.session,
		Model:      tokenUsageOpts.model,
		Harness:    tokenUsageOpts.harness,
		Repository: tokenUsageOpts.repository,
	}
	if since := strings.TrimSpace(tokenUsageOpts.since); since != "" {
		parsed, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return err
		}
		query.Since = parsed
	}
	if until := strings.TrimSpace(tokenUsageOpts.until); until != "" {
		parsed, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return err
		}
		query.Until = parsed
	}
	if tokenUsageOpts.coverage {
		return runTokenCoverage(cmd, runtimeLog.EffectiveLogPath, query)
	}
	events, contexts, err := dashboard.ReadTokenEventsAppendOrder(runtimeLog.EffectiveLogPath, query)
	if err != nil {
		return err
	}
	opts := tokens.Options{
		SessionID: tokenUsageOpts.session,
		TopLimit:  tokenUsageOpts.top,
	}
	if bucket := strings.TrimSpace(tokenUsageOpts.bucket); bucket != "" {
		parsed, err := time.ParseDuration(bucket)
		if err != nil {
			return err
		}
		opts.BucketSize = parsed
	}
	report := tokens.AggregateScopedWithContexts(events, contexts, tokenUsageOpts.runID, opts)
	out := cmd.OutOrStdout()
	if tokenUsageOpts.jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	tokens.RenderText(out, report)
	return nil
}

// runTokenCoverage answers "is this total all of it", which the usage report itself cannot: a
// runtime whose telemetry never arrives is indistinguishable from one nobody used, since both are
// simply absent from every rollup.
//
// It deliberately drops the session, model, repository and run filters and keeps only the time
// window and the harness scope. Those filters select usage-bearing events almost by definition --
// an event carries no model unless it carried a model call -- so a coverage report computed over
// them would find every runtime covered and prove nothing. The window is what a reader is
// actually asking about.
func runTokenCoverage(cmd *cobra.Command, logPath string, query dashboard.EventQuery) error {
	// The harness scope is deliberately NOT handed to the event reader. Its filter compares the
	// flag to harness.name with case-insensitive equality on the raw string, while events carry
	// the canonical name written by NormalizeHarnessName -- so `--harness vscode` matches nothing,
	// every vscode_copilot event being named vscode_copilot. Applied here that asymmetry is worse
	// than a plain miss: the installed list below canonicalizes, so the runtime stays in the
	// report while its events vanish, and a runtime that spent tokens is labelled inactive.
	// Both sides are canonicalized here instead.
	events, _, err := dashboard.ReadTokenEventsAppendOrder(logPath, dashboard.EventQuery{
		Since: query.Since,
		Until: query.Until,
	})
	if err != nil {
		return err
	}
	want := asymptoteobserve.NormalizeHarnessName(strings.TrimSpace(query.Harness))
	if want != "" {
		scopedEvents := events[:0]
		for _, event := range events {
			if strings.EqualFold(asymptoteobserve.NormalizeHarnessName(event.Harness.Name), want) {
				scopedEvents = append(scopedEvents, event)
			}
		}
		events = scopedEvents
	}
	// Installed runtimes come from the config scanner rather than from the log, because the
	// whole question is which installed runtime is missing from the log. tokens.InstalledRuntimes
	// decides which scanner rows are real evidence of an install; the scanner reports files a
	// runtime might read, which is not the same thing.
	configs := make([]tokens.InstalledConfig, 0)
	for _, config := range endpointinventory.Scan(endpointinventory.Options{}).Configs {
		configs = append(configs, tokens.InstalledConfig{
			Runtime:       config.Runtime,
			Path:          config.Path,
			Kind:          config.ConfigKind,
			BeaconManaged: config.BeaconManaged,
			Exists:        config.Exists,
		})
	}
	installed := tokens.InstalledRuntimes(configs)
	// A harness scope narrows the events, so it has to narrow the installed list with them.
	// Otherwise every other installed runtime has no events in the filtered set and is reported
	// inactive -- which reads as "installed but unused this window" when the truth is only that
	// the reader asked about a different runtime.
	if want != "" {
		kept := installed[:0]
		for _, name := range installed {
			if strings.EqualFold(asymptoteobserve.NormalizeHarnessName(name), want) {
				kept = append(kept, name)
			}
		}
		installed = kept
	}
	report := tokens.Coverage(events, installed)
	out := cmd.OutOrStdout()
	if tokenUsageOpts.jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	tokens.RenderCoverageText(out, report)
	return nil
}

func init() {
	rootCmd.AddCommand(tokenUsageCmd)
	tokenUsageCmd.Flags().BoolVar(&tokenUsageOpts.userMode, "user", true, "Use per-user endpoint paths")
	tokenUsageCmd.Flags().BoolVar(&tokenUsageOpts.systemMode, "system", false, "Use system endpoint paths and the system collector service")
	tokenUsageCmd.Flags().StringVar(&tokenUsageOpts.logPath, "log-path", "", "Runtime JSONL log path (defaults to the local runtime log; in CI point at the session log)")
	tokenUsageCmd.Flags().BoolVar(&tokenUsageOpts.jsonOutput, "json", false, "Print the token usage report as JSON")
	tokenUsageCmd.Flags().StringVar(&tokenUsageOpts.since, "since", "", "Only include events at or after this RFC3339 timestamp")
	tokenUsageCmd.Flags().StringVar(&tokenUsageOpts.until, "until", "", "Only include events at or before this RFC3339 timestamp")
	tokenUsageCmd.Flags().StringVar(&tokenUsageOpts.session, "session", "", "Filter to one session and include per-step detail")
	tokenUsageCmd.Flags().StringVar(&tokenUsageOpts.model, "model", "", "Filter by model name")
	tokenUsageCmd.Flags().StringVar(&tokenUsageOpts.harness, "harness", "", "Filter by harness name")
	tokenUsageCmd.Flags().StringVar(&tokenUsageOpts.repository, "repository", "", "Filter by repository")
	tokenUsageCmd.Flags().StringVar(&tokenUsageOpts.runID, "run-id", "", "Filter by CI run id")
	tokenUsageCmd.Flags().StringVar(&tokenUsageOpts.bucket, "bucket", "", "Time-series bucket size (for example 1h or 15m)")
	tokenUsageCmd.Flags().IntVar(&tokenUsageOpts.top, "top", 0, "Limit each grouping to the top N entries (0 keeps all)")
	tokenUsageCmd.Flags().BoolVar(&tokenUsageOpts.coverage, "coverage", false, "Report which runtimes contributed token telemetry instead of the usage totals")
}
