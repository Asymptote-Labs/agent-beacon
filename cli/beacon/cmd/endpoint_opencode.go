package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/opencodesession"
	"github.com/spf13/cobra"
)

var endpointOpenCodeCmd = &cobra.Command{
	Use:   "opencode",
	Short: "Collect telemetry from OpenCode local sessions",
	Long: `Collect endpoint telemetry from OpenCode's local session store.

Beacon's normal OpenCode integration uses a managed plugin for live capture.
This command reads OpenCode's committed local opencode.db or legacy storage tree
so historical sessions and sessions that ran without Beacon's plugin can still be
represented in the local endpoint log. Every emitted event is marked
harness.collection_method=poll: Beacon sees what OpenCode already wrote, so
nothing here can hold or deny a tool call.`,
}

var endpointOpenCodeSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new OpenCode session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointOpenCodeSync,
}

var endpointOpenCodeStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show OpenCode local sessions and collection progress",
	SilenceUsage: true,
	RunE:         runEndpointOpenCodeStatus,
}

var endpointOpenCodeOpts struct {
	dataDir   string
	statePath string
	logPath   string
	print     bool
	watch     bool
	interval  time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointOpenCodeCmd)
	endpointOpenCodeCmd.AddCommand(endpointOpenCodeSyncCmd)
	endpointOpenCodeCmd.AddCommand(endpointOpenCodeStatusCmd)

	for _, c := range []*cobra.Command{endpointOpenCodeSyncCmd, endpointOpenCodeStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointOpenCodeOpts.dataDir, "data-dir", "", "OpenCode data directory or opencode.db path (default ~/.config/opencode)")
		f.StringVar(&endpointOpenCodeOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/opencode-sessions.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointOpenCodeSyncCmd.Flags()
	sync.StringVar(&endpointOpenCodeOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointOpenCodeOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointOpenCodeOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointOpenCodeOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointOpenCodeSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := opencodesession.CollectOptions{
		DataDir:  endpointOpenCodeOpts.dataDir,
		Print:    endpointOpenCodeOpts.print,
		Out:      cmd.OutOrStdout(),
		Write:    !endpointOpenCodeOpts.print,
		UserMode: userMode,
	}
	if !endpointOpenCodeOpts.print {
		opts.StatePath = resolveOpenCodeStatePath(endpointOpenCodeOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointOpenCodeOpts.logPath).EffectiveLogPath
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointOpenCodeOpts.watch && !endpointOpenCodeOpts.print {
		return watchOpenCode(ctx, cmd, opts)
	}
	summary, err := opencodesession.CollectOnce(opts)
	reportOpenCodeSweep(cmd, summary)
	return err
}

func watchOpenCode(ctx context.Context, cmd *cobra.Command, opts opencodesession.CollectOptions) error {
	interval := endpointOpenCodeOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := opencodesession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "opencode sync error: %v\n", err)
		}
		reportOpenCodeSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportOpenCodeSweep(cmd *cobra.Command, summary opencodesession.Summary) {
	if endpointOpenCodeOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "opencode sync: %d traces, %d changed, %d events, %d errors\n",
		summary.Traces, summary.TracesChanged, summary.EventsEmitted, summary.Errors)
}

type openCodeStatusRef struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Workspace  string `json:"workspace,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	Collected  bool   `json:"collected"`
	EventsRead int    `json:"events_read"`
	SizeBytes  int64  `json:"size_bytes"`
	SourcePath string `json:"source_path,omitempty"`
}

type openCodeStatusReport struct {
	DataDir    string              `json:"data_dir"`
	SQLitePath string              `json:"sqlite_path"`
	LegacyDir  string              `json:"legacy_storage_dir"`
	Present    bool                `json:"present"`
	StatePath  string              `json:"state_path,omitempty"`
	Traces     []openCodeStatusRef `json:"traces"`
}

func runEndpointOpenCodeStatus(cmd *cobra.Command, args []string) error {
	store, err := opencodesession.NewStore(endpointOpenCodeOpts.dataDir)
	if err != nil {
		return err
	}
	statePath := resolveOpenCodeStatePath(endpointOpenCodeOpts.statePath, endpointUserMode())
	state, err := opencodesession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, listErr := store.List()
	if listErr != nil && len(refs) == 0 {
		return listErr
	}
	report := openCodeStatusReport{
		DataDir:    store.DataDir,
		SQLitePath: store.SQLitePath,
		LegacyDir:  store.LegacyStorageDir,
		Present:    store.Exists(),
		StatePath:  statePath,
		Traces:     make([]openCodeStatusRef, 0, len(refs)),
	}
	for _, ref := range refs {
		cursor := state.Traces[string(ref.Kind)+":"+ref.ID]
		status := openCodeStatusRef{
			ID:         ref.ID,
			Source:     string(ref.Kind),
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
		fmt.Fprintf(cmd.OutOrStdout(), "OpenCode session store not found at %s\n", report.DataDir)
		return listErr
	}
	fmt.Fprintf(cmd.OutOrStdout(), "OpenCode session store: %s\n", report.DataDir)
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
	return listErr
}

func resolveOpenCodeStatePath(path string, userMode bool) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	if !userMode {
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "opencode-sessions.json")
		}
	}
	return opencodesession.DefaultStatePath()
}
