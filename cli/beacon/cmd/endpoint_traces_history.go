package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
)

var endpointTraceHistoryOpts struct {
	retentionDays int
	maxSizeMB     int64
	rebuild       bool
	yes           bool
}

// traceHistoryIO is where the first-run prompt reads and writes; tests replace it.
var traceHistoryIO = struct {
	in          io.Reader
	out         io.Writer
	interactive func() bool
}{
	in:  os.Stdin,
	out: os.Stderr,
	interactive: func() bool {
		return isTerminal(os.Stdin) && isTerminal(os.Stdout) && !strings.EqualFold(os.Getenv("TERM"), "dumb")
	},
}

// offerTraceHistory runs before the trace commands read anything. On a machine that has not opted
// in to the local history it asks once, on an interactive terminal, whether to create it. Anything
// that is not a person at a terminal -- an agent calling through a shell, --json, a pipe, CI -- is
// never asked and gets one line on stderr instead, so a script is never left waiting on a prompt.
// hint is false for `status`, which says the same thing in its own output.
func offerTraceHistory(logPath string, hint bool) {
	if dashboard.HistoryStoreEnabled() {
		return
	}
	profile := onboarding.Load()
	if profile.HistoryDeclined() {
		return
	}
	if endpointOpts.jsonOutput || os.Getenv("CI") != "" || !traceHistoryIO.interactive() {
		if !hint {
			return
		}
		fmt.Fprintln(os.Stderr, "Beacon: reading traces from the runtime log, which holds about a day or two of activity. Run `beacon endpoint traces reindex` to keep a local history.")
		return
	}
	out := traceHistoryIO.out
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Beacon's runtime log rotates after about 60MB, a day or two of agent activity. A local")
	fmt.Fprintln(out, "  history keeps sessions after that, for 90 days or up to 1GB, and makes trace search faster.")
	fmt.Fprintf(out, "  It lives at %s, readable only by you, and holds the same prompts,\n", dashboard.TraceStorePath(logPath))
	fmt.Fprintln(out, "  commands and output as the log. Nothing leaves this machine. Remove it with")
	fmt.Fprintln(out, "  `beacon endpoint traces reset`.")
	fmt.Fprintln(out)
	index, err := onboarding.Choose(traceHistoryIO.in, out, "Keep a local history of your agent sessions?", []onboarding.Option{
		{Label: "Create it now", Detail: "Reads the current log once, then keeps up as you work."},
		{Label: "Not now", Detail: "Traces are read from the log each time. `beacon endpoint traces reindex` creates it later."},
	})
	if err != nil {
		// An aborted prompt answers nothing; the command runs on the log and asks again next time.
		return
	}
	if index == 1 {
		profile.History = &onboarding.History{DeclinedAt: time.Now().UTC().Format(time.RFC3339)}
		if err := onboarding.Save(profile); err != nil {
			fmt.Fprintf(out, "  Could not record the answer: %v\n", err)
		}
		return
	}
	status, err := dashboard.EnableHistoryStore(logPath, dashboard.HistoryOptions{Progress: historyProgress(out)})
	if err != nil {
		fmt.Fprintf(out, "\r  Could not create the local history: %v\n  Traces are read from the runtime log instead.\n\n", err)
		return
	}
	fmt.Fprintf(out, "\r  Local history ready: %d traces, %d events.            \n\n", status.Traces, status.Events)
}

// historyProgress draws one updating line while a catch-up reads the log.
func historyProgress(out io.Writer) func(done, total int64) {
	last := -1
	return func(done, total int64) {
		if total <= 0 {
			return
		}
		percent := int(done * 100 / total)
		if percent == last {
			return
		}
		last = percent
		fmt.Fprintf(out, "\r  Reading the runtime log... %d%%", percent)
	}
}

