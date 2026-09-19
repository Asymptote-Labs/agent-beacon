package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/clinesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/spf13/cobra"
)

var endpointClineCmd = &cobra.Command{
	Use:   "cline",
	Short: "Collect telemetry from Cline local sessions",
	Long: `Collect endpoint telemetry from Cline's local session store.

Beacon's normal Cline integration uses a managed plugin for live capture. This
command reads Cline's committed local history files under ~/.cline and the VS
Code extension task store so historical sessions and sessions that ran without
Beacon's plugin can still be represented in the local endpoint log. Every emitted
event is marked harness.collection_method=poll: Beacon sees what Cline already
wrote, so nothing here can hold or deny a tool call.`,
}

var endpointClineSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Cline session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointClineSync,
}

var endpointClineStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Cline local sessions and collection progress",
	SilenceUsage: true,
	RunE:         runEndpointClineStatus,
}

var endpointClineOpts struct {
	clineDir  string
	statePath string
	logPath   string
	print     bool
	watch     bool
	interval  time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointClineCmd)
	endpointClineCmd.AddCommand(endpointClineSyncCmd)
	endpointClineCmd.AddCommand(endpointClineStatusCmd)

	for _, c := range []*cobra.Command{endpointClineSyncCmd, endpointClineStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointClineOpts.clineDir, "cline-dir", "", "Cline directory (default ~/.cline)")
		f.StringVar(&endpointClineOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/cline-sessions.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointClineSyncCmd.Flags()
	sync.StringVar(&endpointClineOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointClineOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointClineOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointClineOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointClineSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := clinesession.CollectOptions{
		ClineDir: endpointClineOpts.clineDir,
		Print:    endpointClineOpts.print,
		Out:      cmd.OutOrStdout(),
		Write:    !endpointClineOpts.print,
		UserMode: userMode,
	}
	if !endpointClineOpts.print {
		opts.StatePath = resolveClineStatePath(endpointClineOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointClineOpts.logPath).EffectiveLogPath
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointClineOpts.watch && !endpointClineOpts.print {
		return watchCline(ctx, cmd, opts)
	}
	summary, err := clinesession.CollectOnce(opts)
	reportClineSweep(cmd, summary)
	return err
}

func watchCline(ctx context.Context, cmd *cobra.Command, opts clinesession.CollectOptions) error {
	interval := endpointClineOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := clinesession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "cline sync error: %v\n", err)
		}
		reportClineSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportClineSweep(cmd *cobra.Command, summary clinesession.Summary) {
	if endpointClineOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "cline sync: %d traces, %d changed, %d events, %d errors\n",
		summary.Traces, summary.TracesChanged, summary.EventsEmitted, summary.Errors)
}

type clineStatusRef struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Workspace  string `json:"workspace,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	Collected  bool   `json:"collected"`
	EventsRead int    `json:"events_read"`
	SizeBytes  int64  `json:"size_bytes"`
	SourcePath string `json:"source_path,omitempty"`
}

type clineStatusReport struct {
	ClineDir    string           `json:"cline_dir"`
	SessionsDir string           `json:"sessions_dir"`
	KanbanDir   string           `json:"kanban_workspaces_dir"`
	TasksDirs   []string         `json:"tasks_dirs"`
	Present     bool             `json:"present"`
	StatePath   string           `json:"state_path,omitempty"`
	Traces      []clineStatusRef `json:"traces"`
}

func runEndpointClineStatus(cmd *cobra.Command, args []string) error {
	store, err := clinesession.NewStore(endpointClineOpts.clineDir)
	if err != nil {
		return err
	}
	statePath := resolveClineStatePath(endpointClineOpts.statePath, endpointUserMode())
	state, err := clinesession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}
	report := clineStatusReport{
		ClineDir:    store.ClineDir,
		SessionsDir: store.SessionsDir,
		KanbanDir:   store.KanbanWorkspacesDir,
		TasksDirs:   store.TasksDirs,
		Present:     store.Exists(),
		StatePath:   statePath,
		Traces:      make([]clineStatusRef, 0, len(refs)),
	}
	for _, ref := range refs {
		cursor := state.Traces[ref.Kind+":"+ref.ID]
		status := clineStatusRef{
			ID:         ref.ID,
			Source:     ref.Kind,
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
		fmt.Fprintf(cmd.OutOrStdout(), "Cline session store not found at %s\n", report.ClineDir)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Cline session store: %s\n", report.ClineDir)
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
		fmt.Fprintf(cmd.OutOrStdout(), "  %s [%s] %s events=%d updated=%s", trace.ID, trace.Source, collected, trace.EventsRead, updated)
		if trace.Workspace != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " workspace=%s", trace.Workspace)
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}
	return nil
}

func resolveClineStatePath(path string, userMode bool) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	base := filepath.Join("/var", "lib", "beacon", "endpoint", "state")
	if userMode {
		base = filepath.Dir(clinesession.DefaultStatePath())
	}
	return filepath.Join(base, "cline-sessions.json")
}
