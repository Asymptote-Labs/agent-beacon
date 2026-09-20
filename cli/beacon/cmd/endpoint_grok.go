package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/groksession"
	"github.com/spf13/cobra"
)

var endpointGrokCmd = &cobra.Command{
	Use:   "grok",
	Short: "Collect telemetry from Grok Build sessions",
	Long: `Collect endpoint telemetry from Grok Build's local session store.

The live Grok hook integration remains available through
'beacon endpoint hooks install --harness grok'. This command reads committed
session files under ~/.grok/sessions and converts them into endpoint events
marked harness.collection_method=poll, so it can backfill historical sessions
but cannot hold, allow, or deny tool calls.`,
}

var endpointGrokSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Grok Build session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointGrokSync,
}

var endpointGrokStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Grok Build sessions on this machine and how much has been collected",
	SilenceUsage: true,
	RunE:         runEndpointGrokStatus,
}

var endpointGrokOpts struct {
	sessionsDir string
	statePath   string
	logPath     string
	print       bool
	watch       bool
	interval    time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointGrokCmd)
	endpointGrokCmd.AddCommand(endpointGrokSyncCmd)
	endpointGrokCmd.AddCommand(endpointGrokStatusCmd)

	for _, c := range []*cobra.Command{endpointGrokSyncCmd, endpointGrokStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointGrokOpts.sessionsDir, "sessions-dir", "", "Grok session directory (default ~/.grok/sessions)")
		f.StringVar(&endpointGrokOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/grok-sessions.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointGrokSyncCmd.Flags()
	sync.StringVar(&endpointGrokOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointGrokOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointGrokOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointGrokOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointGrokSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := groksession.CollectOptions{
		SessionsDir: endpointGrokOpts.sessionsDir,
		Print:       endpointGrokOpts.print,
		Out:         cmd.OutOrStdout(),
		Write:       !endpointGrokOpts.print,
		UserMode:    userMode,
	}
	if !endpointGrokOpts.print {
		opts.StatePath = resolveGrokStatePath(endpointGrokOpts.statePath)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointGrokOpts.logPath).EffectiveLogPath
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointGrokOpts.watch && !endpointGrokOpts.print {
		return watchGrok(ctx, cmd, opts)
	}
	summary, err := groksession.CollectOnce(opts)
	reportGrokSweep(cmd, summary)
	return err
}

func watchGrok(ctx context.Context, cmd *cobra.Command, opts groksession.CollectOptions) error {
	interval := endpointGrokOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := groksession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "grok sync error: %v\n", err)
		}
		reportGrokSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportGrokSweep(cmd *cobra.Command, summary groksession.SummaryResult) {
	if endpointGrokOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "grok sync: %d sessions, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	if summary.MalformedLines > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d unreadable line(s) in Grok session logs\n", summary.MalformedLines)
	}
}

func runEndpointGrokStatus(cmd *cobra.Command, args []string) error {
	store, err := groksession.NewStore(endpointGrokOpts.sessionsDir)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}
	state, err := groksession.LoadState(resolveGrokStatePath(endpointGrokOpts.statePath))
	if err != nil {
		return err
	}
	type status struct {
		Sessions  int    `json:"sessions"`
		Collected int    `json:"collected"`
		StatePath string `json:"state_path"`
	}
	out := status{Sessions: len(refs), StatePath: resolveGrokStatePath(endpointGrokOpts.statePath)}
	for _, ref := range refs {
		cursor := state.Sessions[ref.ID]
		if cursor != nil && cursor.UpdatedAtMS >= ref.ModTimeUnixMS && cursor.Events > 0 {
			out.Collected++
		}
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "grok sessions: %d found, %d collected\n", out.Sessions, out.Collected)
	if out.Collected < out.Sessions {
		fmt.Fprintln(cmd.OutOrStdout(), "  run `beacon endpoint grok sync` to collect new committed sessions")
	}
	return nil
}

func resolveGrokStatePath(path string) string {
	if path != "" {
		return path
	}
	return groksession.DefaultStatePath()
}
