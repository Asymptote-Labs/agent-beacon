package cmd

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/tokens"
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
}

var tokenUsageOpts tokenUsageOptions

var tokenUsageCmd = &cobra.Command{
	Use:          "token-usage",
	Short:        "Report token usage and attribution from the endpoint runtime log",
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
	events, err := dashboard.ReadEventsAppendOrder(runtimeLog.EffectiveLogPath, query)
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
	report := tokens.AggregateScoped(events, tokenUsageOpts.runID, opts)
	out := cmd.OutOrStdout()
	if tokenUsageOpts.jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	tokens.RenderText(out, report)
	return nil
}

func init() {
	rootCmd.AddCommand(tokenUsageCmd)
	tokenUsageCmd.Flags().BoolVar(&tokenUsageOpts.userMode, "user", true, "Use per-user endpoint paths")
	tokenUsageCmd.Flags().BoolVar(&tokenUsageOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
	tokenUsageCmd.Flags().StringVar(&tokenUsageOpts.logPath, "log-path", "", "Runtime JSONL log path (defaults to the endpoint runtime log; in CI point at the session log)")
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
}
