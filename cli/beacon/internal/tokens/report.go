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
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TOTALS\tINPUT\tOUTPUT\tCACHE READ\tCACHE CREATE\tREASONING\tCOST USD\tEST COST USD\tEVENTS")
	writeUsageRow(tw, "", report.Totals)
	tw.Flush()

	writeGroups(w, "BY MODEL", report.ByModel)
	writeGroups(w, "BY SESSION", report.BySession)
	writeGroups(w, "BY USER", report.ByUser)
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
		fmt.Fprintln(tw, "BUCKET\tINPUT\tOUTPUT\tCACHE READ\tCACHE CREATE\tREASONING\tCOST USD\tEST COST USD\tEVENTS")
		for _, bucket := range report.Series {
			writeUsageRow(tw, bucket.Start, bucket.Usage)
		}
		tw.Flush()
	}

	if report.SessionDetail != nil {
		fmt.Fprintf(w, "\nSESSION %s\n", report.SessionDetail.SessionID)
		tw = tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "STEP\tMODEL\tINPUT\tOUTPUT\tCACHE READ\tCACHE CREATE\tREASONING\tCOST USD\tEST COST USD")
		for _, step := range report.SessionDetail.Steps {
			writeStepRows(tw, step, 0)
		}
		tw.Flush()
	}

	writePricingFooter(w, report)
}

func writeGroups(w io.Writer, title string, groups []Group) {
	if len(groups) == 0 {
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\tINPUT\tOUTPUT\tCACHE READ\tCACHE CREATE\tREASONING\tCOST USD\tEST COST USD\tEVENTS\n", title)
	for _, group := range groups {
		writeUsageRow(tw, group.Key, group.Usage)
	}
	tw.Flush()
}

func writeUsageRow(w io.Writer, key string, usage Usage) {
	if key == "" {
		key = "total"
	}
	fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%d\n",
		key,
		usage.InputTokens,
		usage.OutputTokens,
		usage.CacheReadInputTokens,
		usage.CacheCreationInputTokens,
		usage.ReasoningOutputTokens,
		formatCost(usage.CostUSD),
		formatEstimate(usage),
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
	fmt.Fprintf(w, "%s%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
		strings.Repeat("  ", depth),
		label,
		step.Model,
		step.Usage.InputTokens,
		step.Usage.OutputTokens,
		step.Usage.CacheReadInputTokens,
		step.Usage.CacheCreationInputTokens,
		step.Usage.ReasoningOutputTokens,
		formatCost(step.Usage.CostUSD),
		formatEstimate(step.Usage),
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

// formatEstimate prints the list-price estimate from its exact microdollar sum when the usage
// came from the aggregator, so a printed figure never carries float drift. A row whose tokens
// were all unpriced prints "?" rather than "-": its cost is unknown, not zero.
func formatEstimate(usage Usage) string {
	if usage.estimated > 0 {
		return usage.estimated.Format(4)
	}
	if usage.EstimatedCostUSD > 0 {
		return fmt.Sprintf("%.4f", usage.EstimatedCostUSD)
	}
	if usage.UnpricedEvents > 0 {
		return "?"
	}
	return "-"
}

// writePricingFooter says which cost column is which, where the estimate came from, and what it
// leaves out. It is printed whenever there is usage, because a reader who sees two cost columns
// must be able to tell them apart without the docs.
func writePricingFooter(w io.Writer, report Report) {
	if report.EventsWithUsage == 0 || report.Pricing == nil {
		return
	}
	catalog := report.Pricing.Catalog
	provenance := "generated " + catalog.GeneratedAt
	if commit := catalog.Commit; commit != "" {
		if len(commit) > 12 {
			commit = commit[:12]
		}
		provenance = "commit " + commit + ", " + provenance
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "COST USD is what the runtimes reported. EST COST USD is an estimate at list price:")
	fmt.Fprintf(w, "  prices from %s (%s);\n", catalog.Name, provenance)
	fmt.Fprintf(w, "  %s tier, no batch, priority or subscription discounts;\n", report.Pricing.Tier)
	fmt.Fprintln(w, "  cache writes are priced at the 5m rate unless the source reported them as 1h writes.")
	totals := report.Totals
	fmt.Fprintf(w, "Effective cost (reported where the runtime reported one, the estimate elsewhere): %s USD",
		effectiveString(totals))
	if totals.CostSource != "" {
		fmt.Fprintf(w, " [%s]", totals.CostSource)
	}
	fmt.Fprintln(w)
	if unpriced := report.Pricing.Unpriced; len(unpriced) > 0 {
		names := make([]string, 0, len(unpriced))
		for _, model := range unpriced {
			name := model.Model
			if name == "" {
				name = "(no model)"
			}
			names = append(names, name)
		}
		fmt.Fprintf(w, "Not in EST COST USD: %d event(s), %d tokens from models the catalog does not price: %s\n",
			totals.UnpricedEvents, totals.UnpricedTokens, strings.Join(names, ", "))
	}
}

func effectiveString(usage Usage) string {
	if usage.effectiveReported == 0 {
		return usage.effectiveEstimated.Format(4)
	}
	return fmt.Sprintf("%.4f", usage.EffectiveCostUSD)
}

// RenderCoverageText writes the token coverage report: which runtimes contributed token telemetry
// in the window and which did not.
//
// Silent rows are the point of the report, so the summary line leads with them and the table is
// sorted to put them first. A reader who only reads one line should learn whether anything needs
// investigating.
func RenderCoverageText(w io.Writer, report CoverageReport) {
	fmt.Fprintf(w, "Token coverage over %d events: %d runtime(s) reporting usage, %d silent\n",
		report.TotalEvents, report.Covered, report.Silent)
	if report.TotalEvents == 0 {
		fmt.Fprintln(w, "\nNo events in this window, so coverage cannot be judged. Widen --since/--until.")
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "RUNTIME\tSTATUS\tINSTALLED\tEVENTS\tUSAGE EVENTS\tTOKENS\tNOTE")
	for _, runtime := range report.Runtimes {
		installed := "no"
		if runtime.Installed {
			installed = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
			runtime.Harness, runtime.Status, installed, runtime.Events,
			runtime.UsageEvents, runtime.Tokens, runtime.Reason)
	}
	tw.Flush()

	if report.Silent > 0 {
		fmt.Fprintln(w, "\nSilent runtimes produced events but no token usage, and Beacon is built to read")
		fmt.Fprintln(w, "usage from them. Check that the runtime is current and its hooks or OTLP export")
		fmt.Fprintln(w, "are still configured: beacon endpoint status, beacon endpoint diagnostics.")
		fmt.Fprintln(w, "Where the note names a sync command, usage arrives only when that sync runs.")
	}
}
