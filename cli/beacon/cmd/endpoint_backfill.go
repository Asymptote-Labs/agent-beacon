package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/claudesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/clinesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/codexsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/copilotsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/cursorsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/factorysession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/fxsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/groksession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/hermessession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/openclawsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/opencodesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pisession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/primesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

// The install-time session backfill reads recent history from every agent's own session store,
// the same sweep `beacon endpoint <runtime> sync` runs, so the local dashboard and a first Beacon
// Cloud connect have something to show before any agent runs again.
//
// It is bounded twice. The window keeps it to recent sessions, and the byte budget keeps it to a
// fraction of the runtime log's rotation window (DefaultRotateBytes times the live file and
// DefaultRotateArchives archives, 60 MiB), so a machine with years of history cannot rotate out
// the live telemetry the log already holds. No one runtime may take more than its share, so a
// heavy Claude Code history still leaves room for Codex. Whatever does not fit is not lost: the
// cursors move only past what was written, and a manual sync reads the rest.
const (
	endpointBackfillWindow = 30 * 24 * time.Hour
	// endpointBackfillEnv set to 0 turns the backfill off for install and connect.
	endpointBackfillEnv = "BEACON_BACKFILL"
)

// The byte limits are variables only so tests can make them small.
var (
	endpointBackfillMaxBytes   int64 = 24 << 20
	endpointBackfillShareBytes int64 = 12 << 20
)

// endpointBackfillRun is what one runtime's sweep needs from the orchestrator.
type endpointBackfillRun struct {
	LogPath string
	// UserMode picks the endpoint's own cursor files and log permissions. A system endpoint keeps
	// its cursors under the system base directory while it reads the console user's stores.
	UserMode bool
	Since    time.Time
	Budget   *writer.Budget
}

// endpointBackfillRunner is one session-store collector.
type endpointBackfillRunner struct {
	// Name is how the summary names the runtime.
	Name string
	// Sync is the command that reads the rest when the budget runs out.
	Sync string
	// Run sweeps the store once and reports how many events it wrote.
	Run func(endpointBackfillRun) (int, error)
}

