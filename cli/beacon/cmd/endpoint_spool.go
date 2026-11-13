package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/workspacespool"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/spf13/cobra"
)

var endpointSpoolCmd = &cobra.Command{
	Use:   "spool",
	Short: "Drain hook events staged in workspace spools",
	Long: `Drain hook events that sandboxed agents staged in their session workspace.

A hook that cannot reach the ingest socket, the runtime log or an account spool
(for example inside a sandbox that confines writes to the session workspace)
stages its event under <workspace>/.beacon/spool/<harness>/<session>/. Beacon
drains those spools into the runtime log for sessions it has already recorded,
and for every session under the configured workspace_spool_roots.`,
}

var endpointSpoolDrainCmd = &cobra.Command{
	Use:          "drain",
	Short:        "Append staged hook events to the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointSpoolDrain,
}

var endpointSpoolStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "List workspace spools holding staged hook events",
	SilenceUsage: true,
	RunE:         runEndpointSpoolStatus,
}

var endpointSpoolOpts struct {
	workspaces []string
	logPath    string
	harness    string
	session    string
}

// endpointSpoolGeteuid is a seam so tests can exercise the root refusal without running as root.
var endpointSpoolGeteuid = os.Geteuid

func init() {
	endpointCmd.AddCommand(endpointSpoolCmd)
	endpointSpoolCmd.AddCommand(endpointSpoolDrainCmd)
	endpointSpoolCmd.AddCommand(endpointSpoolStatusCmd)

	for _, c := range []*cobra.Command{endpointSpoolDrainCmd, endpointSpoolStatusCmd} {
		f := c.Flags()
		f.StringArrayVar(&endpointSpoolOpts.workspaces, "workspace", nil, "Also include every session spool in this workspace, taken literally (repeatable)")
		f.StringVar(&endpointSpoolOpts.harness, "harness", "", "Only spools of this harness (for example opencode)")
		f.StringVar(&endpointSpoolOpts.session, "session", "", "Only spools of this session id")
		f.StringVar(&endpointSpoolOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}
}

// spoolScope is what a spool command looks at: the runtime log whose sessions it knows, the
// configured roots (globs), and the --workspace paths, which stay literal (a workspace whose name
// holds `[` or `*` is still that one workspace).
type spoolScope struct {
	logPath    string
	roots      []string
	workspaces []string
	// note says why the configured roots are missing, when the config could not be read.
	note string
}

// resolveSpoolScope reads the endpoint config for the log and the roots. A config this account
// cannot read -- a system config holding a destination secret is root-only, and root may not
// drain -- is not guessed around: the log it names may be anywhere, and a drain aimed at the
// default path would deliver to a log no collector reads. Without --log-path the scope then has no
// log (unreadable is set): status lists only --workspace spools, and drain refuses, naming the
// flag. A missing config is another matter: the writers then use the default path, and so does
// the scope.
func resolveSpoolScope(userMode bool) (spoolScope, error) {
	var scope spoolScope
	cfg, err := endpointconfig.Load(userMode)
	unreadable := false
	switch {
	case err == nil, os.IsNotExist(err):
	case errors.Is(err, os.ErrPermission):
		unreadable = true
	default:
		return scope, err
	}
	for _, ws := range endpointSpoolOpts.workspaces {
		abs, err := filepath.Abs(ws)
		if err != nil {
			return scope, fmt.Errorf("workspace %q: %w", ws, err)
		}
		scope.workspaces = append(scope.workspaces, abs)
	}
	if unreadable && endpointSpoolOpts.logPath == "" {
		scope.note = fmt.Sprintf("cannot read the endpoint config (%v), which names the runtime log; pass --log-path with that log", err)
		return scope, nil
	}
	if unreadable {
		scope.note = fmt.Sprintf("cannot read the endpoint config (%v); no configured roots are searched", err)
	}
	override := endpointSpoolOpts.logPath
	if override == "" {
		override = cfg.LogPath
	}
	scope.logPath = lifecycle.ResolveRuntimeLog(userMode, override).EffectiveLogPath
	if scope.logPath == "" {
		// No installed log named anywhere: the writers append to the default path, so the
		// sessions they recorded are there.
		scope.logPath = writer.DefaultPath(userMode)
	}
	scope.roots = cfg.WorkspaceSpoolRoots
	return scope, nil
}

// sessions is every session the scope covers, narrowed to --harness and --session. Narrowed, a
// --workspace vouches for that one session only, never for every spool a cloned repository
// planted beside it: the command a hook prints for a staged session names all three.
func (s spoolScope) sessions() ([]workspacespool.Session, error) {
	all, err := workspacespool.Discover(s.logPath, s.roots, s.workspaces)
	harness := asymptoteobserve.NormalizeHarnessName(endpointSpoolOpts.harness)
	var out []workspacespool.Session
	for _, session := range all {
		if (endpointSpoolOpts.harness == "" || session.Harness == harness) && (endpointSpoolOpts.session == "" || session.ID == endpointSpoolOpts.session) {
			out = append(out, session)
		}
	}
	return out, err
}

func runEndpointSpoolDrain(cmd *cobra.Command, args []string) error {
	// Root never writes or removes files under a user's workspace: a drain run as root could be
	// steered by a planted link into touching files the user's account could not.
	if endpointSpoolGeteuid() == 0 {
		return errors.New("refusing to drain workspace spools as root; run `beacon endpoint spool drain` as the user who owns the workspace")
	}
	userMode := endpointUserMode()
	scope, err := resolveSpoolScope(userMode)
	if err != nil {
		return err
	}
	if scope.logPath == "" {
		return errors.New(scope.note)
	}
	logPath := scope.logPath
	// The user-mode sweep drains these same spools, so wait for it rather than race it. The
	// system-mode root sweep never drains workspace spools, so there is nothing to wait for.
	if userMode {
		release, err := acquireEndpointSweepLock(true, endpointSweepLockWait)
		if err != nil {
			return err
		}
		defer release()
	}
	out := cmd.OutOrStdout()
	if scope.note != "" && !endpointOpts.jsonOutput {
		fmt.Fprintf(out, "note: %s\n", scope.note)
	}
	sessions, discoverErr := scope.sessions()
	if discoverErr != nil && !endpointOpts.jsonOutput {
		// A system log this account cannot read hides only the known sessions; the roots and
		// --workspace still name spools worth draining.
		fmt.Fprintf(out, "note: could not list sessions from %s: %v\n", logPath, discoverErr)
	}
	emit := func(event schema.Event) error {
		// No Budget or Guard: an ordinary user in system mode goes through the ingest socket.
		_, err := writer.AppendEvent(event, writer.Options{Path: logPath, UserMode: userMode})
		return err
	}
	var summary workspacespool.Summary
	drainErr := workspacespool.Drain(sessions, emit, &summary)
	if endpointOpts.jsonOutput {
		if err := json.NewEncoder(out).Encode(summary); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "workspace spool drain: %d sessions, %d events, %d rejected\n",
			summary.Sessions, summary.Events, summary.Rejected)
		if summary.Rejected > 0 {
			fmt.Fprintln(out, "  rejected lines were unreadable, or not a hook event of the session whose spool held them")
		}
	}
	return drainErr
}

