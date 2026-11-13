package asymptoteobserve

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// WorkspaceSpoolRootRel is the workspace-relative directory where a hook stages an event it could
// deliver no other way: not through the collector's socket, not to the runtime log, not to the
// account's own spool. That is the position of a hook inside a sandbox a runtime builds for its
// own tools (DeepSeek Harness, #605) or a launcher builds without mounting Beacon in (#1284): the
// sandbox confines writes to the session's working directory, so the workspace is the one durable
// place both the hook, inside, and a drain, outside, can reach.
//
// Below it, each harness has a directory named by its canonical name, and each session one below
// that, holding a localingest.Spool: <workspace>/.beacon/spool/<harness>/<session>/.
// A constant, built with the platform's separator, so package init allocates nothing for it.
const WorkspaceSpoolRootRel = ".beacon" + string(filepath.Separator) + "spool"

// LegacyDSHSpoolRootRel is where DeepSeek Harness hooks staged events before every harness shared
// the workspace spool. Nothing writes it any more; drains still empty it, once, for spools an
// earlier hook left behind.
const LegacyDSHSpoolRootRel = ".beacon" + string(filepath.Separator) + "dsh-spool"

// spoolNameMax bounds a spool directory name so a hostile or malformed id cannot produce an
// unwieldy path. Real session ids are UUID-shaped; 128 is far above any of them.
const spoolNameMax = 128

// ValidSpoolName reports whether a harness name or session id is safe to use as a spool directory
// name. Both arrive from outside -- a hook payload, a session store, a directory listing -- so the
// writer and every drainer must agree on exactly which names get a spool: one containing a path
// separator, a leading dot, or anything outside the portable filename alphabet gets no spool at
// all, on every side, rather than a sanitized name another side would never look for.
func ValidSpoolName(name string) bool {
	if name == "" || len(name) > spoolNameMax || strings.HasPrefix(name, ".") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// WorkspaceSpoolDir is the spool directory for one session of one harness in one workspace. harness
// is the canonical harness name (NormalizeHarnessName). ok is false when the workspace is empty or
// either name is unsafe (ValidSpoolName); the writer then stages nothing and a drainer looks for
// nothing.
func WorkspaceSpoolDir(workspace, harness, sessionID string) (dir string, ok bool) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" || !ValidSpoolName(harness) || !ValidSpoolName(sessionID) {
		return "", false
	}
	return filepath.Join(workspace, WorkspaceSpoolRootRel, harness, sessionID), true
}

// LegacyDSHSpoolDir is the spool directory an earlier DeepSeek Harness hook staged one session's
// events in, under the same agreement as WorkspaceSpoolDir.
func LegacyDSHSpoolDir(workspace, sessionID string) (dir string, ok bool) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" || !ValidSpoolName(sessionID) {
		return "", false
	}
	return filepath.Join(workspace, LegacyDSHSpoolRootRel, sessionID), true
}

// AdmitStagedLine reports whether one line from a workspace spool is what a Beacon hook or plugin
// of harness's session sessionID stages: an endpoint event of that session and harness, captured
// live, and not an action only Beacon itself writes. Every drain applies it before a line reaches a
// runtime log.
//
// A workspace spool sits where the sandboxed agent, and anything else that can write the
// workspace, can write by design -- a checked-out repository included -- so its lines are
// untrusted input, held to the same rule as telemetry from outside (IsBeaconOnlyAction). Without
// it, a planted spool could append events for any session, or a session.share_requested that
// Beacon Cloud acts on by publishing a session. A torn line from a killed hook fails to decode and
// is refused the same way.
func AdmitStagedLine(line []byte, harness, sessionID string) bool {
	var event struct {
		Vendor  string `json:"vendor"`
		Product string `json:"product"`
		Harness struct {
			Name             string `json:"name"`
			CollectionMethod string `json:"collection_method"`
		} `json:"harness"`
		Session *struct {
			ID string `json:"id"`
		} `json:"session"`
		Event struct {
			Action string `json:"action"`
		} `json:"event"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return false
	}
	method := event.Harness.CollectionMethod
	return event.Vendor == Vendor &&
		event.Product == Product &&
		event.Session != nil && event.Session.ID == sessionID &&
		event.Harness.Name == harness &&
		(method == CollectionMethodHook || method == CollectionMethodPlugin) &&
		event.Event.Action != "" &&
		!IsBeaconOnlyAction(event.Event.Action)
}
