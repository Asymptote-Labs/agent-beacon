package dshsession

import (
	"encoding/json"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

// spoolDrainBatch is how many staged lines one Spool.Drain call hands the sweep's sink.
const spoolDrainBatch = 256

// sessionSpool is the workspace spool a sandboxed hook of ref stages events in (#605), found
// through the working directory the session store recorded for it.
func sessionSpool(ref SessionRef) (localingest.Spool, bool) {
	if ref.Meta == nil {
		return localingest.Spool{}, false
	}
	dir, ok := asymptoteobserve.DSHSpoolDir(ref.Meta.CWD, ref.ID)
	return localingest.Spool{Dir: dir}, ok
}

// SpoolPendingBytes is how many bytes of hook events ref's workspace spool holds, waiting for a
// sweep to drain them. Zero when nothing is staged. The size comes from sessionfs.Stat, so a
// confined reader never follows a user-planted path out of the user's home.
func SpoolPendingBytes(ref SessionRef) int64 {
	spool, ok := sessionSpool(ref)
	if !ok {
		return 0
	}
	info, err := sessionfs.Stat(spool.FilePath())
	if err != nil {
		return 0
	}
	return info.Size()
}

// drainSessionSpool moves one session's staged hook events out of its workspace spool and into
// the runtime log, counting them in summary.
//
// Why a spool exists: DeepSeek Harness runs hook commands inside the session sandbox (#605), which
// cannot write ~/.beacon but can write the session workspace in workspace-write mode. Nothing
// inside the sandbox can hand those events to the runtime log, so the drain happens here, outside
// the harness, on the sweep an operator or the watch loop already runs.
//
// Events go through the sweep's own sink, so the budget and retention guard bound them like every
// mapped event, and a refusal by either stops the sweep with the rest left staged. Delivery is at
// least once: a sweep killed between appending a batch and removing it from the spool appends it
// again next time, the same contract localingest.Spool keeps for a restricted-mode hook.
//
// A system backfill (sessionfs confinement active) leaves spools alone: the drain writes and
// removes files under a workspace path the session store names, which root must not do on a
// user's behalf. The user's own `beacon endpoint dsh sync` drains them.
func drainSessionSpool(ref SessionRef, opts CollectOptions, summary *Summary) error {
	if !opts.Write || opts.Print || sessionfs.Active() {
		return nil
	}
	spool, ok := sessionSpool(ref)
	if !ok {
		return nil
	}
	for {
		n, err := spool.Drain(spoolDrainBatch, func(lines [][]byte) (int, error) {
			for i, line := range lines {
				event, ok := stagedHookEvent(line, ref.ID)
				if !ok {
					summary.SpoolRejected++
					continue
				}
				if err := emitEvent(event, opts); err != nil {
					return i, err
				}
				summary.SpoolEvents++
			}
			return len(lines), nil
		})
		if err != nil || n < spoolDrainBatch {
			return err
		}
	}
}

// stagedHookEvent decodes one spooled line and admits it only if it is what a sandboxed Beacon
// hook of this session stages: an event of sessionID, from DeepSeek Harness, collected by a hook,
// and not an action only Beacon itself writes.
//
// The spool sits in a directory the sandboxed agent can write by design, so its contents are
// untrusted input, held to the same rule as telemetry from outside (IsBeaconOnlyAction). Without
// these checks anything running in the workspace could append events for any session, or a
// session.share_requested that Beacon Cloud acts on by publishing the session. A torn line from a
// killed hook fails to decode and is rejected the same way.
func stagedHookEvent(line []byte, sessionID string) (schema.Event, bool) {
	var event schema.Event
	if err := json.Unmarshal(line, &event); err != nil {
		return event, false
	}
	switch {
	case event.Session == nil || event.Session.ID != sessionID,
		event.Harness.Name != Harness,
		event.Harness.CollectionMethod != asymptoteobserve.CollectionMethodHook,
		asymptoteobserve.IsBeaconOnlyAction(event.Event.Action):
		return event, false
	}
	return event, true
}