type spoolPendingStatus struct {
	Workspace string `json:"workspace"`
	Harness   string `json:"harness"`
	SessionID string `json:"session_id"`
	Bytes     int64  `json:"bytes"`
}

func pendingSpools(sessions []workspacespool.Session) []spoolPendingStatus {
	pending := []spoolPendingStatus{}
	for _, s := range sessions {
		if n := workspacespool.PendingBytes(s); n > 0 {
			pending = append(pending, spoolPendingStatus{Workspace: s.Workspace, Harness: s.Harness, SessionID: s.ID, Bytes: n})
		}
	}
	return pending
}

func runEndpointSpoolStatus(cmd *cobra.Command, args []string) error {
	scope, err := resolveSpoolScope(endpointUserMode())
	if err != nil {
		return err
	}
	sessions, discoverErr := scope.sessions()
	pending := pendingSpools(sessions)
	out := cmd.OutOrStdout()
	if endpointOpts.jsonOutput {
		return json.NewEncoder(out).Encode(struct {
			Sessions []spoolPendingStatus `json:"sessions"`
		}{pending})
	}
	if scope.note != "" {
		fmt.Fprintf(out, "note: %s\n", scope.note)
	}
	if discoverErr != nil {
		fmt.Fprintf(out, "note: could not list sessions from %s: %v\n", scope.logPath, discoverErr)
	}
	if len(pending) == 0 {
		fmt.Fprintln(out, "workspace spools: nothing staged")
		return nil
	}
	fmt.Fprintf(out, "workspace spools: %d session(s) with staged events\n", len(pending))
	for _, p := range pending {
		fmt.Fprintf(out, "  %s  %s  %s  %dB\n", p.Workspace, p.Harness, p.SessionID, p.Bytes)
	}
	return nil
}

// doctorWorkspaceSpoolCheck is a seam so doctor tests can stub the spool scan.
var doctorWorkspaceSpoolCheck = workspaceSpoolCheck

// workspaceSpoolCheck reports hook events staged in workspace spools. Like
// dsh_spool_pending_drain, staged events mean capture worked and only the drain is missing.
// DeepSeek Harness sessions are left to that check, so one spool is never reported twice.
func workspaceSpoolCheck(logPath string, roots []string) (diagnostics.Check, bool) {
	found, _ := workspacespool.Discover(logPath, roots, nil)
	pending := pendingSpools(found)
	sessions := 0
	for _, p := range pending {
		if p.Harness != dshsession.Harness {
			sessions++
		}
	}
	if sessions == 0 {
		return diagnostics.Check{}, false
	}
	return diagnostics.Check{
		Name:     "workspace_spool",
		Target:   "workspace_spool",
		Status:   diagnostics.StatusWarn,
		Severity: diagnostics.SeverityLow,
		Evidence: "workspace_spool_pending_drain",
		Message: fmt.Sprintf(
			"%d session(s) have hook events staged in their workspace spool; capture is working, but the events are not in %s yet",
			sessions, logPath),
		Action: "run `beacon endpoint spool drain` as the user who owns the workspace",
	}, true
}
