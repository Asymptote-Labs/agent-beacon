package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/codexsession"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/spf13/cobra"
)

var endpointCodexCmd = &cobra.Command{
	Use:   "codex",
	Short: "Collect telemetry from Codex session files",
	Long: `Collect endpoint telemetry from Codex CLI's local rollout session files.

Beacon's normal Codex integration uses local OpenTelemetry logs and traces for live
capture. This command reads Codex's committed JSONL session files under ~/.codex/sessions
so historical sessions and sessions that ran without Beacon OTLP can still be represented
in the local endpoint log. Every emitted event is marked harness.collection_method=poll:
Beacon sees what Codex already wrote, so nothing here can hold or deny a tool call.`,
}

var endpointCodexSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Codex session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointCodexSync,
}

var endpointCodexStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Codex sessions on this machine and how much Beacon collected",
	SilenceUsage: true,
	RunE:         runEndpointCodexStatus,
}

var endpointCodexOpts struct {
	codexDir  string
	statePath string
	logPath   string
	print     bool
	watch     bool
	interval  time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointCodexCmd)
	endpointCodexCmd.AddCommand(endpointCodexSyncCmd)
	endpointCodexCmd.AddCommand(endpointCodexStatusCmd)

	for _, c := range []*cobra.Command{endpointCodexSyncCmd, endpointCodexStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointCodexOpts.codexDir, "codex-dir", "", "Codex directory (default ~/.codex)")
		f.StringVar(&endpointCodexOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/codex.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointCodexSyncCmd.Flags()
	sync.StringVar(&endpointCodexOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointCodexOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointCodexOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointCodexOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointCodexSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := codexsession.CollectOptions{
		CodexDir: endpointCodexOpts.codexDir,
		Print:    endpointCodexOpts.print,
		Out:      cmd.OutOrStdout(),
		Write:    !endpointCodexOpts.print,
		UserMode: userMode,
	}
	if !endpointCodexOpts.print {
		opts.StatePath = resolveCodexStatePath(endpointCodexOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointCodexOpts.logPath).EffectiveLogPath
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointCodexOpts.watch && !endpointCodexOpts.print {
		return watchCodex(ctx, cmd, opts)
	}

	summary, err := codexsession.CollectOnce(opts)
	reportCodexSweep(cmd, summary)
	return err
}

func watchCodex(ctx context.Context, cmd *cobra.Command, opts codexsession.CollectOptions) error {
	interval := endpointCodexOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := codexsession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "codex sync error: %v\n", err)
		}
		reportCodexSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportCodexSweep(cmd *cobra.Command, summary codexsession.Summary) {
	if endpointCodexOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "codex sync: %d sessions, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	if summary.MalformedLines > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d unreadable line(s) in Codex session logs\n", summary.MalformedLines)
	}
	if summary.PartialSessions > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d session(s) had a partly written record; it will be read on the next sweep\n", summary.PartialSessions)
	}
}

type codexSessionStatus struct {
	SessionID     string `json:"session_id"`
	Path          string `json:"path"`
	Workspace     string `json:"workspace,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	CollectedLine int    `json:"collected_line"`
	Collected     bool   `json:"collected"`
	SizeBytes     int64  `json:"size_bytes"`
	Title         string `json:"title,omitempty"`
}

type codexStatusReport struct {
	CodexDir    string               `json:"codex_dir"`
	SessionsDir string               `json:"sessions_dir"`
	Present     bool                 `json:"present"`
	StatePath   string               `json:"state_path,omitempty"`
	Sessions    []codexSessionStatus `json:"sessions"`
}

func runEndpointCodexStatus(cmd *cobra.Command, args []string) error {
	store, err := codexsession.NewStore(endpointCodexOpts.codexDir)
	if err != nil {
		return err
	}
	statePath := resolveCodexStatePath(endpointCodexOpts.statePath, endpointUserMode())
	state, err := codexsession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}
	report := codexStatusReport{
		CodexDir:    store.CodexDir,
		SessionsDir: store.SessionsDir,
		Present:     store.Exists(),
		StatePath:   statePath,
		Sessions:    make([]codexSessionStatus, 0, len(refs)),
	}
	for _, ref := range refs {
		status := codexSessionStatus{
			SessionID: ref.ID,
			Path:      ref.Path,
			Workspace: ref.Workspace,
			SizeBytes: ref.SizeBytes,
		}
		if ref.ModTimeUnixMS > 0 {
			status.UpdatedAt = time.UnixMilli(ref.ModTimeUnixMS).UTC().Format(time.RFC3339)
		}
		if ref.Index != nil {
			status.Title = ref.Index.ThreadName
		}
		if cursor := state.Files[ref.Path]; cursor != nil {
			status.CollectedLine = cursor.LastLine
			status.Collected = cursor.SizeBytes == ref.SizeBytes && cursor.ModTimeUnixMS == ref.ModTimeUnixMS
		}
		report.Sessions = append(report.Sessions, status)
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Codex sessions: %s\n", report.SessionsDir)
	if !report.Present {
		fmt.Fprintln(cmd.OutOrStdout(), "No Codex session directory found.")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Collector state: %s\n", report.StatePath)
	fmt.Fprintf(cmd.OutOrStdout(), "Sessions: %d\n", len(report.Sessions))
	for _, session := range report.Sessions {
		marker := "pending"
		if session.Collected {
			marker = "collected"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %s line=%d size=%d workspace=%s\n",
			marker, session.SessionID, session.CollectedLine, session.SizeBytes, session.Workspace)
	}
	return nil
}

func resolveCodexStatePath(override string, userMode bool) string {
	if override != "" {
		return override
	}
	if userMode {
		return codexsession.DefaultStatePath()
	}
	return filepath.Join(endpointconfig.BaseDir(false), "state", "codex.json")
}
