package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/factorysession"
	"github.com/spf13/cobra"
)

var endpointFactoryCmd = &cobra.Command{
	Use:   "factory",
	Short: "Collect telemetry from Factory Droid sessions",
	Long: `Collect endpoint telemetry from Factory Droid's local session store.

Factory already supports live OTLP and hook telemetry. This command adds an
after-the-fact local reader for records Factory commits under ~/.factory/sessions.
Every event is marked harness.collection_method=poll: Beacon sees committed
session records rather than observing or controlling the agent as it works.

Reading is local and offline. Run 'sync' on a schedule, or with --watch, to keep
the runtime log current.`,
}

var endpointFactorySyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Factory session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointFactorySync,
}

var endpointFactoryStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Factory sessions on this machine and how much has been collected",
	SilenceUsage: true,
	RunE:         runEndpointFactoryStatus,
}

var endpointFactoryOpts struct {
	sessionsDir string
	statePath   string
	logPath     string
	print       bool
	watch       bool
	interval    time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointFactoryCmd)
	endpointFactoryCmd.AddCommand(endpointFactorySyncCmd)
	endpointFactoryCmd.AddCommand(endpointFactoryStatusCmd)

	for _, c := range []*cobra.Command{endpointFactorySyncCmd, endpointFactoryStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointFactoryOpts.sessionsDir, "sessions-dir", "", "Factory session directory (default ~/.factory/sessions)")
		f.StringVar(&endpointFactoryOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/factory.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointFactorySyncCmd.Flags()
	sync.StringVar(&endpointFactoryOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointFactoryOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointFactoryOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointFactoryOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointFactorySync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := factorysession.CollectOptions{
		SessionsDir: endpointFactoryOpts.sessionsDir,
		Print:       endpointFactoryOpts.print,
		Out:         cmd.OutOrStdout(),
		Write:       !endpointFactoryOpts.print,
		UserMode:    userMode,
	}
	if !endpointFactoryOpts.print {
		opts.StatePath = resolveFactoryStatePath(endpointFactoryOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointFactoryOpts.logPath).EffectiveLogPath
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointFactoryOpts.watch && !endpointFactoryOpts.print {
		return watchFactory(ctx, cmd, opts)
	}
	summary, err := factorysession.CollectOnce(opts)
	reportFactorySweep(cmd, summary)
	return err
}

func watchFactory(ctx context.Context, cmd *cobra.Command, opts factorysession.CollectOptions) error {
	interval := endpointFactoryOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := factorysession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Factory sync error: %v\n", err)
		}
		reportFactorySweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportFactorySweep(cmd *cobra.Command, summary factorysession.Summary) {
	if endpointFactoryOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Factory sync: %d sessions, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	if summary.MalformedLines > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d unreadable line(s) in Factory session logs\n", summary.MalformedLines)
	}
	if summary.PartialSessions > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d session(s) had a partly written record; it will be read on the next sweep\n", summary.PartialSessions)
	}
}

type factorySessionStatus struct {
	SessionID     string `json:"session_id"`
	Workspace     string `json:"workspace,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	CollectedLine int    `json:"collected_line"`
	Lines         int    `json:"lines"`
	Collected     bool   `json:"collected"`
	SizeBytes     int64  `json:"size_bytes"`
	Model         string `json:"model,omitempty"`
}

type factoryStatusReport struct {
	SessionsDir string                 `json:"sessions_dir"`
	Present     bool                   `json:"present"`
	StatePath   string                 `json:"state_path,omitempty"`
	Sessions    []factorySessionStatus `json:"sessions"`
}

func runEndpointFactoryStatus(cmd *cobra.Command, args []string) error {
	store, err := factorysession.NewStore(endpointFactoryOpts.sessionsDir)
	if err != nil {
		return err
	}
	statePath := resolveFactoryStatePath(endpointFactoryOpts.statePath, endpointUserMode())
	state, err := factorysession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}
	report := factoryStatusReport{
		SessionsDir: store.Dir,
		Present:     store.Exists(),
		StatePath:   statePath,
		Sessions:    make([]factorySessionStatus, 0, len(refs)),
	}
	for _, ref := range refs {
		records, stats, _ := store.Read(ref)
		_ = records
		status := factorySessionStatus{
			SessionID: ref.ID,
			Workspace: strings.TrimSpace(ref.CWD),
			Lines:     stats.Lines,
			SizeBytes: ref.SizeBytes,
			Model:     ref.Model,
		}
		if ref.ModTimeUnixMS > 0 {
			status.UpdatedAt = time.UnixMilli(ref.ModTimeUnixMS).UTC().Format(time.RFC3339)
		}
		if cursor := state.Sessions[ref.ID]; cursor != nil {
			status.CollectedLine = cursor.LastLine
			status.Collected = cursor.SourcePath == ref.Path &&
				cursor.LastLine >= stats.Lines &&
				cursor.ModTimeUnixMS == ref.ModTimeUnixMS &&
				cursor.SizeBytes == ref.SizeBytes &&
				cursor.SettingsUnixMS == ref.SettingsUnixMS
		}
		report.Sessions = append(report.Sessions, status)
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	out := cmd.OutOrStdout()
	if !report.Present {
		fmt.Fprintf(out, "Factory sessions: none (%s does not exist)\n", report.SessionsDir)
		return nil
	}
	fmt.Fprintf(out, "Factory sessions: %d in %s\n", len(report.Sessions), report.SessionsDir)
	for _, session := range report.Sessions {
		state := "pending"
		if session.Collected {
			state = "collected"
		}
		fmt.Fprintf(out, "  %s  %s  lines %d/%d  %s\n",
			session.SessionID, state, session.CollectedLine, session.Lines, session.Workspace)
	}
	return nil
}

func resolveFactoryStatePath(override string, userMode bool) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if !userMode {
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "factory-state.json")
		}
	}
	return factorysession.DefaultStatePath()
}
