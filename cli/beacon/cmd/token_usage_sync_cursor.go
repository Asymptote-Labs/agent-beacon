package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/cursorusage"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/spf13/cobra"
)

var syncCursorOpts struct {
	db           string
	state        string
	logPath      string
	print        bool
	since        string
	rebuildState bool
	userMode     bool
	systemMode   bool
}

var tokenUsageSyncCursorCmd = &cobra.Command{
	Use:   "sync-cursor",
	Short: "Sync Cursor token usage from its local state store into the runtime log",
	Long: `Extract runtime-recorded token usage from Cursor's local state store
(state.vscdb) and append it to the Beacon runtime log as canonical token.usage
events, so 'beacon token-usage' and the dashboard token view cover Cursor.

Cursor's hook payloads carry no token counts today, so this explicit,
user-initiated command is the local source for them. It is read-only and
offline: it copies the state store to a private snapshot before reading (never
touching Cursor's live files) and writes only to the local runtime log.

The store is undocumented and best-effort: generations without recorded token
counts are skipped (some Cursor builds record zeros; Beacon never estimates
token counts), and unparseable rows are skipped and counted in the summary.
Repeated runs are idempotent — synced generations are remembered in a local
state file and skipped on later runs.`,
	SilenceUsage: true,
	RunE:         runTokenUsageSyncCursor,
}

func init() {
	tokenUsageCmd.AddCommand(tokenUsageSyncCursorCmd)

	f := tokenUsageSyncCursorCmd.Flags()
	f.StringVar(&syncCursorOpts.db, "db", "", "Cursor state database path (default: Cursor's per-OS state.vscdb location)")
	f.StringVar(&syncCursorOpts.state, "state", "", "Dedup state file path (default ~/.beacon/cursor/usage-sync-state.json)")
	f.StringVar(&syncCursorOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	f.BoolVar(&syncCursorOpts.print, "print", false, "Print events as JSON without writing or recording state (dry run)")
	f.StringVar(&syncCursorOpts.since, "since", "", "Only sync generations at or after this RFC3339 timestamp")
	f.BoolVar(&syncCursorOpts.rebuildState, "rebuild-state", false, "Rebuild the dedup state from the runtime log before syncing")
	f.BoolVar(&syncCursorOpts.userMode, "user", true, "Use per-user endpoint log paths")
	f.BoolVar(&syncCursorOpts.systemMode, "system", false, "Use system endpoint log paths")
}

func runTokenUsageSyncCursor(cmd *cobra.Command, args []string) error {
	dbPath := strings.TrimSpace(syncCursorOpts.db)
	if dbPath == "" {
		resolved, err := cursorusage.DefaultDBPath()
		if err != nil {
			return fmt.Errorf("resolve Cursor state database path: %w", err)
		}
		dbPath = resolved
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("cursor state database not found at %s (is Cursor installed?): %w", dbPath, err)
	}

	opts := cursorusage.Options{
		DBPath: dbPath,
		Print:  syncCursorOpts.print,
		Out:    cmd.OutOrStdout(),
	}
	if since := strings.TrimSpace(syncCursorOpts.since); since != "" {
		ts, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return fmt.Errorf("--since must be RFC3339 (e.g. 2026-07-01T00:00:00Z): %w", err)
		}
		opts.Since = ts
	}
	userMode := syncCursorUserMode()
	if !syncCursorOpts.print {
		opts.UserMode = userMode
		opts.StatePath = resolveCursorUsageStatePath(syncCursorOpts.state)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, syncCursorOpts.logPath).EffectiveLogPath
		if syncCursorOpts.rebuildState {
			if err := os.Remove(opts.StatePath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove state for rebuild: %w", err)
			}
		}
	}

	sum, err := cursorusage.SyncOnce(opts)
	if err != nil {
		return err
	}
	if !syncCursorOpts.print {
		fmt.Fprintf(cmd.OutOrStdout(),
			"token-usage sync-cursor: %d bubbles, %d emitted, %d zero-count skipped, %d already synced, %d before --since, %d parse errors\n",
			sum.Bubbles, sum.Emitted, sum.SkippedZero, sum.SkippedDedup, sum.SkippedBefore, sum.ParseErrors)
	}
	return nil
}

func syncCursorUserMode() bool {
	if syncCursorOpts.systemMode {
		return false
	}
	return syncCursorOpts.userMode
}

// resolveCursorUsageStatePath always returns a non-empty path so dedup state
// persists across runs, preferring ~/.beacon with an OS temp dir fallback,
// matching the other local connectors.
func resolveCursorUsageStatePath(override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "cursor", "usage-sync-state.json")
}
