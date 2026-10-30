package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/devinsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/spf13/cobra"
)

// `beacon endpoint devin` complements the live Devin CLI hook integration.
//
// Devin CLI also keeps committed session history in ~/.local/share/devin/cli/sessions.db.
// Reading that store is a local, offline poll path: useful for backfill and for everything the
// hook payload does not carry -- assistant text, reasoning, per-message token metrics and the
// per-turn model -- but unable to approve, deny, or delay a tool call. Live hooks remain the
// real-time path.
var endpointDevinCmd = &cobra.Command{
	Use:   "devin",
	Short: "Collect telemetry from Devin CLI sessions",
	Long: `Collect endpoint telemetry from Devin CLI's local session database.

Devin hooks remain the real-time integration. This command reads the committed
SQLite session store at ~/.local/share/devin/cli/sessions.db read-only and converts
historical or missed rows into endpoint events marked harness.collection_method=poll.
Sessions the live hooks already cover get only what the hook payload cannot carry:
assistant text, reasoning, per-message token metrics and the per-turn model.`,
}

var endpointDevinSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Read new Devin CLI session rows into the runtime log",
	Long: `Read new Devin CLI session rows into the runtime log.

The runtime log keeps the live file plus five archives, 10 MiB each by default, and every
rotation deletes the oldest. A sweep never rotates out its own output: when writing more would
delete a log file holding the sweep's output, it stops, reports how many sessions are still
pending, and leaves their cursors where they were, so the next sweep picks them up. Between
sweeps, ship or copy the log files if you need to keep every event, or backfill a large history
in one sweep with a larger --rotate-bytes.`,
	SilenceUsage: true,
	RunE:         runEndpointDevinSync,
}

var endpointDevinStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Devin CLI sessions on this machine and how much has been collected",
	SilenceUsage: true,
	RunE:         runEndpointDevinStatus,
}

var endpointDevinOpts struct {
	dbPath      string
	statePath   string
	logPath     string
	print       bool
	watch       bool
	interval    time.Duration
	rotateBytes int64
}

func init() {
	endpointCmd.AddCommand(endpointDevinCmd)
	endpointDevinCmd.AddCommand(endpointDevinSyncCmd)
	endpointDevinCmd.AddCommand(endpointDevinStatusCmd)

	for _, c := range []*cobra.Command{endpointDevinSyncCmd, endpointDevinStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointDevinOpts.dbPath, "db", "", "Devin CLI session database (default ~/.local/share/devin/cli/sessions.db, %APPDATA%\\devin\\cli\\sessions.db on Windows)")
		f.StringVar(&endpointDevinOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/devin.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointDevinSyncCmd.Flags()
	sync.StringVar(&endpointDevinOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointDevinOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointDevinOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointDevinOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
	addSyncRotateBytesFlag(sync, &endpointDevinOpts.rotateBytes)
}

// devinSyncOptions turns the sync flags into collector options.
func devinSyncOptions(cmd *cobra.Command) (devinsession.CollectOptions, error) {
	if err := checkSyncRotateBytes(endpointDevinOpts.rotateBytes); err != nil {
		return devinsession.CollectOptions{}, err
	}
	userMode := endpointUserMode()
	logPath := lifecycle.ResolveRuntimeLog(userMode, endpointDevinOpts.logPath).EffectiveLogPath
	opts := devinsession.CollectOptions{
		DBPath:      endpointDevinOpts.dbPath,
		Print:       endpointDevinOpts.print,
		Out:         cmd.OutOrStdout(),
		Write:       !endpointDevinOpts.print,
		UserMode:    userMode,
		RotateBytes: endpointDevinOpts.rotateBytes,
		// Live coverage comes from the log this sweep writes to, in print and write modes
		// alike: a dry run should show exactly what a real sweep would emit.
		LiveCoverage: endpointDevinLiveCoverage(logPath),
	}
	if !endpointDevinOpts.print {
		opts.StatePath = resolveEndpointDevinStatePath(endpointDevinOpts.statePath, userMode)
		opts.LogPath = logPath
	}
	return opts, nil
}

func runEndpointDevinSync(cmd *cobra.Command, args []string) error {
	opts, err := devinSyncOptions(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointDevinOpts.watch && !endpointDevinOpts.print {
		return watchEndpointDevin(ctx, cmd, opts)
	}

	summary, err := devinsession.CollectOnce(opts)
	reportEndpointDevinSweep(cmd, summary)
	return err
}

func watchEndpointDevin(ctx context.Context, cmd *cobra.Command, opts devinsession.CollectOptions) error {
	interval := endpointDevinOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := devinsession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Devin CLI sync error: %v\n", err)
		}
		reportEndpointDevinSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportEndpointDevinSweep(cmd *cobra.Command, summary devinsession.Summary) {
	if endpointDevinOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Devin CLI sync: %d sessions, %d changed, %d events, %d suppressed (live hooks), %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.EventsSuppressedLive, summary.Errors)
	if summary.RetentionLimited {
		fmt.Fprintf(cmd.OutOrStdout(), "  stopped before log rotation would discard this sweep's own events; %d session(s) left pending for the next sweep.\n"+
			"  Ship or copy the runtime log before the next sweep to keep every event, or backfill in one sweep with a larger --rotate-bytes.\n",
			summary.SessionsPending)
	}
	if summary.EventsRotatedOut > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d event(s) this sweep wrote were rotated out of the runtime log by another writer before it finished\n",
			summary.EventsRotatedOut)
	}
}

