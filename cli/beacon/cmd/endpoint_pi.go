package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pisession"
	"github.com/spf13/cobra"
)

var endpointPiCmd = &cobra.Command{
	Use:   "pi",
	Short: "Collect telemetry from Pi session files",
	Long: `Collect endpoint telemetry from Pi's local session store.

Pi has a live Beacon extension for plugin-time telemetry. This command is the
complementary poll path: it reads committed JSONL transcripts under
~/.pi/agent/sessions and converts records that were not already synced into
endpoint events. Every event is marked harness.collection_method=poll, because
Beacon is reading what Pi wrote after the fact rather than observing the agent
as it works.`,
}

var endpointPiSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Pi session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointPiSync,
}

var endpointPiStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Pi sessions and how much Beacon has collected",
	SilenceUsage: true,
	RunE:         runEndpointPiStatus,
}

var endpointPiOpts struct {
	sessionsDir string
	statePath   string
	logPath     string
	print       bool
	watch       bool
	interval    time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointPiCmd)
	endpointPiCmd.AddCommand(endpointPiSyncCmd)
	endpointPiCmd.AddCommand(endpointPiStatusCmd)

	for _, c := range []*cobra.Command{endpointPiSyncCmd, endpointPiStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointPiOpts.sessionsDir, "sessions-dir", "", "Pi sessions directory (default ~/.pi/agent/sessions)")
		f.StringVar(&endpointPiOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/pi.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointPiSyncCmd.Flags()
	sync.StringVar(&endpointPiOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointPiOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointPiOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointPiOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointPiSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := pisession.CollectOptions{
		SessionsDir: endpointPiOpts.sessionsDir,
		Print:       endpointPiOpts.print,
		Out:         cmd.OutOrStdout(),
		Write:       !endpointPiOpts.print,
		UserMode:    userMode,
	}
	if !endpointPiOpts.print {
		opts.StatePath = resolvePiStatePath(endpointPiOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointPiOpts.logPath).EffectiveLogPath
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointPiOpts.watch && !endpointPiOpts.print {
		return watchPi(ctx, cmd, opts)
	}
	summary, err := pisession.CollectOnce(opts)
	reportPiSweep(cmd, summary)
	return err
}

func watchPi(ctx context.Context, cmd *cobra.Command, opts pisession.CollectOptions) error {
	interval := endpointPiOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := pisession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Pi sync error: %v\n", err)
		}
		reportPiSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportPiSweep(cmd *cobra.Command, summary pisession.Summary) {
	if endpointPiOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Pi sync: %d sessions, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	if summary.MalformedLines > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d unreadable line(s) in Pi session logs\n", summary.MalformedLines)
	}
}

type piSessionStatus struct {
	SessionID     string `json:"session_id"`
	SourcePath    string `json:"source_path"`
	Workspace     string `json:"workspace,omitempty"`
	CollectedLine int    `json:"collected_line"`
	Collected     bool   `json:"collected"`
	SizeBytes     int64  `json:"size_bytes"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

type piStatusReport struct {
	SessionsDir string            `json:"sessions_dir"`
	Present     bool              `json:"present"`
	StatePath   string            `json:"state_path,omitempty"`
	Sessions    []piSessionStatus `json:"sessions"`
}

func runEndpointPiStatus(cmd *cobra.Command, args []string) error {
	store, err := pisession.NewStore(endpointPiOpts.sessionsDir)
	if err != nil {
		return err
	}
	statePath := resolvePiStatePath(endpointPiOpts.statePath, endpointUserMode())
	state, err := pisession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}
	report := piStatusReport{
		SessionsDir: store.SessionsDir,
		Present:     store.Exists(),
		StatePath:   statePath,
		Sessions:    make([]piSessionStatus, 0, len(refs)),
	}
	for _, ref := range refs {
		cursor := state.Files[piStatusKey(ref.Path)]
		status := piSessionStatus{
			SessionID:  ref.ID,
			SourcePath: ref.Path,
			Workspace:  stringFromMap(ref.Header, "cwd"),
			SizeBytes:  ref.SizeBytes,
		}
		if ref.ModTimeMS > 0 {
			status.UpdatedAt = time.UnixMilli(ref.ModTimeMS).UTC().Format(time.RFC3339)
		}
		if cursor != nil {
			status.CollectedLine = cursor.LastLine
			status.Collected = cursor.ModTimeMS == ref.ModTimeMS && cursor.SizeBytes == ref.SizeBytes
		}
		report.Sessions = append(report.Sessions, status)
	}

	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	out := cmd.OutOrStdout()
	if !report.Present {
		fmt.Fprintf(out, "Pi sessions: none (%s does not exist)\n", report.SessionsDir)
		return nil
	}
	fmt.Fprintf(out, "Pi sessions: %d in %s\n", len(report.Sessions), report.SessionsDir)
	for _, session := range report.Sessions {
		state := "pending"
		if session.Collected {
			state = "collected"
		}
		fmt.Fprintf(out, "  %s  %s  line %d  %s\n",
			session.SessionID, state, session.CollectedLine, strings.TrimSpace(session.Workspace))
	}
	return nil
}

func resolvePiStatePath(override string, userMode bool) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if !userMode {
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "pi-state.json")
		}
	}
	return pisession.DefaultStatePath()
}

func piStatusKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
