package cmd

import (
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// handoffLinkEvent is the session.handoff event a prompt implies when it carries the marker
// `beacon handoff resume` puts in a new session's first prompt. fields are the prompt event's own
// fields; the link keeps the session, workspace and harness from them and drops the prompt text,
// which the prompt event already records.
//
// The event is observed: the marker was in the prompt. It is not verified, since anyone can type
// the marker; it records the claim.
func handoffLinkEvent(fields map[string]interface{}, prompt string) (normalizedEvent, bool) {
	info, ok := asymptoteobserve.ParseHandoffMarker(prompt)
	if !ok {
		return normalizedEvent{}, false
	}
	linked := cloneFields(fields)
	for _, key := range []string{"prompt", "gen_ai", "content"} {
		delete(linked, key)
	}
	linked["handoff"] = map[string]interface{}{
		"source_harness":    info.SourceHarness,
		"source_session_id": info.SourceSessionID,
	}
	return normalizedEvent{
		action:   "session.handoff",
		category: "session",
		severity: "info",
		message:  "Session continued from a " + info.SourceHarness + " session",
		fields:   linked,
	}, true
}
