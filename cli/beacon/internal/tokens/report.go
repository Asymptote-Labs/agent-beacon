package tokens

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// RenderText writes a human-readable token usage report.
func RenderText(w io.Writer, report Report) {
	fmt.Fprintf(w, "Token usage report (%d of %d events carry usage)\n\n", report.EventsWithUsage, report.TotalEvents)
	if report.Source != nil && report.Source.Codex != "" {
		fmt.Fprintf(w, "Codex source: %s\n\n", report.Source.Codex)
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TOTALS\tINPUT\tOUTPUT\tCACHE READ\tCACHE CREATE\tREASONING\tCOST USD\tEVENTS")
	writeUsageRow(tw, "", report.Totals)
	tw.Flush()

	writeGroups(w, "BY MODEL", report.ByModel)
	writeGroups(w, "BY SESSION", report.BySession)
	writeGroups(w, "BY HARNESS", report.ByHarness)
	writeGroups(w, "BY REPOSITORY", report.ByRepository)
	writeGroups(w, "BY RUN", report.ByRun)

	if len(report.Utilization) > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "CONTEXT UTILIZATION\tWINDOW\tCALLS\tMAX INPUT\tMAX\tP95\tNEAR LIMIT")
		for _, u := range report.Utilization {
			window := "unknown"
			maxRatio, p95Ratio := "-", "-"
			if u.ContextWindow > 0 {
				window = fmt.Sprintf("%d", u.ContextWindow)
				maxRatio = fmt.Sprintf("%.1f%%", u.MaxRatio*100)
				p95Ratio = fmt.Sprintf("%.1f%%", u.P95Ratio*100)
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\t%d\n", u.Model, window, u.Calls, u.MaxInputTokens, maxRatio, p95Ratio, u.NearLimitCalls)
		}
		tw.Flush()
	}

	if len(report.Series) > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "BUCKET\tINPUT\tOUTPUT\tCACHE READ\tCACHE CREATE\tREASONING\tCOST USD\tEVENTS")
		for _, bucket := range report.Series {
			writeUsageRow(tw, bucket.Start, bucket.Usage)
		}
		tw.Flush()
	}

	if report.SessionDetail != nil {
		fmt.Fprintf(w, "\nSESSION %s\n", report.SessionDetail.SessionID)
		tw = tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "STEP\tMODEL\tINPUT\tOUTPUT\tCACHE READ\tCACHE CREATE\tREASONING\tCOST USD")
		for _, step := range report.SessionDetail.Steps {
			writeStepRows(tw, step, 0)
		}
		tw.Flush()
	}
}

func writeGroups(w io.Writer, title string, groups []Group) {
	if len(groups) == 0 {
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\tINPUT\tOUTPUT\tCACHE READ\tCACHE CREATE\tREASONING\tCOST USD\tEVENTS\n", title)
	for _, group := range groups {
		writeUsageRow(tw, group.Key, group.Usage)
	}
	tw.Flush()
}

func writeUsageRow(w io.Writer, key string, usage Usage) {
	if key == "" {
		key = "total"
	}
	fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%s\t%d\n",
		key,
		usage.InputTokens,
		usage.OutputTokens,
		usage.CacheReadInputTokens,
		usage.CacheCreationInputTokens,
		usage.ReasoningOutputTokens,
		formatCost(usage.CostUSD),
		usage.Events,
	)
}

func writeStepRows(w io.Writer, step *Step, depth int) {
	label := strings.TrimSpace(step.Name)
	if label == "" {
		label = step.Action
	}
	if label == "" {
		label = step.SpanID
	}
	fmt.Fprintf(w, "%s%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
		strings.Repeat("  ", depth),
		label,
		step.Model,
		step.Usage.InputTokens,
		step.Usage.OutputTokens,
		step.Usage.CacheReadInputTokens,
		step.Usage.CacheCreationInputTokens,
		step.Usage.ReasoningOutputTokens,
		formatCost(step.Usage.CostUSD),
	)
	for _, child := range step.Children {
		writeStepRows(w, child, depth+1)
	}
}

func formatCost(cost float64) string {
	if cost == 0 {
		return "-"
	}
	return fmt.Sprintf("%.4f", cost)
}
