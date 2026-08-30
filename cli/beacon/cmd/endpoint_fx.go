package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/fxsession"
	"github.com/spf13/cobra"
)

// `beacon endpoint fx` collects telemetry from fx (vercel-labs/fx).
//
// It is a command rather than an install step because fx has nothing to install into. Every other
// supported runtime exposes a hook config, a plugin directory, or an OTLP endpoint, so Beacon
// writes something once and the runtime calls it. fx has none of those: what it has is a durable
// session log, so collecting from it means reading that log, which is an action rather than a
// configuration.
//
// Everything it reads is already on this machine and nothing it does reaches the network.
var endpointFxCmd = &cobra.Command{
	Use:   "fx",
	Short: "Collect telemetry from fx (vercel-labs/fx) sessions",
	Long: `Collect endpoint telemetry from fx, Vercel Labs' native coding agent.

fx exposes no hook, plugin, or OpenTelemetry surface for third parties, so Beacon
reads the session records fx commits under ~/.fx/sessions and converts them into
endpoint events. Every event is marked harness.collection_method=poll: Beacon sees
what a turn did once fx committed it, rather than observing the agent as it works,
so nothing here can hold or deny a tool call.

Reading is local and offline. Run 'sync' on a schedule, or with --watch, to keep the
runtime log current.`,
}

var endpointFxSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new fx session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointFxSync,
}

var endpointFxStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show fx sessions on this machine and how much of each has been collected",
	SilenceUsage: true,
	RunE:         runEndpointFxStatus,
}

var endpointFxOpts struct {
	sessionsDir string
	statePath   string
	logPath     string
	print       bool
	watch       bool
	interval    time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointFxCmd)
	endpointFxCmd.AddCommand(endpointFxSyncCmd)
	endpointFxCmd.AddCommand(endpointFxStatusCmd)

	for _, c := range []*cobra.Command{endpointFxSyncCmd, endpointFxStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointFxOpts.sessionsDir, "sessions-dir", "", "fx session directory (default ~/.fx/sessions)")
		f.StringVar(&endpointFxOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/fx.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointFxSyncCmd.Flags()
	sync.StringVar(&endpointFxOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointFxOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointFxOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointFxOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointFxSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := fxsession.CollectOptions{
		SessionsDir: endpointFxOpts.sessionsDir,
		Print:       endpointFxOpts.print,
		Out:         cmd.OutOrStdout(),
		Write:       !endpointFxOpts.print,
		UserMode:    userMode,
	}
	// --print is a dry run in both directions: it neither writes the runtime log nor advances the
	// cursor, so running it twice shows the same events and running it does not quietly consume
	// the work a later real sweep would do.
	if !endpointFxOpts.print {
		opts.StatePath = resolveFxStatePath(endpointFxOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointFxOpts.logPath).EffectiveLogPath
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointFxOpts.watch && !endpointFxOpts.print {
		return watchFx(ctx, cmd, opts)
	}

	summary, err := fxsession.CollectOnce(opts)
	// The summary is reported even when the sweep hit an error, because a sweep that collected
	// nine sessions and failed on the tenth did nine sessions' worth of work.
	reportFxSweep(cmd, summary)
	return err
}

func watchFx(ctx context.Context, cmd *cobra.Command, opts fxsession.CollectOptions) error {
	interval := endpointFxOpts.interval
	if interval < 5*time.Second {
		// A sweep re-reads each changed session's log, so a very short interval spends real work
		// to shorten a window that fx's own commit latency already bounds.
		interval = 5 * time.Second
	}
	for {
		summary, err := fxsession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "fx sync error: %v\n", err)
		}
		reportFxSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportFxSweep(cmd *cobra.Command, summary fxsession.Summary) {
	if endpointFxOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "fx sync: %d sessions, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	// Said out loud rather than left in a counter: a damaged session log and an empty one produce
	// the same event count, and only one of them is a reason to look at the machine.
	if summary.MalformedLines > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d unreadable line(s) in fx session logs\n", summary.MalformedLines)
	}
	if summary.PartialSessions > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d session(s) had a partly written record; it will be read on the next sweep\n", summary.PartialSessions)
	}
}

