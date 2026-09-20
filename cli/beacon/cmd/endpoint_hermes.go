package cmd

import (
	"context"
	"encoding/json"
	"fmt"
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
	dbPath    string
	statePath string
	logPath   string
	print     bool
	watch     bool
	interval  time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointHermesCmd)
	endpointHermesCmd.AddCommand(endpointHermesSyncCmd)
	endpointHermesCmd.AddCommand(endpointHermesStatusCmd)

	for _, c := range []*cobra.Command{endpointHermesSyncCmd, endpointHermesStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointHermesOpts.dbPath, "db", "", "Hermes state database (default ~/.hermes/state.db)")
		f.StringVar(&endpointHermesOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/hermes.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointHermesSyncCmd.Flags()
	sync.StringVar(&endpointHermesOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointHermesOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointHermesOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointHermesOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointHermesSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := hermessession.CollectOptions{
		DBPath:   endpointHermesOpts.dbPath,
		Print:    endpointHermesOpts.print,
		Out:      cmd.OutOrStdout(),
		Write:    !endpointHermesOpts.print,
		UserMode: userMode,
	}
	if !endpointHermesOpts.print {
		opts.StatePath = resolveHermesStatePath(endpointHermesOpts.statePath)
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
}

type hermesStatusReport struct {
	DBPath    string                `json:"db_path"`
	Present   bool                  `json:"present"`
	StatePath string                `json:"state_path,omitempty"`
	Sessions  []hermesSessionStatus `json:"sessions"`
}

func runEndpointHermesStatus(cmd *cobra.Command, args []string) error {
	dbPath := endpointHermesOpts.dbPath
	if dbPath == "" {
		dbPath = hermessession.DefaultDBPath()
	}
	statePath := resolveHermesStatePath(endpointHermesOpts.statePath)
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

	state, err := hermessession.LoadState(statePath)
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
		fmt.Fprintf(cmd.OutOrStdout(), "- %s [%s] messages %d/%d %s\n",
			session.SessionID, mark, session.CollectedMessageID, session.LastMessageID, session.Title)
	}
	return nil
}

func resolveHermesStatePath(path string) string {
	if path != "" {
		return path
	}
	return hermessession.DefaultStatePath()
}
