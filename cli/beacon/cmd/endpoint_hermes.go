package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/hermessession"
	"github.com/spf13/cobra"
)

// `beacon endpoint hermes` complements the live Hermes hook integration.
//
// Hermes also keeps committed session history in ~/.hermes/state.db. Reading that store is a local,
// offline poll path: useful for backfill and transcript coverage, but unable to approve, deny, or
// delay tool calls. Live hooks remain the real-time path.
var endpointHermesCmd = &cobra.Command{
	Use:   "hermes",
	Short: "Collect telemetry from Hermes Agent sessions",
	Long: `Collect endpoint telemetry from Hermes Agent's local session database.

Hermes hooks remain the real-time integration. This command reads the committed
SQLite session store at ~/.hermes/state.db and converts historical or missed rows
into endpoint events marked harness.collection_method=poll.`,
}

var endpointHermesSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Hermes session rows into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointHermesSync,
}

var endpointHermesStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Hermes sessions on this machine and how much has been collected",
	SilenceUsage: true,
	RunE:         runEndpointHermesStatus,
}

var endpointHermesOpts struct {
	dbPath      string
	statePath   string
	logPath     string
	print       bool
	watch       bool
	interval    time.Duration
	rotateBytes int64
}

func init() {
	endpointCmd.AddCommand(endpointHermesCmd)
	endpointHermesCmd.AddCommand(endpointHermesSyncCmd)
	endpointHermesCmd.AddCommand(endpointHermesStatusCmd)

	for _, c := range []*cobra.Command{endpointHermesSyncCmd, endpointHermesStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointHermesOpts.dbPath, "db", "", "Hermes state database (default $HERMES_HOME/state.db when set, otherwise the native Hermes data root)")
		f.StringVar(&endpointHermesOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/hermes.json for the native Hermes root, hermes-<profile>-<id>.json for any other home)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointHermesSyncCmd.Flags()
	sync.StringVar(&endpointHermesOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointHermesOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointHermesOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointHermesOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
	addSyncRotateBytesFlag(sync, &endpointHermesOpts.rotateBytes)
}

func runEndpointHermesSync(cmd *cobra.Command, args []string) error {
	if err := checkSyncRotateBytes(endpointHermesOpts.rotateBytes); err != nil {
		return err
	}
	userMode := endpointUserMode()
	dbPath := resolveHermesDBPath(endpointHermesOpts.dbPath)
	opts := hermessession.CollectOptions{
		RotateBytes: endpointHermesOpts.rotateBytes,
		DBPath:      dbPath,
		Print:       endpointHermesOpts.print,
		Out:         cmd.OutOrStdout(),
		Write:       !endpointHermesOpts.print,
		UserMode:    userMode,
	}
	if !endpointHermesOpts.print {
		opts.StatePath = resolveHermesStatePath(endpointHermesOpts.statePath, dbPath, userMode)
		opts.LegacyStatePath = legacyHermesStatePath(endpointHermesOpts.statePath, dbPath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointHermesOpts.logPath).EffectiveLogPath
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointHermesOpts.watch && !endpointHermesOpts.print {
		return watchHermes(ctx, cmd, opts)
	}
	summary, err := hermessession.CollectOnce(opts)
	reportHermesSweep(cmd, summary)
	return err
}

func watchHermes(ctx context.Context, cmd *cobra.Command, opts hermessession.CollectOptions) error {
	interval := endpointHermesOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := hermessession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Hermes sync error: %v\n", err)
		}
		reportHermesSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportHermesSweep(cmd *cobra.Command, summary hermessession.Summary) {
	if endpointHermesOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Hermes sync: %d sessions, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	reportSyncRetention(cmd.OutOrStdout(), summary.Retention)
}

type hermesSessionStatus struct {
	SessionID          string `json:"session_id"`
	Title              string `json:"title,omitempty"`
	WorkingDirectory   string `json:"working_directory,omitempty"`
	StartedAt          string `json:"started_at,omitempty"`
	EndedAt            string `json:"ended_at,omitempty"`
	LastMessageID      int64  `json:"last_message_id"`
	CollectedMessageID int64  `json:"collected_message_id"`
	Collected          bool   `json:"collected"`
	// IDsKeyedByHome is set when this home keys the session's event ids by its own path, because
	// another home, such as the profile it was cloned from, had already collected the same session id.
	IDsKeyedByHome bool `json:"ids_keyed_by_home,omitempty"`
}

type hermesStatusReport struct {
	DBPath    string                `json:"db_path"`
	Present   bool                  `json:"present"`
	StatePath string                `json:"state_path,omitempty"`
	Sessions  []hermesSessionStatus `json:"sessions"`
}

func runEndpointHermesStatus(cmd *cobra.Command, args []string) error {
	dbPath := resolveHermesDBPath(endpointHermesOpts.dbPath)
	statePath := resolveHermesStatePath(endpointHermesOpts.statePath, dbPath, endpointUserMode())
	legacyStatePath := legacyHermesStatePath(endpointHermesOpts.statePath, dbPath, endpointUserMode())
	report := hermesStatusReport{DBPath: dbPath, StatePath: statePath}

	store, err := hermessession.OpenStore(dbPath)
	if err != nil {
		if endpointOpts.jsonOutput {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Hermes state database not found or unreadable: %s\n", dbPath)
		return nil
	}
	defer store.Close()
	report.Present = true

	state, err := hermessession.LoadStateSeeded(statePath, legacyStatePath)
	if err != nil {
		return err
	}
	sessions, err := store.ListSessions()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		lastID, err := store.LastMessageID(session.ID)
		if err != nil {
			return err
		}
		status := hermesSessionStatus{
			SessionID:        session.ID,
			Title:            session.Title,
			WorkingDirectory: session.CWD,
			LastMessageID:    lastID,
		}
		if session.StartedAtMS > 0 {
			status.StartedAt = time.UnixMilli(session.StartedAtMS).UTC().Format(time.RFC3339)
		}
		if session.EndedAtMS > 0 {
			status.EndedAt = time.UnixMilli(session.EndedAtMS).UTC().Format(time.RFC3339)
		}
		if cursor := state.Sessions[session.ID]; cursor != nil {
			status.CollectedMessageID = cursor.LastMessageID
			status.Collected = cursor.Started && cursor.LastMessageID >= lastID && (session.EndedAtMS == 0 || cursor.EndedAtMS == session.EndedAtMS)
			status.IDsKeyedByHome = cursor.KeyedByHome
		}
		report.Sessions = append(report.Sessions, status)
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Hermes state database: %s\n", report.DBPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Collector state: %s\n", report.StatePath)
	if len(report.Sessions) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No Hermes sessions found.")
		return nil
	}
	for _, session := range report.Sessions {
		mark := "pending"
		if session.Collected {
			mark = "collected"
		}
		if session.IDsKeyedByHome {
			mark += ", ids keyed by home"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "- %s [%s] messages %d/%d %s\n",
			session.SessionID, mark, session.CollectedMessageID, session.LastMessageID, session.Title)
	}
	return nil
}

// resolveHermesDBPath fixes the database a command reads once, before anything is derived from it.
// The default is resolved here rather than per sweep, so a --watch that outlives `hermes profile use`
// keeps reading the store its cursor belongs to, and a relative --db is made absolute so the home,
// profile and cursor name come from the real directory rather than ".".
func resolveHermesDBPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return hermessession.DefaultDBPath()
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// resolveHermesStatePath keeps the cursor beside the log the sweep writes to. A system-mode sweep
// collects on behalf of the machine, so its cursor belongs next to the system runtime log rather
// than in whichever home the operator happened to run from; everything else uses the per-user
// default in hermessession.DefaultStatePath. Either default is keyed by the Hermes home dbPath is
// in, so syncing one profile or store never moves another's cursor.
func resolveHermesStatePath(path, dbPath string, userMode bool) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	if dbPath == "" {
		dbPath = hermessession.DefaultDBPath()
	}
	hermesHome := filepath.Dir(dbPath)
	if dir := hermesSystemStateDir(userMode); dir != "" {
		return filepath.Join(dir, hermessession.StateFileName("hermes-state", hermesHome))
	}
	return hermessession.DefaultStatePath(hermesHome)
}