var endpointTracesResetCmd = &cobra.Command{
	Use:          "reset",
	Short:        "Delete the local trace history",
	Long:         "Delete the local trace history (history.db). The runtime log is not touched. Sessions the log has already rotated away cannot be recovered.",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := dashboard.TraceStorePath(endpointTraceLogPath())
		if !dashboard.HistoryStoreEnabled() {
			fmt.Fprintf(cmd.OutOrStdout(), "No local history at %s\n", path)
			return nil
		}
		if !endpointTraceHistoryOpts.yes {
			if !traceHistoryIO.interactive() {
				return errors.New("pass --yes to delete the local history without a prompt")
			}
			index, err := onboarding.Choose(traceHistoryIO.in, traceHistoryIO.out, "Delete the local history? Sessions no longer in the runtime log are lost.", []onboarding.Option{
				{Label: "Keep it"},
				{Label: "Delete it"},
			})
			if err != nil || index != 1 {
				fmt.Fprintln(cmd.OutOrStdout(), "Local history kept.")
				return nil
			}
		}
		if err := dashboard.ResetHistoryStore(); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted the local history at %s\n", path)
		return nil
	},
}

// maxHistorySizeMB bounds --max-size-mb well above any real disk, so the byte count cannot overflow.
const maxHistorySizeMB = 1 << 30

// maxHistoryRetentionDays is the longest --retention-days accepted: 100 years, which never expires.
const maxHistoryRetentionDays = 100 * 365

func runEndpointTracesReindex(cmd *cobra.Command, args []string) error {
	if endpointTraceHistoryOpts.retentionDays < 0 || endpointTraceHistoryOpts.retentionDays > maxHistoryRetentionDays {
		return fmt.Errorf("--retention-days must be between 0 (keep the current setting) and %d (never expire)", maxHistoryRetentionDays)
	}
	if endpointTraceHistoryOpts.maxSizeMB < 0 || endpointTraceHistoryOpts.maxSizeMB > maxHistorySizeMB {
		return fmt.Errorf("--max-size-mb must be between 0 (keep the current setting) and %d", maxHistorySizeMB)
	}
	logPath := endpointTraceLogPath()
	opts := dashboard.HistoryOptions{
		RetentionDays: endpointTraceHistoryOpts.retentionDays,
		MaxBytes:      endpointTraceHistoryOpts.maxSizeMB << 20,
		Rebuild:       endpointTraceHistoryOpts.rebuild,
	}
	if !endpointOpts.jsonOutput && isTerminal(os.Stderr) {
		opts.Progress = historyProgress(os.Stderr)
	}
	status, err := dashboard.EnableHistoryStore(logPath, opts)
	if opts.Progress != nil {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	if err != nil {
		return err
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Local history: %d traces and %d events in %s\n", status.Traces, status.Events, status.Path)
	return nil
}

func printTraceStoreStatus(out io.Writer, logPath string, status dashboard.HistoryStatus) {
	if !status.Enabled {
		fmt.Fprintf(out, "Local history: not set up (%s)\n", status.Path)
		fmt.Fprintf(out, "Runtime log: %s\n", logPath)
		fmt.Fprintln(out, "Traces are read from the runtime log. Run `beacon endpoint traces reindex` to keep a local history.")
		return
	}
	fmt.Fprintf(out, "Local history: %s\n", status.Path)
	fmt.Fprintf(out, "Runtime log: %s\n", logPath)
	fmt.Fprintf(out, "Traces: %d\nEvents: %d\nIndex rows: %d\n", status.Traces, status.Events, status.IndexRows)
	if status.OldestEventAt != "" {
		fmt.Fprintf(out, "Oldest event: %s\nNewest event: %s\n", status.OldestEventAt, status.NewestEventAt)
	}
	fmt.Fprintf(out, "Size: %s of %s", humanBytes(status.SizeBytes+status.WALBytes), humanBytes(status.MaxBytes))
	fmt.Fprintf(out, ", kept for %d days\n", status.RetentionDays)
	if status.Gaps > 0 {
		fmt.Fprintf(out, "Gaps: %d. A log file left before the history finished reading it, so events may be missing", status.Gaps)
		if status.GapBytes > 0 {
			fmt.Fprintf(out, " (at least %s of the log)", humanBytes(status.GapBytes))
		}
		fmt.Fprintln(out, ". Trace commands read new events as they run; the log rotates away unread only if more than about 60 MB is written between two of them.")
	}
	if status.IndexedAt != "" {
		fmt.Fprintf(out, "Last updated: %s\n", status.IndexedAt)
	}
}