// endpointDevinLiveCoverage finds, per Devin session id, the earliest hook event the retained
// runtime log holds. The poll sweep uses it to skip what the hooks already recorded: a session
// the hooks never saw is collected whole, while a covered one keeps only the events the hook
// payload cannot carry.
//
// Every retained archive is read because coverage can sit several rotations back on a busy
// machine; a missing file is simply not evidence. The scan decodes only the three fields it
// needs, so a runtime.jsonl of any size costs a line at a time, and only lines that could be a
// Devin event are unmarshalled.
func endpointDevinLiveCoverage(logPath string) map[string]time.Time {
	coverage := map[string]time.Time{}
	for _, path := range writer.RetainedLogPaths(logPath) {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			if !bytes.Contains(line, []byte("devin")) {
				continue
			}
			var event struct {
				Timestamp string `json:"timestamp"`
				Harness   struct {
					Name             string `json:"name"`
					CollectionMethod string `json:"collection_method"`
				} `json:"harness"`
				Session struct {
					ID string `json:"id"`
				} `json:"session"`
			}
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			name := asymptoteobserve.NormalizeHarnessName(event.Harness.Name)
			if (name != "devin-cli" && name != "devin") || event.Harness.CollectionMethod != asymptoteobserve.CollectionMethodHook {
				continue
			}
			sessionID := strings.TrimSpace(event.Session.ID)
			if sessionID == "" {
				continue
			}
			ts, err := asymptoteobserve.ParseTimestamp(event.Timestamp)
			if err != nil {
				continue
			}
			if earliest, ok := coverage[sessionID]; !ok || ts.Before(earliest) {
				coverage[sessionID] = ts
			}
		}
		_ = f.Close()
	}
	if len(coverage) == 0 {
		return nil
	}
	return coverage
}

type devinSessionStatus struct {
	SessionID        string `json:"session_id"`
	Title            string `json:"title,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	StartedAt        string `json:"started_at,omitempty"`
	LastActivityAt   string `json:"last_activity_at,omitempty"`
	Hidden           bool   `json:"hidden,omitempty"`
	LastRowID        int64  `json:"last_row_id"`
	CollectedRowID   int64  `json:"collected_row_id"`
	Collected        bool   `json:"collected"`
}

type devinStatusReport struct {
	DBPath    string               `json:"db_path"`
	Present   bool                 `json:"present"`
	StatePath string               `json:"state_path,omitempty"`
	Sessions  []devinSessionStatus `json:"sessions"`
}

func runEndpointDevinStatus(cmd *cobra.Command, args []string) error {
	dbPath := endpointDevinOpts.dbPath
	if dbPath == "" {
		dbPath = devinsession.DefaultDBPath()
	}
	statePath := resolveEndpointDevinStatePath(endpointDevinOpts.statePath, endpointUserMode())
	report := devinStatusReport{DBPath: dbPath, StatePath: statePath}

	store, err := devinsession.OpenStore(dbPath)
	if err != nil {
		if endpointOpts.jsonOutput {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Devin CLI session database not found or unreadable: %s\n", dbPath)
		return nil
	}
	defer store.Close()
	report.Present = true

	state, err := devinsession.LoadState(statePath)
	if err != nil {
		return err
	}
	sessions, err := store.ListSessions()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		lastID, err := store.LastRowID(session.ID)
		if err != nil {
			return err
		}
		status := devinSessionStatus{
			SessionID:        session.ID,
			Title:            session.Title,
			WorkingDirectory: session.WorkingDirectory,
			Hidden:           session.Hidden,
			LastRowID:        lastID,
		}
		if session.CreatedAtMS > 0 {
			status.StartedAt = time.UnixMilli(session.CreatedAtMS).UTC().Format(time.RFC3339)
		}
		if session.LastActivityAtMS > 0 {
			status.LastActivityAt = time.UnixMilli(session.LastActivityAtMS).UTC().Format(time.RFC3339)
		}
		if cursor := state.Sessions[session.ID]; cursor != nil {
			status.CollectedRowID = cursor.LastRowID
			status.Collected = cursor.Started && cursor.LastRowID >= lastID
		}
		report.Sessions = append(report.Sessions, status)
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Devin CLI session database: %s\n", report.DBPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Collector state: %s\n", report.StatePath)
	if len(report.Sessions) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No Devin CLI sessions found.")
		return nil
	}
	for _, session := range report.Sessions {
		mark := "pending"
		if session.Collected {
			mark = "collected"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "- %s [%s] rows %d/%d %s\n",
			session.SessionID, mark, session.CollectedRowID, session.LastRowID, session.Title)
	}
	return nil
}

// resolveEndpointDevinStatePath keeps the cursor beside the log the sweep writes to, like the
// other collectors: a system-mode sweep's cursor belongs next to the system runtime log rather
// than in whichever home the operator happened to run from.
func resolveEndpointDevinStatePath(path string, userMode bool) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	if !userMode {
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "devin-state.json")
		}
	}
	return devinsession.DefaultStatePath()
}
