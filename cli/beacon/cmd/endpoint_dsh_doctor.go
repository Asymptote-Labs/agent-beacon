package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	endpointintegrations "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// dshHookCaptureGrace is how far a native session's last write may run past the last live hook
// event before the session counts as one the hooks missed.
//
// DeepSeek Harness appends to its session store when a turn's records are committed, which can be
// after the Stop hook for that turn has already run, so a session whose file is a little newer than
// the last hook event is the normal shape of a working install. Five minutes is far longer than
// that gap and far shorter than the time between two sessions an operator would notice missing.
const dshHookCaptureGrace = 5 * time.Minute

// doctorDshHookCaptureCheck is the check doctor runs, indirected so a doctor test can supply a
// result without resolving the real Harness home.
var doctorDshHookCaptureCheck = dshHookCaptureCheck

// dshHookCaptureCheck reports whether DeepSeek Harness hooks are actually writing events.
//
// It exists because nothing else can see the failure (#605). dsh runs hook commands sandboxed to
// the session workspace in every sandbox mode except danger-full-access, and its sandbox policy has
// no writable-path allow-list, so beacon-hooks cannot write the runtime log under ~/.beacon. The
// hook exits 0 on purpose (a non-zero exit is a block on this contract), the harness reports
// nothing, and `harness_observed` stays green because `beacon endpoint dsh sync` fills the same log
// with poll events under the same harness name.
//
// Doctor cannot reproduce the sandbox -- running the hook command from here runs it outside dsh,
// where it succeeds -- so the check reads the evidence instead: native sessions DeepSeek wrote after
// the hooks were installed, set against the last event whose harness.collection_method is `hook`.
// Sessions with no live hook event near them are sessions the hooks did not capture, whatever the
// cause; the sandbox is the common one and the message leads with it, and a dsh that was already
// running when the hooks were installed is the other. A session whose workspace spool still holds
// staged hook events is neither: capture worked and only the sweep is missing, so it gets its own
// finding (`dsh_spool_pending_drain`) with the sync as its remedy instead of counting as a miss.
func dshHookCaptureCheck(logPath string) (diagnostics.Check, bool) {
	status := endpointhooks.DshHookStatus(endpointhooks.DshOptions{Level: endpointhooks.LevelUser})
	if !status.Installed || status.HooksPath == "" {
		return diagnostics.Check{}, false
	}
	store, err := dshsession.NewStore("")
	if err != nil {
		return diagnostics.Check{}, false
	}
	return dshHookCaptureCheckAt(logPath, status.HooksPath, store.DSHHome), true
}

