package cmd

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/tokens"
)

var endpointTokensOpts struct {
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
}

var endpointTokensCmd = &cobra.Command{
	Use:          "tokens",
	Short:        "Report token usage and attribution from the endpoint runtime log",
	SilenceUsage: true,
	RunE:         runEndpointTokens,
}

func runEndpointTokens(cmd *cobra.Command, args []string) error {
	userMode := endpointTokensOpts.userMode
	if endpointTokensOpts.systemMode {
		userMode = false
	}
	runtimeLog := lifecycle.ResolveRuntimeLog(userMode, endpointTokensOpts.logPath)
	query := dashboard.EventQuery{
		NoLimit:    true,
		Session:    endpointTokensOpts.session,
		Model:      endpointTokensOpts.model,
		Harness:    endpointTokensOpts.harness,
		Repository: endpointTokensOpts.repository,
	}
	if since := strings.TrimSpace(endpointTokensOpts.since); since != "" {
		parsed, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return err
		}
		query.Since = parsed
	}
	if until := strings.TrimSpace(endpointTokensOpts.until); until != "" {
		parsed, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return err
		}
		query.Until = parsed
	}
	result, err := dashboard.ReadEvents(runtimeLog.EffectiveLogPath, query)
	if err != nil {
		return err
	}
	// Feed Aggregate in chronological (append) order so cumulative metric
	// series resolve correctly when a batch of datapoints shares the same
	// second-resolution timestamp.
	dashboard.SortRecordsAppendOrder(result.Events)
	session := strings.TrimSpace(endpointTokensOpts.session)
	runID := strings.TrimSpace(endpointTokensOpts.runID)
	events := make([]schema.Event, 0, len(result.Events))
	for _, record := range result.Events {
		// Exact session match keeps totals/grouping consistent with the
		// per-step drilldown, which matches the session id exactly.
		if session != "" && (record.Event.Session == nil || record.Event.Session.ID != session) {
			continue
		}
		if runID != "" {
			if record.Event.Run == nil || record.Event.Run.RunID != runID {
				continue
			}
		}
		events = append(events, record.Event)
	}
	opts := tokens.Options{
		SessionID: endpointTokensOpts.session,
		TopLimit:  endpointTokensOpts.top,
	}
	if bucket := strings.TrimSpace(endpointTokensOpts.bucket); bucket != "" {
		parsed, err := time.ParseDuration(bucket)
		if err != nil {
			return err
		}
		opts.BucketSize = parsed
	}
	report := tokens.Aggregate(events, opts)
	out := cmd.OutOrStdout()
	if endpointTokensOpts.jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	tokens.RenderText(out, report)
	return nil
}

func init() {
	endpointCmd.AddCommand(endpointTokensCmd)
	endpointTokensCmd.Flags().BoolVar(&endpointTokensOpts.userMode, "user", true, "Use per-user endpoint paths")
	endpointTokensCmd.Flags().BoolVar(&endpointTokensOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
	endpointTokensCmd.Flags().StringVar(&endpointTokensOpts.logPath, "log-path", "", "Runtime JSONL log path (defaults to the endpoint runtime log; in CI point at the session log)")
	endpointTokensCmd.Flags().BoolVar(&endpointTokensOpts.jsonOutput, "json", false, "Print the token usage report as JSON")
	endpointTokensCmd.Flags().StringVar(&endpointTokensOpts.since, "since", "", "Only include events at or after this RFC3339 timestamp")
	endpointTokensCmd.Flags().StringVar(&endpointTokensOpts.until, "until", "", "Only include events at or before this RFC3339 timestamp")
	endpointTokensCmd.Flags().StringVar(&endpointTokensOpts.session, "session", "", "Filter to one session and include per-step detail")
	endpointTokensCmd.Flags().StringVar(&endpointTokensOpts.model, "model", "", "Filter by model name")
	endpointTokensCmd.Flags().StringVar(&endpointTokensOpts.harness, "harness", "", "Filter by harness name")
	endpointTokensCmd.Flags().StringVar(&endpointTokensOpts.repository, "repository", "", "Filter by repository")
	endpointTokensCmd.Flags().StringVar(&endpointTokensOpts.runID, "run-id", "", "Filter by CI run id")
	endpointTokensCmd.Flags().StringVar(&endpointTokensOpts.bucket, "bucket", "", "Time-series bucket size (for example 1h or 15m)")
	endpointTokensCmd.Flags().IntVar(&endpointTokensOpts.top, "top", 0, "Limit each grouping to the top N entries (0 keeps all)")
}
