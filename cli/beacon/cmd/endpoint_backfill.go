package cmd

import (
	"context"
	"encoding/json"
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
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/devinsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/factorysession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/fxsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/groksession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/harness2session"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/hermessession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/openclawsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/opencodesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pisession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/primesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
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
	// State lists the cursor files Run saves for a mode, resolved in the home the sweep reads. A
	// skipped backfill floors only the runtimes with none of them (recordSkippedBackfill).
	State func(userMode bool) []string
	// SQLite says the store is a SQLite database, which a confined (system) sweep reads from a
	// private copy; the scheduled session sync takes that copy at most every endpointSnapshotInterval.
	SQLite bool
}

// endpointBackfillRunners lists every session-store collector, a seam for tests. Each one runs with
// the same defaults its own sync command uses for the endpoint's mode, and reads its store only
// through internal/sessionfs, so a system sweep is confined to the console user's home; a new
// collector joins the list in sessionfs's collectors_test.go too.
var endpointBackfillRunners = func() []endpointBackfillRunner {
	return []endpointBackfillRunner{
		{Name: "Claude Code", Sync: "beacon endpoint claude sync", State: func(m bool) []string { return []string{resolveClaudeStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := claudesession.CollectOnce(claudesession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveClaudeStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Codex CLI", Sync: "beacon endpoint codex sync", State: func(m bool) []string { return []string{resolveCodexStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := codexsession.CollectOnce(codexsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveCodexStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Cursor", Sync: "beacon endpoint cursor sync", State: func(m bool) []string { return []string{resolveCursorStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := cursorsession.CollectOnce(cursorsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveCursorStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}, SQLite: true},
		{Name: "OpenCode", Sync: "beacon endpoint opencode sync", State: func(m bool) []string { return []string{resolveOpenCodeStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := opencodesession.CollectOnce(opencodesession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveOpenCodeStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}, SQLite: true},
		{Name: "GitHub Copilot CLI", Sync: "beacon endpoint copilot sync", State: func(m bool) []string { return []string{resolveCopilotStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := copilotsession.CollectOnce(copilotsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveCopilotStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Cline", Sync: "beacon endpoint cline sync", State: func(m bool) []string { return []string{resolveClineStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := clinesession.CollectOnce(clinesession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveClineStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Factory Droid", Sync: "beacon endpoint factory sync", State: func(m bool) []string { return []string{resolveFactoryStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := factorysession.CollectOnce(factorysession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveFactoryStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Pi", Sync: "beacon endpoint pi sync", State: func(m bool) []string { return []string{resolvePiStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := pisession.CollectOnce(pisession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolvePiStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Prime Agent", Sync: "beacon endpoint prime sync", State: func(m bool) []string { return []string{resolvePrimeStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := primesession.CollectOnce(primesession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolvePrimeStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Hermes Agent", Sync: "beacon endpoint hermes sync", State: func(m bool) []string {
			return []string{resolveHermesStatePath("", "", m), legacyHermesStatePath("", "", m)}
		}, Run: func(r endpointBackfillRun) (int, error) {
			s, err := hermessession.CollectOnce(hermessession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveHermesStatePath("", "", r.UserMode), LegacyStatePath: legacyHermesStatePath("", "", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}, SQLite: true},
		{Name: "Devin CLI", Sync: "beacon endpoint devin sync", State: func(m bool) []string { return []string{resolveEndpointDevinStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := devinsession.CollectOnce(devinsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveEndpointDevinStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget, LiveCoverage: endpointDevinLiveCoverage(r.LogPath)})
			return s.EventsEmitted, err
		}, SQLite: true},
		{Name: "DeepSeek Harness", Sync: "beacon endpoint dsh sync", State: func(m bool) []string { return []string{resolveDshStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := dshsession.CollectOnce(dshsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveDshStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "fx", Sync: "beacon endpoint fx sync", State: func(m bool) []string { return []string{resolveFxStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := fxsession.CollectOnce(fxsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveFxStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Grok Build", Sync: "beacon endpoint grok sync", State: func(m bool) []string { return []string{resolveGrokStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := groksession.CollectOnce(groksession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveGrokStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
		{Name: "Harness²", Sync: "beacon endpoint harness2 sync", State: func(m bool) []string { return []string{resolveHarness2StatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			opts := harness2session.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveHarness2StatePath("", r.UserMode), Since: r.Since, Budget: r.Budget}
			opts.ExtraRoots = harness2BackfillExtraRoots(r.UserMode)
			s, err := harness2session.CollectOnce(opts)
			return s.EventsEmitted, err
		}},
		{Name: "OpenClaw Gateway", Sync: "beacon endpoint integrations openclaw sessions sync", State: func(m bool) []string { return []string{resolveOpenClawSessionsStatePath("", m)} }, Run: func(r endpointBackfillRun) (int, error) {
			s, err := openclawsession.CollectOnce(openclawsession.CollectOptions{Write: true, UserMode: r.UserMode, LogPath: r.LogPath, StatePath: resolveOpenClawSessionsStatePath("", r.UserMode), Since: r.Since, Budget: r.Budget})
			return s.EventsEmitted, err
		}},
	}
}

// harness2BackfillExtraRoots is the runs directories a backfill reads beyond its own cursor's. A
// system backfill keeps its cursor beside the system log, but a --runs-dir the console user named
// was remembered in their own cursor file, which the home the backfill runs under names; without it
// a checkout outside ~/harness2 would be read by the user's syncs and never by the system install.
func harness2BackfillExtraRoots(userMode bool) []string {
	if userMode {
		return nil
	}
	return harness2session.UserRememberedRoots(harness2session.DefaultStatePath())
}

// endpointBackfillScratchDir is where a system backfill copies SQLite stores it reads as root: under
// the root-owned system base directory, on disk, rather than a /tmp that is often memory-backed. A
// seam for tests.
var endpointBackfillScratchDir = func() string {
	return filepath.Join(endpointconfig.BaseDir(false), "state", "backfill-snapshots")
}

// secureBackfillScratchDir makes the scratch directory readable by SYSTEM and Administrators alone
// before a system backfill copies anything into it. A seam for tests.
var secureBackfillScratchDir = sysacl.SecureSecretDir

// endpointBackfillNow is time.Now, a seam for tests.
var endpointBackfillNow = time.Now

// endpointBackfillRuntimeResult is one runtime's part of a backfill.
type endpointBackfillRuntimeResult struct {
	Name    string
	Sync    string
	Events  int
	Limited bool
	// RetentionLimited says the runtime stopped before log rotation would discard what it had
	// written, rather than at its share of the size budget.
	RetentionLimited bool
	Err              error
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
	// Floored is the earliest floor a skipped backfill left that limited a runtime in this sweep,
	// or zero.
	Floored time.Time
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
	return runEndpointBackfillLocked(logPath, userMode, endpointSweepLockWait, sweepBackfill)
}

// endpointSweepKind is who runs a sweep, which decides the window it reads and how it treats the
// session sync floor and SQLite copies.
type endpointSweepKind int

const (
	// sweepBackfill is install or connect: the whole window, and it lifts the session sync floor.
	sweepBackfill endpointSweepKind = iota
	// sweepManualSync is `beacon endpoint sessions sync` run by hand: it honours the floor.
	sweepManualSync
	// sweepScheduledSync is the scheduled job: it honours the floor and, confined, copies each
	// SQLite store at most every endpointSnapshotInterval.
	sweepScheduledSync
)

// runEndpointBackfillLocked is runEndpointBackfill with an explicit wait for the sweep lock. Every
// sweep (install, connect, `beacon endpoint sessions sync` and its scheduled job) and every
// per-runtime `beacon endpoint <runtime> sync` that writes (collectUnderSweepLock) holds one
// exclusive lock per mode for its whole run, because each collector loads its cursors at the start
// and saves them at the end: two sweeps running together would both start from the same cursors
// and append the same records twice. A sweep that waits for another starts from the cursors that
// one saved and reads only what is new. wait <= 0 does not wait: the scheduled job skips a run
// while another sweep holds the lock, since the next run picks up whatever it leaves.
//
// A session sync (manual or scheduled) reads no session older than the floor a skipped backfill
// recorded (recordSkippedBackfill). An install or connect backfill reads the whole window, so it
// lifts that floor.
func runEndpointBackfillLocked(logPath string, userMode bool, wait time.Duration, kind endpointSweepKind) endpointBackfillResult {
	release, err := acquireEndpointSweepLock(userMode, wait)
	if err != nil {
		return endpointBackfillResult{Skipped: err.Error()}
	}
	defer release()
	sweep := endpointSweep{ThrottleSnapshots: kind == sweepScheduledSync && !userMode}
	if kind == sweepBackfill {
		_ = os.Remove(sessionSyncFloorPath(userMode))
	} else {
		sweep.Floors = readSessionSyncFloors(userMode)
	}
	return runEndpointBackfillUnlocked(logPath, userMode, sweep)
}

// endpointSweep is what a sweep reads beyond the window: for a runtime with a floor, no session
// older than it, and, with ThrottleSnapshots, a SQLite store only if its private copy is due.
type endpointSweep struct {
	Floors            map[string]time.Time
	ThrottleSnapshots bool
}

// endpointSnapshotInterval is how often the scheduled system session sync copies a SQLite store
// (Cursor's state.vscdb, OpenCode, Devin CLI, Hermes Agent) to read it. A copy can approach
// sessionfs.MaxSnapshotBytes, and those runtimes are captured live by their hooks or plugins, so
// their stores are read hourly rather than every run. JSONL stores, which are read in place, are
// read every run.
const endpointSnapshotInterval = time.Hour

// endpointSnapshotTimesPath records when the scheduled system session sync last copied each
// runtime's SQLite store, beside the lock.
func endpointSnapshotTimesPath() string {
	return filepath.Join(filepath.Dir(endpointSweepLockPath(false)), "session-sync-snapshots.json")
}

func readEndpointSnapshotTimes() map[string]time.Time {
	times := map[string]time.Time{}
	if data, err := os.ReadFile(endpointSnapshotTimesPath()); err == nil {
		_ = json.Unmarshal(data, &times)
	}
	return times
}

func writeEndpointSnapshotTimes(times map[string]time.Time) {
	path := endpointSnapshotTimesPath()
	data, err := json.Marshal(times)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
	}
}

// sessionSyncFloorPath records, per runtime, when an install or connect skipped its backfill,
// beside the lock.
func sessionSyncFloorPath(userMode bool) string {
	return filepath.Join(filepath.Dir(endpointSweepLockPath(userMode)), "session-sync-floor.json")
}

type sessionSyncFloors struct {
	NotBefore map[string]time.Time `json:"not_before"`
}

// recordSkippedBackfill notes that an install or connect skipped its backfill (--no-backfill or
// BEACON_BACKFILL=0). A runtime no sweep has read has no cursors, so without a floor the next
// session sync would read its whole window, which the operator opted out of, and on a connected
// endpoint forward it, as lines written after the connection point. With one, a session sync reads
// only that runtime's sessions active since.
//
// The floor is per runtime and set once. A runtime whose cursors exist reads on from them, and a
// floor set then would drop every session that went idle with records its cursors have not
// reached; so a runtime with cursors gets none, and an existing floor is never moved later. A dry
// run records nothing.
func recordSkippedBackfill(out io.Writer, userMode bool) {
	if endpointOpts.dryRun {
		return
	}
	floors := readSessionSyncFloors(userMode)
	now := endpointBackfillNow().UTC()
	changed := false
	withSweepHome(userMode, func() {
		for _, runner := range endpointBackfillRunners() {
			if _, ok := floors[runner.Name]; ok || runnerHasCursors(runner, userMode) {
				continue
			}
			floors[runner.Name] = now
			changed = true
		}
	})
	if !changed {
		return
	}
	path := sessionSyncFloorPath(userMode)
	data, _ := json.Marshal(sessionSyncFloors{NotBefore: floors})
	tmp := path + ".tmp"
	err := os.MkdirAll(filepath.Dir(path), 0o700)
	if err == nil {
		err = os.WriteFile(tmp, append(data, '\n'), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintf(out, "Warning: could not record the skipped session backfill (%v); a scheduled session sync may read the last %d days.\n", err, int(endpointBackfillWindow/(24*time.Hour)))
	}
}

// withSweepHome runs fn in the home a sweep for the mode reads: the invoking user's, or for a system
// endpoint the console user's, which some cursor paths are keyed by (Hermes). With nobody logged in
// it runs in place.
func withSweepHome(userMode bool, fn func()) {
	if !userMode {
		if info, ok, err := activeConsoleUser(); err == nil && ok && strings.TrimSpace(info.HomeDir) != "" {
			_, _ = withUserHome(info.HomeDir, func() (struct{}, error) { fn(); return struct{}{}, nil })
			return
		}
	}
	fn()
}

// runnerHasCursors reports whether a sweep has saved any of runner's cursor files for the mode.
func runnerHasCursors(runner endpointBackfillRunner, userMode bool) bool {
	if runner.State == nil {
		return false
	}
	for _, path := range runner.State(userMode) {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// readSessionSyncFloors is the per-runtime floors recordSkippedBackfill left, never nil.
func readSessionSyncFloors(userMode bool) map[string]time.Time {
	var floors sessionSyncFloors
	if data, err := os.ReadFile(sessionSyncFloorPath(userMode)); err == nil {
		_ = json.Unmarshal(data, &floors)
	}
	if floors.NotBefore == nil {
		return map[string]time.Time{}
	}
	return floors.NotBefore
}

// collectUnderSweepLock runs one per-runtime sync (`beacon endpoint <runtime> sync`, once or per
// --watch tick) under the sweep lock, so it never overlaps the scheduled session sync or an install
// backfill reading the same cursor files. A run that writes nothing (--print) moves no cursors and
// takes no lock. A one-shot sync waits like install does; a watch tick passes wait 0 and reports
// the busy lock as that tick's error, and the next tick reads what the other sweep left.
func collectUnderSweepLock[O, S any](write, userMode bool, wait time.Duration, collect func(O) (S, error), opts O) (S, error) {
	if !write {
		return collect(opts)
	}
	release, err := acquireEndpointSweepLock(userMode, wait)
	if err != nil {
		var zero S
		return zero, err
	}
	defer release()
	return collect(opts)
}

// endpointSweepLockWait bounds how long an install, connect or manual sync waits for a sweep that
// is already running (normally a scheduled run).
var endpointSweepLockWait = 10 * time.Minute

// errEndpointSweepBusy is the skip reason when another sweep holds the lock.
var errEndpointSweepBusy = errors.New("another session sync is already running; its cursors carry over to the next run")

// endpointSweepLockPath is the lock file every sweep for the mode takes, beside the cursors. A
// seam for tests, which must not create the system base directory.
var endpointSweepLockPath = func(userMode bool) string {
	return filepath.Join(endpointconfig.BaseDir(userMode), "state", "session-sweep.lock")
}

func acquireEndpointSweepLock(userMode bool, wait time.Duration) (func(), error) {
	path := endpointSweepLockPath(userMode)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("could not create the session sync lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("could not open the session sync lock: %w", err)
	}
	var lock *filelock.Lock
	if wait <= 0 {
		lock, err = filelock.TryExclusive(f)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		lock, err = filelock.ExclusiveContext(ctx, f)
		cancel()
	}
	if err != nil {
		_ = f.Close()
		return nil, errEndpointSweepBusy
	}
	return func() {
		_ = lock.Release()
		_ = f.Close()
	}, nil
}

func runEndpointBackfillUnlocked(logPath string, userMode bool, sweep endpointSweep) endpointBackfillResult {
	if userMode {
		return sweepEndpointBackfill(logPath, true, sweep)
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
	// The snapshots are copies of that user's session stores. On Windows their directory would
	// inherit the machine-wide tree's grant of read to every user, which 0700 does nothing about, so
	// it is made SYSTEM and Administrators only first, and nothing is copied when that fails.
	// Elsewhere root ownership and 0700 already keep it private.
	scratch := endpointBackfillScratchDir()
	if err := secureBackfillScratchDir(scratch); err != nil {
		return endpointBackfillResult{Skipped: fmt.Sprintf("could not make the snapshot directory %s private: %v", scratch, err)}
	}
	confined, err := sessionfs.Confine(info.HomeDir, uid, sessionfs.Options{ScratchDir: scratch})
	if err != nil {
		return endpointBackfillResult{Skipped: err.Error()}
	}
	defer confined.Release()
	result, _ := withUserHome(info.HomeDir, func() (endpointBackfillResult, error) {
		return sweepEndpointBackfill(logPath, false, sweep), nil
	})
	result.Refused, result.RefusedExamples = confined.Refused()
	return result
}

// sweepEndpointBackfill reads sessions active within the window, or since sweep.Floor when that is
// later.
func sweepEndpointBackfill(logPath string, userMode bool, sweep endpointSweep) endpointBackfillResult {
	now := endpointBackfillNow()
	result := endpointBackfillResult{Since: now.Add(-endpointBackfillWindow)}
	var copied map[string]time.Time
	if sweep.ThrottleSnapshots {
		copied = readEndpointSnapshotTimes()
		defer writeEndpointSnapshotTimes(copied)
	}
	total := writer.NewBudget(endpointBackfillMaxBytes)
	for _, runner := range endpointBackfillRunners() {
		if sweep.ThrottleSnapshots && runner.SQLite {
			if last, ok := copied[runner.Name]; ok && now.Sub(last) < endpointSnapshotInterval && !last.After(now) {
				continue
			}
			copied[runner.Name] = now
		}
		since := result.Since
		if floor := sweep.Floors[runner.Name]; floor.After(since) {
			since = floor
			if result.Floored.IsZero() || floor.Before(result.Floored) {
				result.Floored = floor
			}
		}
		share := total.Sub(endpointBackfillShareBytes)
		events, err := runner.Run(endpointBackfillRun{LogPath: logPath, UserMode: userMode, Since: since, Budget: share})
		// A confined (system) sweep reads SQLite stores through private snapshots; drop each
		// runtime's before the next one runs. A no-op otherwise.
		sessionfs.DropSnapshots()
		runtime := endpointBackfillRuntimeResult{Name: runner.Name, Sync: runner.Sync, Events: events, Oversized: sessionfs.TakeOversized()}
		if sessionsync.Stopped(err) || share.Refused() {
			runtime.Limited = true
			result.Limited = true
		}
		runtime.RetentionLimited = errors.Is(err, writer.ErrRetentionWindowFull)
		if err != nil && !onlyStopped(err) {
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

// onlyStopped reports whether err says nothing but that the sweep stopped early: the budget ran
// out, or writing more would have rotated the sweep's own output out of the log. Either way the
// collector's cursors stop at what was written and the runtime's sync reads the rest.
func onlyStopped(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if !onlyStopped(e) {
				return false
			}
		}
		return true
	}
	return sessionsync.Stopped(err)
}

// reportEndpointBackfill prints what a backfill wrote: one line when it went cleanly, plus a line
// per runtime that hit the limit or failed, each naming the command that picks up from there.
func reportEndpointBackfill(out io.Writer, result endpointBackfillResult) {
	reportSessionSweep(out, "Session backfill", result)
}

// reportSessionSweep is reportEndpointBackfill under another name: `beacon endpoint sessions sync`
// and its scheduled job run the same sweep and say "Session sync".
func reportSessionSweep(out io.Writer, label string, result endpointBackfillResult) {
	window := fmt.Sprintf("from the last %d days", int(endpointBackfillWindow/(24*time.Hour)))
	if result.Skipped != "" {
		fmt.Fprintf(out, "%s skipped: %s.\n", label, result.Skipped)
		return
	}
	if result.Events == 0 && !result.Limited && len(result.Runtimes) == 0 {
		fmt.Fprintf(out, "%s: no new agent session activity %s.\n", label, window)
		reportSessionSyncFloor(out, result)
		reportEndpointBackfillRefusals(out, result)
		return
	}
	var parts []string
	for _, r := range result.Runtimes {
		if r.Events > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", r.Name, r.Events))
		}
	}
	line := fmt.Sprintf("%s: %d events %s", label, result.Events, window)
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
		case r.RetentionLimited:
			fmt.Fprintf(out, "  %s: stopped before log rotation would discard what it wrote; `%s` reads the rest.\n", r.Name, r.Sync)
		case r.Limited:
			fmt.Fprintf(out, "  %s: stopped at the backfill size limit; `%s` reads the rest.\n", r.Name, r.Sync)
		}
	}
	reportSessionSyncFloor(out, result)
	reportEndpointBackfillRefusals(out, result)
}

// reportSessionSyncFloor says when a skipped backfill limited what some runtimes read.
func reportSessionSyncFloor(out io.Writer, result endpointBackfillResult) {
	if !result.Floored.IsZero() {
		fmt.Fprintf(out, "  Runtimes not read before the skipped backfill at %s read only sessions active since.\n", result.Floored.Format(time.RFC3339))
	}
}

// endpointConnectBackfill is the backfill a connect ships, or nil when it ships none. Every connect
// ships one, first or not, user or system: the staged lines keep the deterministic event.id they
// were written with, so history the forwarder already shipped is the same event to the ingest side
// rather than a second one. synced says this process has just run the install-time backfill, so
// connect does not sweep again and spend a second budget; it still stages what the first sweep wrote.
func endpointConnectBackfill(out io.Writer, userMode bool, logPath string, synced bool) *asymptote.ConnectBackfill {
	if !endpointBackfillEnabled() {
		recordSkippedBackfill(out, userMode)
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
