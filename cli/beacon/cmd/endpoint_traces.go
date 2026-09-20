package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

var endpointTraceOpts struct {
	query       string
	limit       int
	page        int
	state       string
	visibility  string
	resultLevel string
	offset      int
	aroundEvent int
	before      int
	after       int
	eventTypes  string
}

var endpointTracesCmd = &cobra.Command{
	Use:   "traces",
	Short: "Index and search local endpoint traces",
}

var endpointTracesStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show local trace store status",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		logPath := endpointTraceLogPath()
		status, err := dashboard.TraceStoreStatus(logPath)
		if err != nil {
			return err
		}
		if endpointOpts.jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(status)
		}
		fmt.Printf("Trace store: %s\n", status.Path)
		fmt.Printf("Runtime log: %s\n", logPath)
		fmt.Printf("Traces: %d\nEvents: %d\nIndex rows: %d\n", status.Traces, status.Events, status.IndexRows)
		fmt.Printf("Size: %d bytes", status.SizeBytes)
		if status.WALBytes > 0 {
			fmt.Printf(" (+%d bytes WAL)", status.WALBytes)
		}
		fmt.Println()
		if status.IndexedAt != "" {
			fmt.Printf("Indexed at: %s\n", status.IndexedAt)
		}
		return nil
	},
}

var endpointTracesReindexCmd = &cobra.Command{
	Use:          "reindex",
	Short:        "Rebuild the local trace store from runtime JSONL",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		logPath := endpointTraceLogPath()
		if err := dashboard.ReindexTraceStore(logPath); err != nil {
			return err
		}
		status, err := dashboard.TraceStoreStatus(logPath)
		if err != nil {
			return err
		}
		if endpointOpts.jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(status)
		}
		fmt.Printf("Reindexed %d traces and %d events into %s\n", status.Traces, status.Events, status.Path)
		return nil
	},
}

var endpointTracesListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List local traces",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := dashboard.ReadTraceList(endpointTraceLogPath(), endpointTraceQuery())
		if err != nil {
			return err
		}
		if endpointOpts.jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		for _, trace := range result.Traces {
			fmt.Printf("%s\t%s\t%s\t%s\n", trace.ID, trace.Harness.Name, trace.UpdatedAt, trace.Title)
		}
		if result.Truncated {
			fmt.Printf("Showing %d of %d traces; use --page or --limit to continue.\n", result.Returned, result.TotalMatched)
		}
		return nil
	},
}

var endpointTracesSearchCmd = &cobra.Command{
	Use:          "search <pattern>",
	Short:        "Search local trace metadata and indexed event text",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		query := endpointTraceQuery()
		query.Q = args[0]
		query.ResultLevel = endpointTraceOpts.resultLevel
		result, err := dashboard.SearchTraces(endpointTraceLogPath(), query)
		if err != nil {
			return err
		}
		if endpointOpts.jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		if result.ResultLevel == "event" {
			for _, match := range result.Events {
				fmt.Printf("%s\t#%d\t%s\t%s\n", match.Trace.ID, match.Event.Number, match.Event.Type, traceFirstNonEmpty(match.Snippet, match.Event.Title, match.Event.Summary))
			}
		} else {
			for _, trace := range result.Traces {
				fmt.Printf("%s\t%s\t%s\n", trace.ID, trace.Harness.Name, trace.Title)
			}
		}
		return nil
	},
}

var endpointTracesShowCmd = &cobra.Command{
	Use:          "show <trace-id>",
	Short:        "Show events from one local trace",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		result, ok, err := dashboard.ShowTrace(endpointTraceLogPath(), args[0], endpointTraceQuery())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("trace not found: %s", args[0])
		}
		if endpointOpts.jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		fmt.Printf("%s\t%s\n", result.Trace.ID, result.Trace.Title)
		for _, event := range result.Events {
			fmt.Printf("#%d\t%s\t%s\n", event.Number, event.Type, traceFirstNonEmpty(event.Title, event.Summary))
			if event.Content != nil && event.Content.Text != "" {
				fmt.Println(indentTraceText(event.Content.Text))
			}
			if event.Command != nil && event.Command.Command != "" {
				fmt.Println(indentTraceText(event.Command.Command))
			}
		}
		if result.Range.ReturnedEvents < result.Range.TotalEvents {
			fmt.Printf("Showing %d of %d events from offset %d.\n", result.Range.ReturnedEvents, result.Range.TotalEvents, result.Range.Offset)
		}
		return nil
	},
}

func endpointTraceLogPath() string {
	runtimeLog := lifecycle.ResolveRuntimeLog(endpointUserMode(), endpointOpts.logPath)
	return runtimeLog.EffectiveLogPath
}

func endpointTraceQuery() dashboard.TraceQuery {
	return dashboard.TraceQuery{
		EventQuery:  dashboard.EventQuery{Q: endpointTraceOpts.query},
		Limit:       endpointTraceOpts.limit,
		Page:        endpointTraceOpts.page,
		State:       endpointTraceOpts.state,
		Visibility:  endpointTraceOpts.visibility,
		ResultLevel: endpointTraceOpts.resultLevel,
		Offset:      endpointTraceOpts.offset,
		AroundEvent: endpointTraceOpts.aroundEvent,
		Before:      endpointTraceOpts.before,
		After:       endpointTraceOpts.after,
		EventTypes:  splitCSV(endpointTraceOpts.eventTypes),
	}
}

func indentTraceText(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}

func traceFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
