package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/harness2session"
	"github.com/spf13/cobra"
)

// `beacon endpoint harness2` collects telemetry from Harness² (google-research/harness2) rollouts.
//
// It is a command rather than an install step for the reason fx's is: there is nothing to install
// into. Harness² runs OpenCode and Codex under environments it builds itself -- Codex with its own
// CODEX_HOME and --ephemeral, OpenCode from a source checkout -- so Beacon's hooks and plugins for
// those agents are not what records these sessions. What Harness² keeps is each rollout's event
// stream under its runs directory, so collecting from it means reading those files.
//
// Everything it reads is already on this machine and nothing it does reaches the network.
var endpointHarness2Cmd = &cobra.Command{
	Use:   "harness2",
	Short: "Collect telemetry from Harness² (google-research/harness2) rollouts",
	Long: `Collect endpoint telemetry from Harness², Google Research's framework for
recursive agent harnessing.

Harness² refines an OpenCode or Codex executor's harness by running it over
benchmark tasks many times. It records each rollout's event stream under its
runs directory ($HARNESS2_RUNS_DIR, or runs/ in its checkout), and Beacon reads
those streams into endpoint events with harness.name=harness2, keeping the
benchmark, task, run and executor of each rollout beside its events. Every event
is marked harness.collection_method=poll: Beacon sees a rollout once Harness²
recorded it, so nothing here can hold or deny a tool call.

Name a checkout's runs directory once with --runs-dir; later syncs, and the
install-time backfill, keep reading it. Reading is local and offline.`,
}

var endpointHarness2SyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Harness² rollout events into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointHarness2Sync,
}

var endpointHarness2StatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Harness² rollouts on this machine and how much of each has been collected",
	SilenceUsage: true,
	RunE:         runEndpointHarness2Status,
}

var endpointHarness2Opts struct {
	runsDirs    []string
	statePath   string
	logPath     string
	print       bool
	watch       bool
	interval    time.Duration
	rotateBytes int64
}

func init() {
	endpointCmd.AddCommand(endpointHarness2Cmd)
	endpointHarness2Cmd.AddCommand(endpointHarness2SyncCmd)
	endpointHarness2Cmd.AddCommand(endpointHarness2StatusCmd)

	for _, c := range []*cobra.Command{endpointHarness2SyncCmd, endpointHarness2StatusCmd} {
		f := c.Flags()
		f.StringArrayVar(&endpointHarness2Opts.runsDirs, "runs-dir", nil, "Harness² runs directory; repeatable (default $HARNESS2_RUNS_DIR or ~/harness2/runs, plus directories earlier syncs were given)")
		f.StringVar(&endpointHarness2Opts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/harness2.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointHarness2SyncCmd.Flags()
	sync.StringVar(&endpointHarness2Opts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointHarness2Opts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointHarness2Opts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointHarness2Opts.interval, "interval", time.Minute, "Sweep interval for --watch")
	addSyncRotateBytesFlag(sync, &endpointHarness2Opts.rotateBytes)
}

func runEndpointHarness2Sync(cmd *cobra.Command, args []string) error {
	if err := checkSyncRotateBytes(endpointHarness2Opts.rotateBytes); err != nil {
		return err
	}
	userMode := endpointUserMode()
	opts := harness2session.CollectOptions{
		RotateBytes: endpointHarness2Opts.rotateBytes,
		RunsDirs:    endpointHarness2Opts.runsDirs,
		Print:       endpointHarness2Opts.print,
		Out:         cmd.OutOrStdout(),
		Write:       !endpointHarness2Opts.print,
		UserMode:    userMode,
	}
	// --print is a dry run in both directions: it neither writes the runtime log nor advances the
	// cursor -- nor remembers a --runs-dir -- so running it changes nothing a later sweep would see.
	if !endpointHarness2Opts.print {
		opts.StatePath = resolveHarness2StatePath(endpointHarness2Opts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointHarness2Opts.logPath).EffectiveLogPath
	} else {
		// A dry run still reads the directories earlier syncs remembered, so it shows what a real
		// sweep would read; it only never writes the cursor file.
		opts.RunsDirs = harness2PrintRoots(endpointHarness2Opts.runsDirs, resolveHarness2StatePath(endpointHarness2Opts.statePath, userMode))
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointHarness2Opts.watch && !endpointHarness2Opts.print {
		return watchHarness2(ctx, cmd, opts)
	}

	summary, err := harness2session.CollectOnce(opts)
	// Reported even when the sweep hit an error: a sweep that read nine rollouts and failed on the
	// tenth did nine rollouts' worth of work.
	reportHarness2Sweep(cmd, summary)
	return err
}

// harness2PrintRoots is the roots a dry run reads: the ones given, or the defaults and the ones a
// real sync remembered in the cursor file at statePath.
func harness2PrintRoots(runsDirs []string, statePath string) []string {
	if len(runsDirs) > 0 {
		return runsDirs
	}
	state, err := harness2session.LoadState(statePath)
	if err != nil {
		return nil
	}
	roots, err := harness2session.Roots(nil, state)
	if err != nil {
		return nil
	}
	return roots
}

func watchHarness2(ctx context.Context, cmd *cobra.Command, opts harness2session.CollectOptions) error {
	interval := endpointHarness2Opts.interval
	if interval < 5*time.Second {
		// A sweep walks every runs root, so a very short interval spends real work on directories
		// that hold thousands of finished rollouts.
		interval = 5 * time.Second
	}
	for {
		summary, err := harness2session.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Harness² sync error: %v\n", err)
		}
		reportHarness2Sweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportHarness2Sweep(cmd *cobra.Command, summary harness2session.Summary) {
	if endpointHarness2Opts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Harness² sync: %d rollouts, %d changed, %d events, %d errors\n",
		summary.Trajectories, summary.FilesChanged, summary.EventsEmitted, summary.Errors)
	reportSyncRetention(cmd.OutOrStdout(), summary.Retention)
	if summary.Trajectories == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  no rollouts found in %s; name a checkout's runs directory with --runs-dir\n",
			strings.Join(summary.Roots, ", "))
	}
	if summary.PartialFiles > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d rollout(s) still being written; the rest is read on the next sweep\n", summary.PartialFiles)
	}
}

