package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/claudesession"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/spf13/cobra"
)

var endpointClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Collect telemetry from Claude Code session files",
	Long: `Collect endpoint telemetry from Claude Code's local session transcripts.

Beacon's normal Claude Code integration uses hooks and OpenTelemetry for live capture.
This command reads Claude's committed JSONL session files under ~/.claude/projects so
historical sessions and sessions that ran without Beacon hooks can still be represented
in the local endpoint log. Every emitted event is marked harness.collection_method=poll:
Beacon sees what Claude already wrote, so nothing here can hold or deny a tool call.`,
}

var endpointClaudeSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Claude Code session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointClaudeSync,
}

var endpointClaudeStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Claude Code sessions on this machine and how much Beacon collected",
	SilenceUsage: true,
	RunE:         runEndpointClaudeStatus,
}

var endpointClaudeOpts struct {
	projectsDir string
	statePath   string
	logPath     string
	print       bool
	watch       bool
	interval    time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointClaudeCmd)
	endpointClaudeCmd.AddCommand(endpointClaudeSyncCmd)
	endpointClaudeCmd.AddCommand(endpointClaudeStatusCmd)

	for _, c := range []*cobra.Command{endpointClaudeSyncCmd, endpointClaudeStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointClaudeOpts.projectsDir, "projects-dir", "", "Claude projects directory (default ~/.claude/projects)")
		f.StringVar(&endpointClaudeOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/claude.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointClaudeSyncCmd.Flags()
	sync.StringVar(&endpointClaudeOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointClaudeOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointClaudeOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointClaudeOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointClaudeSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := claudesession.CollectOptions{
		ProjectsDir: endpointClaudeOpts.projectsDir,
		Print:       endpointClaudeOpts.print,
		Out:         cmd.OutOrStdout(),
		Write:       !endpointClaudeOpts.print,
		UserMode:    userMode,
	}
	if !endpointClaudeOpts.print {
		opts.StatePath = resolveClaudeStatePath(endpointClaudeOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointClaudeOpts.logPath).EffectiveLogPath
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointClaudeOpts.watch && !endpointClaudeOpts.print {
		return watchClaude(ctx, cmd, opts)
	}

	summary, err := claudesession.CollectOnce(opts)
	reportClaudeSweep(cmd, summary)
	return err
}

func watchClaude(ctx context.Context, cmd *cobra.Command, opts claudesession.CollectOptions) error {
	interval := endpointClaudeOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := claudesession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "claude sync error: %v\n", err)
		}
		reportClaudeSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportClaudeSweep(cmd *cobra.Command, summary claudesession.Summary) {
	if endpointClaudeOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "claude sync: %d sessions, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	if summary.MalformedLines > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d unreadable line(s) in Claude session logs\n", summary.MalformedLines)
	}
	if summary.PartialSessions > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d session(s) had a partly written record; it will be read on the next sweep\n", summary.PartialSessions)
	}
}

type claudeSessionStatus struct {
	SessionID     string `json:"session_id"`
	Path          string `json:"path"`
	Workspace     string `json:"workspace,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	CollectedLine int    `json:"collected_line"`
	Collected     bool   `json:"collected"`
	SizeBytes     int64  `json:"size_bytes"`
	IsSidechain   bool   `json:"is_sidechain,omitempty"`
	ParentSession string `json:"parent_session_id,omitempty"`
}

type claudeStatusReport struct {
	ProjectsDir string                `json:"projects_dir"`
	Present     bool                  `json:"present"`
	StatePath   string                `json:"state_path,omitempty"`
	Sessions    []claudeSessionStatus `json:"sessions"`
}

func runEndpointClaudeStatus(cmd *cobra.Command, args []string) error {
	store, err := claudesession.NewStore(endpointClaudeOpts.projectsDir)
	if err != nil {
		return err
	}
	statePath := resolveClaudeStatePath(endpointClaudeOpts.statePath, endpointUserMode())
	state, err := claudesession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}
	report := claudeStatusReport{
		ProjectsDir: store.ProjectsDir,
		Present:     store.Exists(),
		StatePath:   statePath,
		Sessions:    make([]claudeSessionStatus, 0, len(refs)),
	}
	for _, ref := range refs {
		status := claudeSessionStatus{
			SessionID:     ref.ID,
			Path:          ref.Path,
			Workspace:     ref.ProjectPath,
			SizeBytes:     ref.SizeBytes,
			IsSidechain:   ref.IsSidechain,
			ParentSession: ref.ParentSessionID,
		}
		if ref.ModTimeUnixMS > 0 {
			status.UpdatedAt = time.UnixMilli(ref.ModTimeUnixMS).UTC().Format(time.RFC3339)
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
	fmt.Fprintf(cmd.OutOrStdout(), "Claude projects: %s\n", report.ProjectsDir)
	if !report.Present {
		fmt.Fprintln(cmd.OutOrStdout(), "No Claude Code session directory found.")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Collector state: %s\n", report.StatePath)
	fmt.Fprintf(cmd.OutOrStdout(), "Sessions: %d\n", len(report.Sessions))
	for _, session := range report.Sessions {
		marker := "pending"
		if session.Collected {
			marker = "collected"
		}
		sidechain := ""
		if session.IsSidechain {
			sidechain = " sidechain"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %s%s line=%d size=%d workspace=%s\n",
			marker, session.SessionID, sidechain, session.CollectedLine, session.SizeBytes, session.Workspace)
	}
	return nil
}

func resolveClaudeStatePath(override string, userMode bool) string {
	if override != "" {
		return override
	}
	if userMode {
		return claudesession.DefaultStatePath()
	}
	return filepath.Join(endpointconfig.BaseDir(false), "state", "claude.json")
}
