package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/copilotsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/spf13/cobra"
)

var endpointCopilotCmd = &cobra.Command{
	Use:   "copilot",
	Short: "Collect telemetry from GitHub Copilot CLI sessions",
	Long: `Collect endpoint telemetry from GitHub Copilot CLI's native session store.

Copilot CLI persists local session records under ~/.copilot/session-state. Beacon
reads those records after the fact and maps them to endpoint events with
harness.collection_method=poll. This complements the live OTLP integration:
OTLP still provides live approvals and spans, while session sync backfills
assistant text, complete tool results, commands, file activity, and token usage
that the live path may not expose.`,
}

var endpointCopilotSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new GitHub Copilot CLI session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointCopilotSync,
}

var endpointCopilotStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show GitHub Copilot CLI sessions and collection progress",
	SilenceUsage: true,
	RunE:         runEndpointCopilotStatus,
}

var endpointCopilotOpts struct {
	copilotDir string
	statePath  string
	logPath    string
	print      bool
	watch      bool
	interval   time.Duration
	workspace  string
	sessionID  string
}

func init() {
	endpointCmd.AddCommand(endpointCopilotCmd)
	endpointCopilotCmd.AddCommand(endpointCopilotSyncCmd)
	endpointCopilotCmd.AddCommand(endpointCopilotStatusCmd)

	for _, c := range []*cobra.Command{endpointCopilotSyncCmd, endpointCopilotStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointCopilotOpts.copilotDir, "copilot-dir", "", "GitHub Copilot CLI directory (default ~/.copilot)")
		f.StringVar(&endpointCopilotOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/copilot.json)")
		f.StringVar(&endpointCopilotOpts.workspace, "workspace", "", "Only include sessions whose cwd matches this workspace")
		f.StringVar(&endpointCopilotOpts.sessionID, "session-id", "", "Only include one GitHub Copilot CLI session id")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointCopilotSyncCmd.Flags()
	sync.StringVar(&endpointCopilotOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointCopilotOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor")
	sync.BoolVar(&endpointCopilotOpts.watch, "watch", false, "Sweep continuously on --interval")
	sync.DurationVar(&endpointCopilotOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointCopilotSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := copilotsession.CollectOptions{
		CopilotDir: endpointCopilotOpts.copilotDir,
		Print:      endpointCopilotOpts.print,
		Out:        cmd.OutOrStdout(),
		Write:      !endpointCopilotOpts.print,
		UserMode:   userMode,
		Workspace:  endpointCopilotOpts.workspace,
		SessionID:  endpointCopilotOpts.sessionID,
	}
	if !endpointCopilotOpts.print {
		opts.StatePath = resolveCopilotStatePath(endpointCopilotOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointCopilotOpts.logPath).EffectiveLogPath
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointCopilotOpts.watch && !endpointCopilotOpts.print {
		return watchCopilot(ctx, cmd, opts)
	}
	summary, err := copilotsession.CollectOnce(opts)
	reportCopilotSweep(cmd, summary)
	return err
}

func watchCopilot(ctx context.Context, cmd *cobra.Command, opts copilotsession.CollectOptions) error {
	interval := endpointCopilotOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := copilotsession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "copilot sync error: %v\n", err)
		}
		reportCopilotSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportCopilotSweep(cmd *cobra.Command, summary copilotsession.Summary) {
	if endpointCopilotOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "copilot sync: %d sessions, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	if summary.MalformedLines > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d unreadable line(s) in GitHub Copilot CLI session logs\n", summary.MalformedLines)
	}
	if summary.PartialSessions > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d session(s) had a partly written record; it will be read on the next sweep\n", summary.PartialSessions)
	}
}

type copilotSessionStatus struct {
	SessionID     string `json:"session_id"`
	Workspace     string `json:"workspace,omitempty"`
	Repository    string `json:"repository,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Title         string `json:"title,omitempty"`
	Path          string `json:"path"`
	CollectedLine int    `json:"collected_line"`
	LastLine      int    `json:"last_line"`
	Collected     bool   `json:"collected"`
	SizeBytes     int64  `json:"size_bytes"`
}

type copilotStatusReport struct {
	CopilotDir      string                 `json:"copilot_dir"`
	SessionStateDir string                 `json:"session_state_dir"`
	Present         bool                   `json:"present"`
	StatePath       string                 `json:"state_path,omitempty"`
	Sessions        []copilotSessionStatus `json:"sessions"`
}

func runEndpointCopilotStatus(cmd *cobra.Command, args []string) error {
	store, err := copilotsession.NewStore(endpointCopilotOpts.copilotDir)
	if err != nil {
		return err
	}
	statePath := resolveCopilotStatePath(endpointCopilotOpts.statePath, endpointUserMode())
	state, err := copilotsession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}
	refs = copilotsession.FilterRefs(refs, copilotsession.CollectOptions{Workspace: endpointCopilotOpts.workspace, SessionID: endpointCopilotOpts.sessionID})
	report := copilotStatusReport{
		CopilotDir:      store.CopilotDir,
		SessionStateDir: store.SessionStateDir,
		Present:         store.Exists(),
		StatePath:       statePath,
		Sessions:        make([]copilotSessionStatus, 0, len(refs)),
	}
	for _, ref := range refs {
		records, _, _ := store.Read(ref)
		status := copilotSessionStatus{
			SessionID: ref.ID,
			Path:      ref.Path,
			LastLine:  lastCopilotLine(records),
			SizeBytes: ref.SizeBytes,
		}
		if ref.Meta != nil {
			status.Workspace = copilotFirstNonEmpty(ref.Meta.CWD, ref.Meta.GitRoot)
			status.Repository = ref.Meta.Repository
			status.Branch = ref.Meta.Branch
			status.Title = ref.Meta.Name
		}
		if cursor := state.Files[ref.Path]; cursor != nil {
			status.CollectedLine = cursor.LastLine
			status.Collected = cursor.LastLine >= status.LastLine && cursor.SizeBytes == ref.SizeBytes && !cursor.PartialTail
		}
		report.Sessions = append(report.Sessions, status)
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	out := cmd.OutOrStdout()
	if !report.Present {
		fmt.Fprintf(out, "GitHub Copilot CLI sessions: none (%s does not exist)\n", report.SessionStateDir)
		return nil
	}
	fmt.Fprintf(out, "GitHub Copilot CLI sessions: %d in %s\n", len(report.Sessions), report.SessionStateDir)
	for _, session := range report.Sessions {
		state := "pending"
		if session.Collected {
			state = "collected"
		}
		fmt.Fprintf(out, "  %s  %s  line %d/%d  %s\n",
			session.SessionID, state, session.CollectedLine, session.LastLine, strings.TrimSpace(session.Workspace))
	}
	return nil
}

func lastCopilotLine(records []copilotsession.Record) int {
	if len(records) == 0 {
		return 0
	}
	return records[len(records)-1].Line
}

func resolveCopilotStatePath(override string, userMode bool) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if !userMode {
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "copilot-state.json")
		}
	}
	return copilotsession.DefaultStatePath()
}

func copilotFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
