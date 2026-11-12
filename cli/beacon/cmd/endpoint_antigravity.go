package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/antigravitysession"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	endpointintegrations "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/spf13/cobra"
)

var endpointAntigravityCmd = &cobra.Command{
	Use:     "antigravity",
	Aliases: []string{"antigravity-cli"},
	Short:   "Collect telemetry from Antigravity CLI conversations",
	Long: `Collect endpoint telemetry from Antigravity CLI's local conversation store.

The live Antigravity hook integration remains available through
'beacon endpoint hooks install --harness antigravity'. This command reads the
transcripts Antigravity commits under ~/.gemini/antigravity-cli/brain, with each
conversation's working directory from conversation_summaries.db beside it, and
converts them into endpoint events marked harness.collection_method=poll, so it
can backfill historical conversations and the agent messages, reasoning and
token usage the hook payloads do not carry, but cannot hold, allow, or deny
tool calls. Conversations the live hooks already cover, from the first hook
event in the runtime log, get only what the hook payload cannot carry: agent
messages, reasoning and token usage.`,
}

var endpointAntigravitySyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Antigravity CLI conversation steps into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointAntigravitySync,
}

var endpointAntigravityStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Antigravity CLI conversations on this machine and how much has been collected",
	SilenceUsage: true,
	RunE:         runEndpointAntigravityStatus,
}

var endpointAntigravityOpts struct {
	brainDir    string
	statePath   string
	logPath     string
	print       bool
	watch       bool
	interval    time.Duration
	rotateBytes int64
}

func init() {
	endpointCmd.AddCommand(endpointAntigravityCmd)
	endpointAntigravityCmd.AddCommand(endpointAntigravitySyncCmd)
	endpointAntigravityCmd.AddCommand(endpointAntigravityStatusCmd)

	for _, c := range []*cobra.Command{endpointAntigravitySyncCmd, endpointAntigravityStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointAntigravityOpts.brainDir, "brain-dir", "", "Antigravity CLI brain directory; conversation_summaries.db is read from beside it (default ~/.gemini/antigravity-cli/brain)")
		f.StringVar(&endpointAntigravityOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/antigravity-sessions.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointAntigravitySyncCmd.Flags()
	sync.StringVar(&endpointAntigravityOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointAntigravityOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointAntigravityOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointAntigravityOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
	addSyncRotateBytesFlag(sync, &endpointAntigravityOpts.rotateBytes)
}

func runEndpointAntigravitySync(cmd *cobra.Command, args []string) error {
	if err := checkSyncRotateBytes(endpointAntigravityOpts.rotateBytes); err != nil {
		return err
	}
	userMode := endpointUserMode()
	logPath := lifecycle.ResolveRuntimeLog(userMode, endpointAntigravityOpts.logPath).EffectiveLogPath
	opts := antigravitysession.CollectOptions{
		RotateBytes: endpointAntigravityOpts.rotateBytes,
		BrainDir:    endpointAntigravityOpts.brainDir,
		Print:       endpointAntigravityOpts.print,
		Out:         cmd.OutOrStdout(),
		Write:       !endpointAntigravityOpts.print,
		UserMode:    userMode,
		// Live coverage comes from the log this sweep writes to, in print and write modes
		// alike: a dry run should show exactly what a real sweep would emit.
		LiveCoverage: endpointAntigravityLiveCoverage(logPath),
	}
	if !endpointAntigravityOpts.print {
		opts.StatePath = resolveAntigravityStatePath(endpointAntigravityOpts.statePath, userMode)
		opts.LogPath = logPath
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointAntigravityOpts.watch && !endpointAntigravityOpts.print {
		return watchAntigravity(ctx, cmd, opts)
	}
	summary, err := collectUnderSweepLock(opts.Write, opts.UserMode, endpointSweepLockWait, antigravitysession.CollectOnce, opts)
	reportAntigravitySweep(cmd, summary)
	return err
}

func watchAntigravity(ctx context.Context, cmd *cobra.Command, opts antigravitysession.CollectOptions) error {
	interval := endpointAntigravityOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		// Coverage is rebuilt every tick: a conversation the hooks start recording after the watch
		// began would otherwise be missing from it, and every tick would record it a second time.
		opts.LiveCoverage = endpointAntigravityLiveCoverage(opts.LogPath)
		summary, err := collectUnderSweepLock(opts.Write, opts.UserMode, 0, antigravitysession.CollectOnce, opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "antigravity sync error: %v\n", err)
		}
		reportAntigravitySweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportAntigravitySweep(cmd *cobra.Command, summary antigravitysession.SummaryResult) {
	if endpointAntigravityOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "antigravity sync: %d conversations, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	reportSyncRetention(cmd.OutOrStdout(), summary.Retention)
	if summary.MalformedLines > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d unreadable line(s) in Antigravity CLI transcripts\n", summary.MalformedLines)
	}
	if summary.EventsSuppressedLive > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d event(s) skipped as already recorded by the live Antigravity hooks\n", summary.EventsSuppressedLive)
	}
}

func runEndpointAntigravityStatus(cmd *cobra.Command, args []string) error {
	// The conversations come back even when the summaries database could not be read; the status
	// is still right about them, so the error is a warning here.
	refs, listErr := antigravitysession.NewStore(endpointAntigravityOpts.brainDir).List()
	if listErr != nil && len(refs) == 0 {
		return listErr
	}
	if listErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", listErr)
	}
	statePath := resolveAntigravityStatePath(endpointAntigravityOpts.statePath, endpointUserMode())
	state, err := antigravitysession.LoadState(statePath)
	if err != nil {
		return err
	}
	type status struct {
		Sessions  int    `json:"sessions"`
		Collected int    `json:"collected"`
		StatePath string `json:"state_path"`
	}
	out := status{Sessions: len(refs), StatePath: statePath}
	for _, ref := range refs {
		cursor := state.Sessions[ref.ID]
		if cursor != nil && cursor.UpdatedAtMS >= ref.ModTimeUnixMS && cursor.Events > 0 {
			out.Collected++
		}
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "antigravity conversations: %d found, %d collected\n", out.Sessions, out.Collected)
	if out.Collected < out.Sessions {
		fmt.Fprintln(cmd.OutOrStdout(), "  run `beacon endpoint antigravity sync` to collect new committed conversations")
	}
	return nil
}

// resolveAntigravityStatePath picks the cursor file for the mode the sweep is running in, the same
// split resolveGrokStatePath makes: a system sweep collects on behalf of the machine, so its cursor
// belongs beside the system state rather than in one operator's home.
func resolveAntigravityStatePath(path string, userMode bool) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	if userMode {
		return antigravitysession.DefaultStatePath()
	}
	return filepath.Join(endpointconfig.BaseDir(false), "state", "antigravity-sessions.json")
}

// endpointAntigravityLiveCoverage is the live hook coverage of each Antigravity conversation in the
// retained runtime log, for the sync command and the backfill runner alike.
func endpointAntigravityLiveCoverage(logPath string) sessionsync.LiveCoverage {
	return endpointintegrations.HookCoverage(logPath, "antigravity", antigravitysession.Harness)
}