// fxSessionStatus is one session as `status` reports it: what fx has, and how much of it Beacon has
// collected.
type fxSessionStatus struct {
	SessionID     string `json:"session_id"`
	Workspace     string `json:"workspace,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	Turns         int    `json:"turns,omitempty"`
	LastEventSeq  uint64 `json:"last_event_seq,omitempty"`
	CollectedSeq  uint64 `json:"collected_seq"`
	Collected     bool   `json:"collected"`
	SizeBytes     int64  `json:"size_bytes"`
	ManifestFound bool   `json:"manifest_found"`
}

type fxStatusReport struct {
	SessionsDir string            `json:"sessions_dir"`
	Present     bool              `json:"present"`
	StatePath   string            `json:"state_path,omitempty"`
	Sessions    []fxSessionStatus `json:"sessions"`
}

func runEndpointFxStatus(cmd *cobra.Command, args []string) error {
	store, err := fxsession.NewStore(endpointFxOpts.sessionsDir)
	if err != nil {
		return err
	}
	statePath := resolveFxStatePath(endpointFxOpts.statePath, endpointUserMode())
	state, err := fxsession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}

	report := fxStatusReport{
		SessionsDir: store.Dir,
		Present:     store.Exists(),
		StatePath:   statePath,
		Sessions:    make([]fxSessionStatus, 0, len(refs)),
	}
	for _, ref := range refs {
		status := fxSessionStatus{
			SessionID:     ref.ID,
			SizeBytes:     ref.SizeBytes,
			ManifestFound: ref.Manifest != nil,
		}
		if ref.Manifest != nil {
			status.Workspace = ref.Manifest.WorkspaceRoot
			status.Turns = ref.Manifest.HistoryLen
			status.LastEventSeq = ref.Manifest.LastEventSeq
			if ref.Manifest.UpdatedAtMS > 0 {
				status.UpdatedAt = time.UnixMilli(ref.Manifest.UpdatedAtMS).UTC().Format(time.RFC3339)
			}
		}
		if cursor := state.Sessions[ref.ID]; cursor != nil {
			status.CollectedSeq = cursor.LastSeq
			// "Collected" means caught up with what fx says it has committed, which is the question
			// someone running this command is actually asking. A session with no manifest cannot
			// answer it, so it is reported as not caught up rather than assumed to be.
			status.Collected = ref.Manifest != nil && cursor.Generation == ref.Manifest.LogGeneration &&
				cursor.LastSeq >= ref.Manifest.LastEventSeq
		}
		report.Sessions = append(report.Sessions, status)
	}

	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	out := cmd.OutOrStdout()
	if !report.Present {
		fmt.Fprintf(out, "fx sessions: none (%s does not exist)\n", report.SessionsDir)
		return nil
	}
	fmt.Fprintf(out, "fx sessions: %d in %s\n", len(report.Sessions), report.SessionsDir)
	for _, session := range report.Sessions {
		state := "pending"
		if session.Collected {
			state = "collected"
		}
		fmt.Fprintf(out, "  %s  %s  seq %d/%d  %s\n",
			session.SessionID, state, session.CollectedSeq, session.LastEventSeq,
			strings.TrimSpace(session.Workspace))
	}
	return nil
}

// resolveFxStatePath always returns a path so the cursor survives between runs. Without one, every
// scheduled sweep would re-append every session's whole history.
//
// A system-mode sweep keeps its cursor beside the system runtime log; everything else uses the
// per-user default in fxsession.DefaultStatePath.
func resolveFxStatePath(override string, userMode bool) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if !userMode {
		// A system-mode sweep collects on behalf of the machine, so its cursor belongs beside the
		// system runtime log rather than in whichever home the operator happened to run from.
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "fx-state.json")
		}
	}
	return fxsession.DefaultStatePath()
}
