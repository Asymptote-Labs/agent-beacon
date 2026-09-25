package asymptoteobserve

import (
	"fmt"
	"regexp"
)

// HandoffInfo links a session to the session it was handed off from: the one whose brief
// `beacon handoff resume` pointed it at.
type HandoffInfo struct {
	SourceHarness   string `json:"source_harness,omitempty"`
	SourceSessionID string `json:"source_session_id,omitempty"`
}

// The handoff marker is the one line `beacon handoff resume` adds to a new session's first prompt so
// that whatever records the prompt can link the new session to the old one. It is a claim made in
// prompt text, not a verified identity: anyone can type it, and a session.handoff event records that
// the marker was observed, nothing more. A harness name may carry a hyphen, as devin-cli does.
var (
	handoffMarkerPattern = regexp.MustCompile(`\[beacon-handoff from=([a-z0-9_-]{1,64}) session=([A-Za-z0-9._:\-]{1,200})\]`)
	handoffHarnessChars  = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	handoffSessionChars  = regexp.MustCompile(`^[A-Za-z0-9._:\-]{1,200}$`)
)

// HandoffMarker returns the marker naming harness's session sessionID, or "" when either value
// cannot be carried in one (an id with spaces, for instance), so a caller never writes a marker
// that parses back differently.
func HandoffMarker(harness, sessionID string) string {
	harness = NormalizeHarnessName(harness)
	if !handoffHarnessChars.MatchString(harness) || !handoffSessionChars.MatchString(sessionID) {
		return ""
	}
	return fmt.Sprintf("[beacon-handoff from=%s session=%s]", harness, sessionID)
}

// ParseHandoffMarker finds the first handoff marker in text.
func ParseHandoffMarker(text string) (HandoffInfo, bool) {
	match := handoffMarkerPattern.FindStringSubmatch(text)
	if match == nil {
		return HandoffInfo{}, false
	}
	return HandoffInfo{SourceHarness: NormalizeHarnessName(match[1]), SourceSessionID: match[2]}, true
}
