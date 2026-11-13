package dshsession

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/workspacespool"
)

// spoolSession is ref's workspace spool session, found through the working directory the session
// store recorded for it.
func spoolSession(ref SessionRef) (workspacespool.Session, bool) {
	if ref.Meta == nil {
		return workspacespool.Session{}, false
	}
	return workspacespool.Session{Workspace: ref.Meta.CWD, Harness: Harness, ID: ref.ID}, true
}

// SpoolPendingBytes is how many bytes of hook events ref's workspace spools hold, waiting for a
// sweep to drain them. Zero when nothing is staged.
func SpoolPendingBytes(ref SessionRef) int64 {
	s, ok := spoolSession(ref)
	if !ok {
		return 0
	}
	return workspacespool.PendingBytes(s)
}

// drainSessionSpool moves one session's staged hook events out of its workspace spool and into
// the runtime log, counting them in summary.
//
// DeepSeek Harness runs hook commands inside the session sandbox (#605), which cannot write
// ~/.beacon but can write the session workspace, so its hooks stage there. The session store names
// the workspace, so the dsh sweep drains its sessions' spools itself, before it maps the store.
//
// Events go through the sweep's own sink, so the budget and retention guard bound them like every
// mapped event, and a refusal by either stops the sweep with the rest left staged. A system
// backfill leaves spools alone (workspacespool.DrainSession); the user's own sync drains them.
func drainSessionSpool(ref SessionRef, opts CollectOptions, summary *Summary) error {
	if !opts.Write || opts.Print {
		return nil
	}
	s, ok := spoolSession(ref)
	if !ok {
		return nil
	}
	var drained workspacespool.Summary
	err := workspacespool.DrainSession(s, func(event schema.Event) error { return emitEvent(event, opts) }, &drained)
	summary.SpoolEvents += drained.Events
	summary.SpoolRejected += drained.Rejected
	return err
}