// legacyHermesStatePath is the unkeyed cursor a default sweep advanced before cursors were keyed by
// home: hermes.json, or hermes-state.json beside the system log. Every default sweep used it, so it
// belongs to whatever home DefaultDBPath() resolves to, which need not be the native root when
// HERMES_HOME or a sticky profile is set. It is empty for an explicit --state, for any other home,
// and when the keyed cursor already is the legacy file.
func legacyHermesStatePath(path, dbPath string, userMode bool) string {
	if strings.TrimSpace(path) != "" {
		return ""
	}
	if dbPath == "" {
		dbPath = hermessession.DefaultDBPath()
	}
	if !hermessession.SameHome(filepath.Dir(dbPath), filepath.Dir(hermessession.DefaultDBPath())) {
		return ""
	}
	keyed := resolveHermesStatePath("", dbPath, userMode)
	name := "hermes.json"
	if hermesSystemStateDir(userMode) != "" {
		name = "hermes-state.json"
	}
	if legacy := filepath.Join(filepath.Dir(keyed), name); legacy != keyed {
		return legacy
	}
	return ""
}

// hermesSystemStateDir is the directory beside the system runtime log that a system-mode sweep keeps
// its cursor in, or empty in user mode or when that log has no usable directory.
func hermesSystemStateDir(userMode bool) string {
	if userMode {
		return ""
	}
	if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
		return dir
	}
	return ""
}
