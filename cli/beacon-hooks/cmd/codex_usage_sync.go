package cmd

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/codexusage"
)

var codexUsageSyncCmd = &cobra.Command{
	Use:    "codex-usage-sync",
	Short:  "Reconcile Codex session token usage into endpoint telemetry",
	Hidden: true,
	Run:    runCodexUsageSync,
}

func init() {
	rootCmd.AddCommand(codexUsageSyncCmd)
}

func runCodexUsageSync(cmd *cobra.Command, args []string) {
	logger := logging.NewLoggerForPlatform("codex-usage-sync", platformFlag)
	if platformFlag != "codex" {
		outputJSON(emptyResponse)
		return
	}
	if err := reconcileCodexUsage(logger); err != nil {
		logger.Warn("Codex usage reconciliation failed", "error", err.Error())
	}
	outputJSON(emptyResponse)
}

func maybeReconcileCodexUsage(logger *logging.Logger) {
	if platformFlag != "codex" {
		return
	}
	if err := reconcileCodexUsage(logger); err != nil {
		logger.Warn("Codex usage reconciliation failed", "error", err.Error())
	}
}

func reconcileCodexUsage(logger *logging.Logger) error {
	result, err := codexusage.ReconcileAndWrite(codexusage.ReconcileOptions{}, func(event codexusage.UsageEvent) error {
		fields := codexUsageFields(event)
		if err := logger.EndpointEvent("token.usage", "metric", "info", "Codex token usage observed", fields); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	logger.Debug("Codex usage reconciliation completed", "events", len(result.Events), "scanned", result.Scanned)
	return nil
}

func codexUsageFields(event codexusage.UsageEvent) map[string]interface{} {
	sessionID := "codex:" + event.SessionID
	if event.SessionID == "" {
		sessionID = ""
	}
	ts := event.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	fields := map[string]interface{}{
		"timestamp": ts.UTC().Format(time.RFC3339),
		"harness":   map[string]interface{}{"name": "codex_cli"},
		"model":     event.Model,
		"gen_ai": map[string]interface{}{
			"usage": map[string]interface{}{
				"input_tokens":  event.InputTokens,
				"output_tokens": event.OutputTokens,
				"cache_read": map[string]interface{}{
					"input_tokens": event.CacheReadTokens,
				},
			},
		},
		"raw": map[string]interface{}{
			"source":           codexusage.SourceCodexSessionJSONL,
			"source_kind":      "derived_session_usage",
			"source_path_hash": codexusage.SourcePathHash(event.SourcePath),
			"dedup_key":        event.DedupKey,
			"turn_id":          event.TurnID,
			"cost_source":      "not_computed",
		},
	}
	if event.ReasoningTokens > 0 {
		fields["gen_ai"].(map[string]interface{})["usage"].(map[string]interface{})["reasoning"] = map[string]interface{}{
			"output_tokens": event.ReasoningTokens,
		}
	}
	if sessionID != "" {
		fields["session"] = map[string]interface{}{"id": sessionID, "working_directory": event.WorkingDir}
	}
	if event.WorkingDir != "" {
		fields["repository"] = event.WorkingDir
	}
	return fields
}
