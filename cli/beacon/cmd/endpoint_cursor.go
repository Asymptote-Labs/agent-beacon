package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/cursorsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/spf13/cobra"
)

// `beacon endpoint cursor` collects telemetry from Cursor's own durable local
// session stores. This supplements Cursor hooks: it can recover sessions that
// happened before Beacon hooks were installed, but it is poll-based and cannot
// hold or deny tool calls.
var endpointCursorCmd = &cobra.Command{
	Use:   "cursor",
	Short: "Collect telemetry from Cursor local sessions",
	Long: `Collect endpoint telemetry from Cursor's local session stores.

Cursor keeps Composer conversations in its global VS Code storage database and
agent transcripts under ~/.cursor/projects. Beacon reads those committed local
records and converts them into endpoint events marked harness.collection_method=poll.

Reading is local and offline. Run 'sync' on a schedule, or with --watch, to keep
the runtime log current.`,
}

var endpointCursorSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Cursor session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointCursorSync,
}

var endpointCursorStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Cursor local session stores and collection progress",
	SilenceUsage: true,
	RunE:         runEndpointCursorStatus,
}

var endpointCursorOpts struct {
	globalDBPath string
	projectsDir  string
	statePath    string
	logPath      string
	print        bool
	watch        bool
	interval     time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointCursorCmd)
	endpointCursorCmd.AddCommand(endpointCursorSyncCmd)
	endpointCursorCmd.AddCommand(endpointCursorStatusCmd)

	for _, c := range []*cobra.Command{endpointCursorSyncCmd, endpointCursorStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointCursorOpts.globalDBPath, "global-db", "", "Cursor global storage DB path (default OS-specific Cursor state.vscdb)")
		f.StringVar(&endpointCursorOpts.projectsDir, "projects-dir", "", "Cursor projects directory (default ~/.cursor/projects)")
		f.StringVar(&endpointCursorOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/cursor-sessions.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointCursorSyncCmd.Flags()
	sync.StringVar(&endpointCursorOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointCursorOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointCursorOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointCursorOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointCursorSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := cursorsession.CollectOptions{
		GlobalDBPath: endpointCursorOpts.globalDBPath,
		ProjectsDir:  endpointCursorOpts.projectsDir,
		Print:        endpointCursorOpts.print,
		Out:          cmd.OutOrStdout(),
		Write:        !endpointCursorOpts.print,
		UserMode:     userMode,
	}
	if !endpointCursorOpts.print {
		opts.StatePath = resolveCursorStatePath(endpointCursorOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointCursorOpts.logPath).EffectiveLogPath
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointCursorOpts.watch && !endpointCursorOpts.print {
		return watchCursor(ctx, cmd, opts)
	}
	summary, err := cursorsession.CollectOnce(opts)
	reportCursorSweep(cmd, summary)
	return err
}

func watchCursor(ctx context.Context, cmd *cobra.Command, opts cursorsession.CollectOptions) error {
	interval := endpointCursorOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := cursorsession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "cursor sync error: %v\n", err)
		}
		reportCursorSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportCursorSweep(cmd *cobra.Command, summary cursorsession.Summary) {
	if endpointCursorOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "cursor sync: %d traces, %d changed, %d events, %d errors\n",
		summary.Traces, summary.TracesChanged, summary.EventsEmitted, summary.Errors)
}

type cursorStatusRef struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Workspace  string `json:"workspace,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	Collected  bool   `json:"collected"`
	EventsRead int    `json:"events_read"`
	SizeBytes  int64  `json:"size_bytes"`
	SourcePath string `json:"source_path,omitempty"`
}

type cursorStatusReport struct {
	GlobalDBPath string            `json:"global_db_path"`
	ProjectsDir  string            `json:"projects_dir"`
	Present      bool              `json:"present"`
	StatePath    string            `json:"state_path,omitempty"`
	Traces       []cursorStatusRef `json:"traces"`
}

func runEndpointCursorStatus(cmd *cobra.Command, args []string) error {
	store, err := cursorsession.NewStore(endpointCursorOpts.globalDBPath, endpointCursorOpts.projectsDir)
	if err != nil {
		return err
	}
	statePath := resolveCursorStatePath(endpointCursorOpts.statePath, endpointUserMode())
	state, err := cursorsession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, listErr := store.List()
	if listErr != nil && len(refs) == 0 {
		return listErr
	}
	report := cursorStatusReport{
		GlobalDBPath: store.GlobalDBPath,
		ProjectsDir:  store.ProjectsDir,
		Present:      store.Exists(),
		StatePath:    statePath,
		Traces:       make([]cursorStatusRef, 0, len(refs)),
	}
	for _, ref := range refs {
		status := cursorStatusRef{
			ID:         ref.ID,
			Source:     string(ref.Kind),
			Workspace:  ref.Workspace,
			SizeBytes:  ref.SizeBytes,
			SourcePath: ref.SourcePath,
		}
		if ref.UpdatedAtUnixMS > 0 {
			status.UpdatedAt = time.UnixMilli(ref.UpdatedAtUnixMS).UTC().Format(time.RFC3339)
		}
		records, _ := store.Read(ref)
		status.EventsRead = len(records)
		if cursor := state.Traces[string(ref.Kind)+":"+ref.ID]; cursor != nil {
			status.Collected = cursor.LastOrder >= len(records) && (ref.UpdatedAtUnixMS == 0 || cursor.UpdatedAtMS >= ref.UpdatedAtUnixMS)
		}
		report.Traces = append(report.Traces, status)
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	out := cmd.OutOrStdout()
	if !report.Present {
		fmt.Fprintf(out, "cursor sessions: none (%s and %s do not exist)\n", report.GlobalDBPath, report.ProjectsDir)
		return nil
	}
	fmt.Fprintf(out, "cursor sessions: %d traces\n", len(report.Traces))
	for _, trace := range report.Traces {
		state := "pending"
		if trace.Collected {
			state = "collected"
		}
		fmt.Fprintf(out, "  %s  %s  %s  %s\n", trace.ID, trace.Source, state, strings.TrimSpace(trace.Workspace))
	}
	return nil
}

func resolveCursorStatePath(override string, userMode bool) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if !userMode {
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "cursor-sessions-state.json")
		}
	}
	return cursorsession.DefaultStatePath()
}
