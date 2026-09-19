package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/openclawsession"
	"github.com/spf13/cobra"
)

var endpointOpenClawSessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Collect telemetry from OpenClaw committed session files",
	Long: `Collect endpoint telemetry from OpenClaw Gateway's local session store.

Beacon's normal OpenClaw support uses a managed plugin for live agent activity
and OpenClaw's diagnostics-otel plugin for gateway traces. This command reads
OpenClaw's committed JSONL session files under ~/.openclaw/agents/*/sessions
so historical sessions and sessions that ran without Beacon's plugin can still
be represented in the local endpoint log.

Every emitted event is marked harness.collection_method=poll: Beacon sees what
OpenClaw already wrote, so nothing here can hold or deny a tool call.`,
}

var endpointOpenClawSessionsSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new OpenClaw session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointOpenClawSessionsSync,
}

var endpointOpenClawSessionsStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show OpenClaw session files and collection progress",
	SilenceUsage: true,
	RunE:         runEndpointOpenClawSessionsStatus,
}

var endpointOpenClawSessionsOpts struct {
	openClawDir string
	statePath   string
	logPath     string
	print       bool
	watch       bool
	interval    time.Duration
}

func init() {
	endpointOpenClawCmd.AddCommand(endpointOpenClawSessionsCmd)
	endpointOpenClawSessionsCmd.AddCommand(endpointOpenClawSessionsSyncCmd)
	endpointOpenClawSessionsCmd.AddCommand(endpointOpenClawSessionsStatusCmd)

	for _, c := range []*cobra.Command{endpointOpenClawSessionsSyncCmd, endpointOpenClawSessionsStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointOpenClawSessionsOpts.openClawDir, "openclaw-dir", "", "OpenClaw state directory (default $OPENCLAW_STATE_DIR or ~/.openclaw)")
		f.StringVar(&endpointOpenClawSessionsOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/openclaw-sessions.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointOpenClawSessionsSyncCmd.Flags()
	sync.StringVar(&endpointOpenClawSessionsOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointOpenClawSessionsOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointOpenClawSessionsOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointOpenClawSessionsOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointOpenClawSessionsSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := openclawsession.CollectOptions{
		OpenClawDir: endpointOpenClawSessionsOpts.openClawDir,
		Print:       endpointOpenClawSessionsOpts.print,
		Out:         cmd.OutOrStdout(),
		Write:       !endpointOpenClawSessionsOpts.print,
		UserMode:    userMode,
	}
	if !endpointOpenClawSessionsOpts.print {
		opts.StatePath = resolveOpenClawSessionsStatePath(endpointOpenClawSessionsOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointOpenClawSessionsOpts.logPath).EffectiveLogPath
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointOpenClawSessionsOpts.watch && !endpointOpenClawSessionsOpts.print {
		return watchOpenClawSessions(ctx, cmd, opts)
	}
	summary, err := openclawsession.CollectOnce(opts)
	reportOpenClawSessionsSweep(cmd, summary)
	return err
}

func watchOpenClawSessions(ctx context.Context, cmd *cobra.Command, opts openclawsession.CollectOptions) error {
	interval := endpointOpenClawSessionsOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := openclawsession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "openclaw sessions sync error: %v\n", err)
		}
		reportOpenClawSessionsSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportOpenClawSessionsSweep(cmd *cobra.Command, summary openclawsession.Summary) {
	if endpointOpenClawSessionsOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "openclaw sessions sync: %d traces, %d changed, %d events, %d errors\n",
		summary.Traces, summary.TracesChanged, summary.EventsEmitted, summary.Errors)
}

type openClawSessionStatusRef struct {
	ID         string `json:"id"`
	Profile    string `json:"profile"`
	Workspace  string `json:"workspace,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	Collected  bool   `json:"collected"`
	EventsRead int    `json:"events_read"`
	SizeBytes  int64  `json:"size_bytes"`
	SourcePath string `json:"source_path,omitempty"`
}

type openClawSessionsStatusReport struct {
	OpenClawDir string                     `json:"openclaw_dir"`
	Present     bool                       `json:"present"`
	StatePath   string                     `json:"state_path,omitempty"`
	Traces      []openClawSessionStatusRef `json:"traces"`
}

func runEndpointOpenClawSessionsStatus(cmd *cobra.Command, args []string) error {
	store, err := openclawsession.NewStore(endpointOpenClawSessionsOpts.openClawDir)
	if err != nil {
		return err
	}
	statePath := resolveOpenClawSessionsStatePath(endpointOpenClawSessionsOpts.statePath, endpointUserMode())
	state, err := openclawsession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}
	report := openClawSessionsStatusReport{
		OpenClawDir: store.OpenClawDir,
		Present:     store.Exists(),
		StatePath:   statePath,
		Traces:      make([]openClawSessionStatusRef, 0, len(refs)),
	}
	for _, ref := range refs {
		cursor := state.Traces[ref.Profile+":"+ref.ID+":"+ref.SourcePath]
		status := openClawSessionStatusRef{
			ID:         ref.ID,
			Profile:    ref.Profile,
			Workspace:  ref.Directory,
			Collected:  cursor != nil && cursor.UpdatedAtMS >= ref.UpdatedAtUnixMS,
			SizeBytes:  ref.SizeBytes,
			SourcePath: ref.SourcePath,
		}
		if ref.UpdatedAtUnixMS > 0 {
			status.UpdatedAt = time.UnixMilli(ref.UpdatedAtUnixMS).UTC().Format(time.RFC3339)
		}
		if cursor != nil {
			status.EventsRead = cursor.LastOrder
		}
		report.Traces = append(report.Traces, status)
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	if !report.Present {
		fmt.Fprintf(cmd.OutOrStdout(), "OpenClaw session store not found at %s\n", report.OpenClawDir)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "OpenClaw session store: %s\n", report.OpenClawDir)
	fmt.Fprintf(cmd.OutOrStdout(), "State: %s\n", report.StatePath)
	for _, trace := range report.Traces {
		collected := "pending"
		if trace.Collected {
			collected = "collected"
		}
		updated := trace.UpdatedAt
		if updated == "" {
			updated = "unknown"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s [%s] %s events=%d updated=%s", trace.ID, trace.Profile, collected, trace.EventsRead, updated)
		if trace.Workspace != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " workspace=%s", trace.Workspace)
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}
	return nil
}

func resolveOpenClawSessionsStatePath(path string, userMode bool) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	base := filepath.Join("/var", "lib", "beacon", "endpoint", "state")
	if userMode {
		base = filepath.Dir(openclawsession.DefaultStatePath())
	}
	return filepath.Join(base, "openclaw-sessions.json")
}