// endpointBackfillRunners lists every session-store collector, a seam for tests. Each one runs with
// the same defaults its own sync command uses for the endpoint's mode, and reads its store only
// through internal/sessionfs, so a system sweep is confined to the console user's home; a new
// collector joins the list in sessionfs's collectors_test.go too.
var endpointBackfillRunners = func() []endpointBackfillRunner {
	return []endpointBackfillRunner{
		{Name: "Claude Code", Sync: "beacon endpoint claude sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := claudesession.CollectOnce(claudesession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveClaudeStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Codex CLI", Sync: "beacon endpoint codex sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := codexsession.CollectOnce(codexsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveCodexStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Cursor", Sync: "beacon endpoint cursor sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := cursorsession.CollectOnce(cursorsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveCursorStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "OpenCode", Sync: "beacon endpoint opencode sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := opencodesession.CollectOnce(opencodesession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveOpenCodeStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "GitHub Copilot CLI", Sync: "beacon endpoint copilot sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := copilotsession.CollectOnce(copilotsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveCopilotStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Cline", Sync: "beacon endpoint cline sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := clinesession.CollectOnce(clinesession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveClineStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Factory Droid", Sync: "beacon endpoint factory sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := factorysession.CollectOnce(factorysession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveFactoryStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Pi", Sync: "beacon endpoint pi sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := pisession.CollectOnce(pisession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolvePiStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Prime Agent", Sync: "beacon endpoint prime sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := primesession.CollectOnce(primesession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolvePrimeStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Hermes Agent", Sync: "beacon endpoint hermes sync", Run: func(r endpointBackfillRun) (int, error) {
			dbPath := hermessession.DefaultDBPath()
			statePath, err := resolveHermesStatePath("", dbPath, r.UserMode)
			if err != nil {
				return 0, err
			}
			s, err := hermessession.CollectOnce(hermessession.CollectOptions{DBPath: dbPath, Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: statePath, Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "DeepSeek Harness", Sync: "beacon endpoint dsh sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := dshsession.CollectOnce(dshsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveDshStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "fx", Sync: "beacon endpoint fx sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := fxsession.CollectOnce(fxsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveFxStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Grok Build", Sync: "beacon endpoint grok sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := groksession.CollectOnce(groksession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveGrokStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "OpenClaw Gateway", Sync: "beacon endpoint integrations openclaw sessions sync", Run: func(r endpointBackfillRun) (int, error) {
			s, err := openclawsession.CollectOnce(openclawsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveOpenClawSessionsStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
	}
}

// endpointBackfillScratchDir is where a system backfill copies SQLite stores it reads as root: under
// the root-owned system base directory, on disk, rather than a /tmp that is often memory-backed. A
// seam for tests.
var endpointBackfillScratchDir = func() string {
	return filepath.Join(endpointconfig.BaseDir(false), "state", "backfill-snapshots")
}

// endpointBackfillNow is time.Now, a seam for tests.
var endpointBackfillNow = time.Now

// endpointBackfillRuntimeResult is one runtime's part of a backfill.
type endpointBackfillRuntimeResult struct {
	Name    string
	Sync    string
	Events  int
	Limited bool
	Err     error
	// Oversized names the databases a system backfill would not copy because they are too large;
	// Sync, run as the user, reads them in place.
	Oversized []string
}

// endpointBackfillResult is what one backfill wrote.
type endpointBackfillResult struct {
	Since    time.Time
	Events   int
	Bytes    int64
	Limited  bool
	Runtimes []endpointBackfillRuntimeResult
	// Skipped says why nothing was read, such as a system endpoint with no console user to read for.
	Skipped string
	// Refused counts the store paths a system backfill would not read as root, and RefusedExamples
	// names a few of them with the reason.
	Refused         int
	RefusedExamples []string
}

// endpointBackfillEnabled reports whether this install or connect should backfill. Every real
// install and connect does, in user and system mode alike; only a dry run, --no-backfill and
// BEACON_BACKFILL=0 turn it off.
func endpointBackfillEnabled() bool {
	if endpointOpts.dryRun || endpointOpts.noBackfill {
		return false
	}
	return strings.TrimSpace(os.Getenv(endpointBackfillEnv)) != "0"
}

// endpointBackfillDays is the backfill window in days for an interactive user install, or zero when
// the backfill is off; the onboarding wizard names it on its confirm screen.
func endpointBackfillDays() int {
	if !endpointBackfillEnabled() {
		return 0
	}
	return int(endpointBackfillWindow / (24 * time.Hour))
}

// runEndpointBackfill sweeps every session store once into logPath, newest sessions first, within
// the window and the budget. A runtime that fails is reported and the rest still run: the backfill
// is a convenience on top of a working install and never fails it.
//
// A user endpoint reads the invoking user's stores. A system endpoint runs as root, whose home holds
// no agent sessions, so it reads the active console user's stores instead, the same person system
// hook repair and inventory resolve, while its cursors stay under the system base directory. With
// nobody to read for, it reads nothing and says so.
func runEndpointBackfill(logPath string, userMode bool) endpointBackfillResult {
	if userMode {
		return sweepEndpointBackfill(logPath, true)
	}
	info, ok, err := activeConsoleUser()
	switch {
	case err != nil:
		return endpointBackfillResult{Skipped: fmt.Sprintf("could not resolve the console user: %v", err)}
	case !ok || strings.TrimSpace(info.HomeDir) == "":
		return endpointBackfillResult{Skipped: "no active console user to read agent sessions for"}
	}
	// Root reads a home the console user controls, so every read is confined to that home and to
	// regular files that user owns: a planted symlink, hard link, FIFO or device is skipped rather
	// than read with root's privileges. See internal/sessionfs.
	uid, err := sessionfs.OwnerUID(info.HomeDir)
	if err != nil {
		return endpointBackfillResult{Skipped: fmt.Sprintf("could not read the owner of the console user's home: %v", err)}
	}
	confined, err := sessionfs.Confine(info.HomeDir, uid, sessionfs.Options{ScratchDir: endpointBackfillScratchDir()})
	if err != nil {
		return endpointBackfillResult{Skipped: err.Error()}
	}
	defer confined.Release()
	result, _ := withUserHome(info.HomeDir, func() (endpointBackfillResult, error) {
		return sweepEndpointBackfill(logPath, false), nil
	})
	result.Refused, result.RefusedExamples = confined.Refused()
	return result
}

func sweepEndpointBackfill(logPath string, userMode bool) endpointBackfillResult {
	result := endpointBackfillResult{Since: endpointBackfillNow().Add(-endpointBackfillWindow)}
	total := writer.NewBudget(endpointBackfillMaxBytes)
	for _, runner := range endpointBackfillRunners() {
		share := total.Sub(endpointBackfillShareBytes)
		events, err := runner.Run(endpointBackfillRun{LogPath: logPath, UserMode: userMode, Since: result.Since, Budget: share})
		// A confined (system) sweep reads SQLite stores through private snapshots; drop each
		// runtime's before the next one runs. A no-op otherwise.
		sessionfs.DropSnapshots()
		runtime := endpointBackfillRuntimeResult{Name: runner.Name, Sync: runner.Sync, Events: events, Oversized: sessionfs.TakeOversized()}
		if errors.Is(err, writer.ErrBudgetSpent) || share.Refused() {
			runtime.Limited = true
			result.Limited = true
		}
		if err != nil && !onlyBudgetSpent(err) {
			runtime.Err = err
		}
		result.Events += events
		if events > 0 || runtime.Limited || runtime.Err != nil || len(runtime.Oversized) > 0 {
			result.Runtimes = append(result.Runtimes, runtime)
		}
	}
	result.Bytes = total.Used()
	return result
}

// onlyBudgetSpent reports whether err says nothing but that the budget ran out.
func onlyBudgetSpent(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if !onlyBudgetSpent(e) {
				return false
			}
		}
		return true
	}
	return errors.Is(err, writer.ErrBudgetSpent)
}

// reportEndpointBackfill prints what a backfill wrote: one line when it went cleanly, plus a line
// per runtime that hit the limit or failed, each naming the command that picks up from there.
func reportEndpointBackfill(out io.Writer, result endpointBackfillResult) {
	days := int(endpointBackfillWindow / (24 * time.Hour))
	if result.Skipped != "" {
		fmt.Fprintf(out, "Session backfill skipped: %s.\n", result.Skipped)
		return
	}
	if result.Events == 0 && !result.Limited && len(result.Runtimes) == 0 {
		fmt.Fprintf(out, "Session backfill: no new agent session activity from the last %d days.\n", days)
		reportEndpointBackfillRefusals(out, result)
		return
	}
	var parts []string
	for _, r := range result.Runtimes {
		if r.Events > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", r.Name, r.Events))
		}
	}
	line := fmt.Sprintf("Session backfill: %d events from the last %d days", result.Events, days)
	if len(parts) > 0 {
		line += " (" + strings.Join(parts, ", ") + ")"
	}
	fmt.Fprintln(out, line+".")
	for _, r := range result.Runtimes {
		for _, path := range r.Oversized {
			fmt.Fprintf(out, "  %s: %s is too large for a system backfill to copy; run `%s` as the console user to read it.\n", r.Name, path, r.Sync)
		}
		switch {
		case r.Err != nil && len(r.Oversized) > 0:
			// The error is the collector failing to open the store named just above.
		case r.Err != nil:
			fmt.Fprintf(out, "  %s: %v; retry with `%s`.\n", r.Name, r.Err, r.Sync)
		case r.Limited:
			fmt.Fprintf(out, "  %s: stopped at the backfill size limit; `%s` reads the rest.\n", r.Name, r.Sync)
		}
	}
	reportEndpointBackfillRefusals(out, result)
}

// endpointConnectBackfill is the backfill a connect ships, or nil when it ships none. Every connect
// ships one, first or not, user or system: the staged lines keep the deterministic event.id they
// were written with, so history the forwarder already shipped is the same event to the ingest side
// rather than a second one. synced says this process has just run the install-time backfill, so
// connect does not sweep again and spend a second budget; it still stages what the first sweep wrote.
func endpointConnectBackfill(out io.Writer, userMode bool, logPath string, synced bool) *asymptote.ConnectBackfill {
	if !endpointBackfillEnabled() {
		return nil
	}
	backfill := &asymptote.ConnectBackfill{Options: asymptote.BackfillOptions{
		Since:    endpointBackfillNow().Add(-endpointBackfillWindow),
		MaxBytes: endpointBackfillMaxBytes,
	}}
	if !synced {
		backfill.Sync = func() error {
			reportEndpointBackfill(out, runEndpointBackfill(logPath, userMode))
			return nil
		}
	}
	return backfill
}

// reportEndpointBackfillRefusals says what a system backfill left out of the console user's home.
func reportEndpointBackfillRefusals(out io.Writer, result endpointBackfillResult) {
	if result.Refused == 0 {
		return
	}
	fmt.Fprintf(out, "  Skipped %d session store path(s) a system backfill does not read as root (links leading out of the console user's home, files that user does not own, special files, or stores too large to copy):\n", result.Refused)
	for _, example := range result.RefusedExamples {
		fmt.Fprintf(out, "    %s\n", example)
	}
}
