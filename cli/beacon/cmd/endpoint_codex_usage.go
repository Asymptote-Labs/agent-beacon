package cmd

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/codexusage"
)

var endpointCodexUsageCmd = &cobra.Command{
	Use:   "codex-usage",
	Short: "Manage Codex session token usage reconciliation",
}

var endpointCodexUsageSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Write derived Codex session token usage events to the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointCodexUsageSync,
}

func init() {
	endpointCmd.AddCommand(endpointCodexUsageCmd)
	endpointCodexUsageCmd.AddCommand(endpointCodexUsageSyncCmd)
	endpointCodexUsageSyncCmd.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
	endpointCodexUsageSyncCmd.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	endpointCodexUsageSyncCmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
}

func runEndpointCodexUsageSync(cmd *cobra.Command, args []string) error {
	userMode := endpointOpts.userMode
	if endpointOpts.systemMode {
		userMode = false
	}
	runtimeLog := lifecycle.ResolveRuntimeLog(userMode, endpointOpts.logPath)
	result, err := syncCodexUsageToLog(runtimeLog.EffectiveLogPath, runtimeLog.EffectiveUserMode)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %d Codex token usage events (scanned %d files)\n", len(result.Events), result.Scanned)
	return nil
}

func syncCodexUsageToLog(logPath string, userMode bool) (codexusage.ReconcileResult, error) {
	result, err := codexusage.Reconcile(codexusage.ReconcileOptions{})
	if err != nil {
		return codexusage.ReconcileResult{}, err
	}
	for _, usage := range result.Events {
		event := codexUsageSchemaEvent(usage)
		if _, err := writer.AppendEvent(event, writer.Options{Path: logPath, UserMode: userMode}); err != nil {
			return codexusage.ReconcileResult{}, err
		}
		if err := codexusage.MarkEventSeen(usage, ""); err != nil {
			return codexusage.ReconcileResult{}, err
		}
	}
	return result, nil
}

func codexUsageSchemaEvent(usage codexusage.UsageEvent) schema.Event {
	ts := usage.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	sessionID := ""
	if usage.SessionID != "" {
		sessionID = "codex:" + usage.SessionID
	}
	input := usage.InputTokens
	output := usage.OutputTokens
	cacheRead := usage.CacheReadTokens
	reasoning := usage.ReasoningTokens
	event := schema.NewEvent(schema.NewEventOptions{
		Action:   "token.usage",
		Category: "metric",
		Severity: schema.SeverityInfo,
		Harness:  schema.HarnessInfo{Name: "codex_cli"},
		Message:  "Codex token usage observed",
		Origin:   schema.OriginLocal,
	})
	event.Timestamp = ts.UTC().Format(time.RFC3339)
	event.Endpoint.OS = runtime.GOOS
	if hostname, err := os.Hostname(); err == nil {
		event.Endpoint.Hostname = hostname
	}
	event.Model = usage.Model
	event.Repository = usage.WorkingDir
	if sessionID != "" {
		event.Session = &schema.SessionInfo{ID: sessionID, WorkingDirectory: usage.WorkingDir}
	}
	event.GenAI = &schema.GenAIInfo{
		Usage: &schema.GenAIUsageInfo{
			InputTokens:  &input,
			OutputTokens: &output,
			CacheRead:    &schema.GenAIUsageCacheReadInfo{InputTokens: &cacheRead},
		},
	}
	if reasoning > 0 {
		event.GenAI.Usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: &reasoning}
	}
	event.Raw = map[string]interface{}{
		"source":           codexusage.SourceCodexSessionJSONL,
		"source_kind":      "derived_session_usage",
		"source_path_hash": codexusage.SourcePathHash(usage.SourcePath),
		"dedup_key":        usage.DedupKey,
		"turn_id":          usage.TurnID,
		"cost_source":      "not_computed",
	}
	event.Content = &schema.ContentInfo{Retention: asymptoteobserve.ContentRetentionMetadata, Included: false}
	return event
}
