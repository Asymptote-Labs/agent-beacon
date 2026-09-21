package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/spf13/cobra"
)

var endpointDshCmd = &cobra.Command{
	Use:   "dsh",
	Short: "Collect telemetry from DeepSeek Harness sessions",
	Long: `Collect endpoint telemetry from DeepSeek Harness' native session store.

DeepSeek Harness already persists local session records under $DSH_HOME/sessions
(default ~/.dsh/sessions). Beacon reads those records after the fact and maps
them to endpoint events with harness.collection_method=poll. This complements
the live hook integration: hooks observe current tool activity and optional
policy decisions, while session sync backfills assistant text, reasoning, tool
failure status, and token usage that the hook bridge does not expose.`,
}

var endpointDshSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new DeepSeek Harness session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointDshSync,
}

var endpointDshStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show DeepSeek Harness sessions and collection progress",
	SilenceUsage: true,
	RunE:         runEndpointDshStatus,
}

var endpointDshOpts struct {
	dshHome   string
	statePath string
	logPath   string
	print     bool
	watch     bool
	interval  time.Duration
	workspace string
	sessionID string
}

func init() {
	endpointCmd.AddCommand(endpointDshCmd)
	endpointDshCmd.AddCommand(endpointDshSyncCmd)
	endpointDshCmd.AddCommand(endpointDshStatusCmd)

	for _, c := range []*cobra.Command{endpointDshSyncCmd, endpointDshStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointDshOpts.dshHome, "dsh-home", "", "DeepSeek Harness home (default $DSH_HOME or ~/.dsh)")
		f.StringVar(&endpointDshOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/dsh.json)")
		f.StringVar(&endpointDshOpts.workspace, "workspace", "", "Only include sessions whose cwd matches this workspace")
		f.StringVar(&endpointDshOpts.sessionID, "session-id", "", "Only include one DeepSeek session id")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointDshSyncCmd.Flags()
	sync.StringVar(&endpointDshOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointDshOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor")
	sync.BoolVar(&endpointDshOpts.watch, "watch", false, "Sweep continuously on --interval")
	sync.DurationVar(&endpointDshOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointDshSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := dshsession.CollectOptions{
		DSHHome:   endpointDshOpts.dshHome,
		Print:     endpointDshOpts.print,
		Out:       cmd.OutOrStdout(),
		Write:     !endpointDshOpts.print,
		UserMode:  userMode,
		Workspace: endpointDshOpts.workspace,
		SessionID: endpointDshOpts.sessionID,
	}
	if !endpointDshOpts.print {
		opts.StatePath = resolveDshStatePath(endpointDshOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointDshOpts.logPath).EffectiveLogPath
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointDshOpts.watch && !endpointDshOpts.print {
		return watchDsh(ctx, cmd, opts)
	}
	summary, err := dshsession.CollectOnce(opts)
	reportDshSweep(cmd, summary)
	return err
}

func watchDsh(ctx context.Context, cmd *cobra.Command, opts dshsession.CollectOptions) error {
	interval := endpointDshOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := dshsession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "dsh sync error: %v\n", err)
		}
		reportDshSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportDshSweep(cmd *cobra.Command, summary dshsession.Summary) {
	if endpointDshOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "dsh sync: %d sessions, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	if summary.MalformedLines > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d unreadable line(s) in DeepSeek session logs\n", summary.MalformedLines)
	}
	if summary.PartialSessions > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d session(s) had a partly written record; it will be read on the next sweep\n", summary.PartialSessions)
	}
}

type dshSessionStatus struct {
	SessionID     string `json:"session_id"`
	Workspace     string `json:"workspace,omitempty"`
	ParentSession string `json:"parent_session,omitempty"`
	Model         string `json:"model,omitempty"`
	Title         string `json:"title,omitempty"`
	Path          string `json:"path"`
	CollectedLine int    `json:"collected_line"`
	LastLine      int    `json:"last_line"`
	Collected     bool   `json:"collected"`
	SizeBytes     int64  `json:"size_bytes"`
}

type dshStatusReport struct {
	DSHHome     string             `json:"dsh_home"`
	SessionsDir string             `json:"sessions_dir"`
	Present     bool               `json:"present"`
	StatePath   string             `json:"state_path,omitempty"`
	Sessions    []dshSessionStatus `json:"sessions"`
}

func runEndpointDshStatus(cmd *cobra.Command, args []string) error {
	store, err := dshsession.NewStore(endpointDshOpts.dshHome)
	if err != nil {
		return err
	}
	statePath := resolveDshStatePath(endpointDshOpts.statePath, endpointUserMode())
	state, err := dshsession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}
	refs = filterDshStatusRefs(refs)
	report := dshStatusReport{
		DSHHome:     store.DSHHome,
		SessionsDir: store.SessionsDir,
		Present:     store.Exists(),
		StatePath:   statePath,
		Sessions:    make([]dshSessionStatus, 0, len(refs)),
	}
	for _, ref := range refs {
		records, _, _ := store.Read(ref)
		status := dshSessionStatus{
			SessionID: ref.ID,
			Path:      ref.Path,
			LastLine:  lastDshLine(records),
			SizeBytes: ref.SizeBytes,
		}
		if ref.Meta != nil {
			status.Workspace = ref.Meta.CWD
			status.ParentSession = ref.Meta.ParentSessionID
			status.Model = ref.Meta.Model
			status.Title = ref.Meta.Title
		}
		if cursor := state.Sources[ref.Path]; cursor != nil {
			status.CollectedLine = cursor.LastLine
			status.Collected = cursor.LastLine >= status.LastLine && cursor.SizeBytes == ref.SizeBytes && !cursor.PartialTail && !cursor.PartialFrame
		}
		report.Sessions = append(report.Sessions, status)
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	out := cmd.OutOrStdout()
	if !report.Present {
		fmt.Fprintf(out, "DeepSeek Harness sessions: none (%s does not exist)\n", report.SessionsDir)
		return nil
	}
	fmt.Fprintf(out, "DeepSeek Harness sessions: %d in %s\n", len(report.Sessions), report.SessionsDir)
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

func filterDshStatusRefs(refs []dshsession.SessionRef) []dshsession.SessionRef {
	opts := dshsession.CollectOptions{Workspace: endpointDshOpts.workspace, SessionID: endpointDshOpts.sessionID}
	out := refs[:0]
	sessionID := strings.TrimSpace(opts.SessionID)
	workspace := strings.TrimSpace(opts.Workspace)
	if workspace != "" {
		if abs, err := filepath.Abs(workspace); err == nil {
			workspace = filepath.Clean(abs)
		}
	}
	for _, ref := range refs {
		if sessionID != "" && ref.ID != sessionID {
			continue
		}
		if workspace != "" {
			cwd := ""
			if ref.Meta != nil {
				cwd = strings.TrimSpace(ref.Meta.CWD)
				if abs, err := filepath.Abs(cwd); err == nil {
					cwd = filepath.Clean(abs)
				}
			}
			if cwd != workspace {
				continue
			}
		}
		out = append(out, ref)
	}
	return out
}

func lastDshLine(records []dshsession.Record) int {
	if len(records) == 0 {
		return 0
	}
	return records[len(records)-1].Line
}

func resolveDshStatePath(override string, userMode bool) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if !userMode {
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "dsh-state.json")
		}
	}
	return dshsession.DefaultStatePath()
}