// dshHookCaptureCheckAt is dshHookCaptureCheck with its inputs resolved, so it can be tested
// against a temporary Harness home and runtime log.
func dshHookCaptureCheckAt(logPath, hooksPath, dshHome string) diagnostics.Check {
	check := diagnostics.Check{
		Name:     "dsh_hook_capture",
		Target:   dshsession.Harness,
		Status:   diagnostics.StatusOK,
		Severity: diagnostics.SeverityInfo,
	}

	// Evidence that cannot be read is a warning, not a pass. Doctor's text output prints only
	// failures and warnings, so an ok here would tell the operator hook capture had been checked
	// when it had not been.
	unreadable := func(evidence, what string, err error) diagnostics.Check {
		check.Status = diagnostics.StatusWarn
		check.Severity = diagnostics.SeverityLow
		check.Evidence = evidence
		check.Message = "could not verify DeepSeek Harness hook capture: " + what + " could not be read: " + err.Error()
		check.Action = "check that this user can read " + what + ", then run beacon endpoint doctor again"
		return check
	}

	// The hooks file is rewritten wholesale by every install and repair, so its modification time
	// is when the current hook commands took effect. Sessions before it were never going to be
	// captured live and are not evidence of anything.
	hooksInfo, err := os.Stat(hooksPath)
	if err != nil {
		return unreadable("dsh_hooks_unreadable", "the hooks file "+hooksPath, err)
	}
	installedAt := hooksInfo.ModTime()

	store, err := dshsession.NewStore(dshHome)
	if err != nil {
		return unreadable("dsh_sessions_unreadable", "the DeepSeek Harness session store", err)
	}
	refs, err := store.List()
	if err != nil {
		return unreadable("dsh_sessions_unreadable", "the DeepSeek Harness session store "+store.SessionsDir, err)
	}

	// Every retained archive is read, newest first, stopping at the first that holds a hook event:
	// archives are strictly older than the file before them, so that event is the latest one. Poll
	// backfill writes into the same log, so on a busy machine the last hook event can sit several
	// rotations back while capture is working fine.
	//
	var lastHook time.Time
	hookSeen := false
	paths := writer.RetainedLogPaths(logPath)
	for _, path := range paths {
		if ts, ok := endpointintegrations.LastHarnessEventByCollectionMethod(path, dshsession.Harness, asymptoteobserve.CollectionMethodHook); ok {
			hookSeen = true
			lastHook = ts
			break
		}
	}
	// Once the oldest archive slot is occupied, rotation has started discarding history, and a
	// session last written before the oldest retained event has no evidence either way: its hook
	// events, if it had any, are gone. Such sessions are not counted. Before that slot fills,
	// nothing has been discarded and every session since install is fair evidence.
	var oldestRetained time.Time
	if len(paths) > 0 {
		if first, ok := endpointintegrations.FirstEventTime(paths[len(paths)-1]); ok {
			oldestRetained = first
		}
	}

	// A session is missed when it was written after the hooks took effect and after the last live
	// hook event, by more than the commit gap. Taking the later of the two lets a machine that
	// captured fine and later moved to a sandboxed mode be flagged too, not only a fresh install.
	//
	// A session whose workspace still holds a spool is not a miss: its hook events exist, staged
	// where the sandbox allowed the hook to write them (#605), and the next `beacon endpoint dsh
	// sync` drains them into the log. Reporting it as uncaptured would call working capture broken
	// and send the operator to change the sandbox mode for nothing; the pending drain is its own
	// finding, with the same remedy.
	cutoff := installedAt
	if hookSeen && lastHook.After(cutoff) {
		cutoff = lastHook.Add(dshHookCaptureGrace)
	}
	sinceInstall := 0
	missed := 0
	pendingDrain := 0
	var newest time.Time
	for _, ref := range refs {
		modified := time.UnixMilli(ref.ModTimeUnixMS)
		if modified.Before(installedAt) {
			// Sessions before the current hooks took effect were never going to be captured
			// live, spool or no spool; they are not evidence of anything.
			continue
		}
		pending := ref.Meta != nil && strings.TrimSpace(ref.Meta.CWD) != "" &&
			asymptoteobserve.DSHSpoolPendingBytes(ref.Meta.CWD, ref.ID) > 0
		if !pending && modified.Before(oldestRetained) {
			continue
		}
		sinceInstall++
		if pending {
			pendingDrain++
			continue
		}
		if modified.After(cutoff) {
			missed++
			if modified.After(newest) {
				newest = modified
			}
		}
	}

	switch {
	case sinceInstall == 0:
		check.Message = "no DeepSeek Harness session has run since the hooks were installed"
		check.Evidence = "dsh_no_sessions_since_install"
	case missed == 0 && pendingDrain > 0:
		check.Status = diagnostics.StatusWarn
		check.Severity = diagnostics.SeverityLow
		check.Evidence = "dsh_spool_pending_drain"
		check.Message = fmt.Sprintf(
			"%d DeepSeek Harness session(s) have hook events staged in their workspace spool; capture is working, but the events are not in %s yet",
			pendingDrain, logPath)
		check.Action = "run `beacon endpoint dsh sync` to drain the staged events into the runtime log"
	case missed == 0:
		check.Message = "live DeepSeek Harness hook events are being recorded"
		check.Evidence = "dsh_hook_events_observed"
	default:
		since := "since the hooks were installed"
		if hookSeen && lastHook.After(installedAt) {
			since = "since " + lastHook.UTC().Format(time.RFC3339)
		}
		check.Status = diagnostics.StatusWarn
		check.Severity = diagnostics.SeverityMedium
		check.Evidence = "dsh_sessions_without_hook_events"
		check.Message = fmt.Sprintf(
			"%d DeepSeek Harness session(s) ran without a live hook event (newest %s; no hook event %s). "+
				"The usual cause is dsh's sandbox: it runs hooks confined to the session workspace unless the sandbox "+
				"mode is danger-full-access, so beacon-hooks cannot write %s and records nothing while still exiting 0",
			missed, newest.UTC().Format(time.RFC3339), since, logPath)
		check.Action = "run `beacon endpoint dsh sync` to backfill those sessions from DeepSeek's session store; " +
			"for live hook capture, run dsh in danger-full-access mode, or restart dsh if it was already running when the hooks were installed"
	}
	return check
}