// harness2RolloutStatus is one trajectory as `status` reports it.
type harness2RolloutStatus struct {
	Path      string `json:"path"`
	Executor  string `json:"executor"`
	Benchmark string `json:"benchmark,omitempty"`
	Subset    string `json:"subset,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	Label     string `json:"label,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Trial     string `json:"trial,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	Events    int    `json:"events"`
	// Collected means the file is unchanged since a sweep last read all of it.
	Collected bool `json:"collected"`
}

type harness2StatusReport struct {
	Roots     []string                `json:"roots"`
	Present   bool                    `json:"present"`
	StatePath string                  `json:"state_path,omitempty"`
	Rollouts  []harness2RolloutStatus `json:"rollouts"`
	// Errors names each runs directory that could not be read.
	Errors []string `json:"errors,omitempty"`
}

func runEndpointHarness2Status(cmd *cobra.Command, args []string) error {
	statePath := resolveHarness2StatePath(endpointHarness2Opts.statePath, endpointUserMode())
	state, err := harness2session.LoadState(statePath)
	if err != nil {
		return err
	}
	roots, err := harness2session.Roots(endpointHarness2Opts.runsDirs, state)
	if err != nil {
		return err
	}
	store := &harness2session.Store{Roots: roots}
	// A root that cannot be read is reported beside the rollouts the others hold, as a sync reports
	// it, rather than hiding them.
	refs, listErr := store.List()
	report := harness2StatusReport{
		Roots:     roots,
		Present:   store.Exists(),
		StatePath: statePath,
		Rollouts:  make([]harness2RolloutStatus, 0, len(refs)),
	}
	for _, ref := range refs {
		status := harness2RolloutStatus{
			Path:      ref.Path,
			Executor:  ref.Format.Executor(),
			Benchmark: ref.Context.Benchmark,
			Subset:    ref.Context.Subset,
			TaskID:    ref.Context.TaskID,
			Label:     ref.Context.Label,
			RunID:     ref.Context.RunID,
			Trial:     ref.Context.Trial,
			SizeBytes: ref.SizeBytes,
		}
		if ref.ModTimeUnixMS > 0 {
			status.UpdatedAt = time.UnixMilli(ref.ModTimeUnixMS).UTC().Format(time.RFC3339)
		}
		if cursor := state.Files[ref.Path]; cursor != nil {
			status.SessionID = cursor.SessionID
			status.Events = cursor.Events
			status.Collected = cursor.SizeBytes == ref.SizeBytes && cursor.ModTimeUnixMS == ref.ModTimeUnixMS
		}
		report.Rollouts = append(report.Rollouts, status)
	}

	if listErr != nil {
		report.Errors = strings.Split(listErr.Error(), "\n")
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	out := cmd.OutOrStdout()
	for _, msg := range report.Errors {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg)
	}
	if !report.Present {
		fmt.Fprintf(out, "Harness² rollouts: none (no runs directory at %s); name one with --runs-dir\n", strings.Join(report.Roots, ", "))
		return nil
	}
	fmt.Fprintf(out, "Harness² rollouts: %d in %s\n", len(report.Rollouts), strings.Join(report.Roots, ", "))
	for _, rollout := range report.Rollouts {
		state := "pending"
		if rollout.Collected {
			state = "collected"
		}
		what := strings.Join(nonEmpty(rollout.Benchmark, rollout.Subset, rollout.TaskID, rollout.Label, rollout.RunID, rollout.Trial), " ")
		fmt.Fprintf(out, "  %-9s %-8s %4d events  %s  %s\n", state, rollout.Executor, rollout.Events, what, rollout.Path)
	}
	return nil
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

// resolveHarness2StatePath always returns a path so the cursor survives between runs. A system-mode
// sweep keeps its cursor beside the system runtime log; everything else uses the per-user default.
func resolveHarness2StatePath(override string, userMode bool) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if !userMode {
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "harness2-state.json")
		}
	}
	return harness2session.DefaultStatePath()
}
