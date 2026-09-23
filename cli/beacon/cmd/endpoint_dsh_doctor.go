package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	endpointintegrations "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations"
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
// running when the hooks were installed is the other.
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

	// The hooks file is rewritten wholesale by every install and repair, so its modification time
	// is when the current hook commands took effect. Sessions before it were never going to be
	// captured live and are not evidence of anything.
	hooksInfo, err := os.Stat(hooksPath)
	if err != nil {
		check.Message = "DeepSeek Harness hooks file could not be read: " + err.Error()
		check.Evidence = "dsh_hooks_unreadable"
		return check
	}
	installedAt := hooksInfo.ModTime()

	store, err := dshsession.NewStore(dshHome)
	if err != nil {
		check.Message = "DeepSeek Harness session store could not be resolved: " + err.Error()
		check.Evidence = "dsh_sessions_unreadable"
		return check
	}
	refs, err := store.List()
	if err != nil {
		check.Message = "DeepSeek Harness session store could not be read: " + err.Error()
		check.Evidence = "dsh_sessions_unreadable"
		return check
	}

	// The newest archive is read as well as the live log: a busy machine rotates at 10 MiB, and
	// hook events that moved to runtime.jsonl.1 would otherwise make captured sessions read as
	// missed.
	var lastHook time.Time
	hookSeen := false
	for _, path := range []string{logPath, logPath + ".1"} {
		if ts, ok := endpointintegrations.LastHarnessEventByCollectionMethod(path, dshsession.Harness, asymptoteobserve.CollectionMethodHook); ok {
			hookSeen = true
			if ts.After(lastHook) {
				lastHook = ts
			}
		}
	}
	// A session is missed when it was written after the hooks took effect and after the last live
	// hook event, by more than the commit gap. Taking the later of the two lets a machine that
	// captured fine and later moved to a sandboxed mode be flagged too, not only a fresh install.
	cutoff := installedAt
	if hookSeen && lastHook.After(cutoff) {
		cutoff = lastHook.Add(dshHookCaptureGrace)
	}
	sinceInstall := 0
	missed := 0
	var newest time.Time
	for _, ref := range refs {
		modified := time.UnixMilli(ref.ModTimeUnixMS)
		if modified.Before(installedAt) {
			continue
		}
		sinceInstall++
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
