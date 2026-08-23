package asymptoteobserve

import (
	"regexp"
	"testing"
)

// callLine is one endpoint event the runtime named itself, with the parts the
// call identity is derived from left as parameters and everything else varying
// freely, so a test can say exactly which difference it is testing.
func callLine(harness, timestamp, callID, command string) []byte {
	return []byte(`{"timestamp":"` + timestamp + `","event":{"action":"command.executed"},` +
		`"harness":{"name":"` + harness + `"},"session":{"id":"s1","working_directory":"/repo"},` +
		`"command":{"command":"` + command + `"},"gen_ai":{"tool":{"call":{"id":"` + callID + `"}}},` +
		`"message":"Shell command executed"}`)
}

var uuidV5Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestEventIDForLineIsAVersion5UUID(t *testing.T) {
	for name, line := range map[string][]byte{
		"call identity":    callLine("claude_code", "2026-08-21T18:00:01Z", "toolu_1", "echo hi"),
		"content identity": []byte(`{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"session.activity"},"session":{"id":"s1"}}`),
		"unparseable":      []byte(`not json at all`),
	} {
		if id := EventIDForLine(line); !uuidV5Pattern.MatchString(id) {
			t.Errorf("%s: EventIDForLine = %q, want an RFC 4122 version 5 UUID", name, id)
		}
	}
}

// The point of the call identity: the hook writes when the tool runs and the
// collector writes when its batch flushes, so the two reports of one call agree
// on almost nothing except the call ID. They must still name one event.
func TestEventIDForLineIgnoresWhatTheTwoCapturePathsDisagreeOn(t *testing.T) {
	hook := EventIDForLine(callLine("claude_code", "2026-08-21T18:00:01Z", "toolu_1", "echo hi"))
	otlp := EventIDForLine(callLine("otel", "2026-08-21T18:00:07Z", "toolu_1", "echo hi"))
	if hook != otlp {
		t.Fatalf("one call reported twice derived two IDs: %s and %s", hook, otlp)
	}
}

func TestEventIDForLineSeparatesDistinctCalls(t *testing.T) {
	first := EventIDForLine(callLine("claude_code", "2026-08-21T18:00:01Z", "toolu_1", "echo hi"))
	for name, other := range map[string][]byte{
		"another call":   callLine("claude_code", "2026-08-21T18:00:01Z", "toolu_2", "echo hi"),
		"another target": callLine("claude_code", "2026-08-21T18:00:01Z", "toolu_1", "rm -rf /"),
	} {
		if id := EventIDForLine(other); id == first {
			t.Errorf("%s derived the same ID as the original call: %s", name, id)
		}
	}
}

// Without a call ID there is nothing to identify the event by but the event, so
// the ID has to move when any of it does -- and stay put when none of it does,
// which is what makes re-reading a log derive the IDs it already had.
func TestEventIDForLineFallsBackToTheEventItself(t *testing.T) {
	line := []byte(`{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"session.activity"},"session":{"id":"s1"},"message":"Hook executed"}`)
	later := []byte(`{"timestamp":"2026-08-21T18:00:02Z","event":{"action":"session.activity"},"session":{"id":"s1"},"message":"Hook executed"}`)
	if EventIDForLine(line) != EventIDForLine(line) {
		t.Fatal("the same line derived two IDs")
	}
	if EventIDForLine(line) == EventIDForLine(later) {
		t.Fatal("events a second apart derived one ID")
	}
}

// A content digest and a call identity must not be able to collide, because
// they mean different things: see EventIDForLine.
func TestEventIDForLineKeepsTheTwoIdentityModesApart(t *testing.T) {
	if eventUUID("call", "x") == eventUUID("content", "x") {
		t.Fatal("the call and content identity spaces overlap")
	}
}

// The namespace and the derivation are a written contract: an event's ID has to
// survive a release, or nothing downstream can address it across two of them.
func TestEventIDForLineIsStableAcrossReleases(t *testing.T) {
	const want = "244a6772-0cbe-5230-9f79-37cb62ce25ee"
	got := EventIDForLine(callLine("claude_code", "2026-08-21T18:00:01Z", "toolu_1", "echo hi"))
	if got != want {
		t.Fatalf("EventIDForLine = %s, want %s -- if this is deliberate, every ID Beacon has "+
			"ever written just changed meaning", got, want)
	}
}

// The same disagreement, at the identity layer: the hook's and the collector's
// reports of one Write must derive one event ID even though they name the
// operation differently.
func TestEventIDForLineIgnoresHowTheOperationIsNamed(t *testing.T) {
	hook := EventIDForLine([]byte(`{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/notes.md","operation":"modify"},"gen_ai":{"tool":{"call":{"id":"toolu_w"}}}}`))
	collector := EventIDForLine([]byte(`{"timestamp":"2026-08-21T18:00:07Z","event":{"action":"file.modified"},"harness":{"name":"claude_code"},"session":{"id":"s1","working_directory":"/repo"},"file":{"path":"/repo/notes.md","operation":"create"},"gen_ai":{"tool":{"call":{"id":"toolu_w"}}}}`))
	if hook != collector {
		t.Fatalf("one Write derived two IDs: %s and %s", hook, collector)
	}
}

// Provenance markers must not split the identity of one action.
//
// harness.collection_method and event.fidelity exist precisely because the two
// capture paths differ, so they differ on the two reports of one call by design:
// the hook says hook/observed, the collector says otlp and may say inferred for
// an action it classified by substring. If either reached the call identity, one
// action would derive two IDs again -- which is the failure this whole change
// exists to remove, arriving by way of an unrelated and perfectly correct
// addition. Exactly how the August dedup regression happened.
func TestEventIDForLineIgnoresProvenanceMarkers(t *testing.T) {
	hook := EventIDForLine([]byte(`{"timestamp":"2026-08-21T18:00:01Z","event":{"action":"command.executed","fidelity":"observed"},"harness":{"name":"claude_code","collection_method":"hook"},"session":{"id":"s1","working_directory":"/repo"},"command":{"command":"echo hi"},"gen_ai":{"tool":{"call":{"id":"toolu_1"}}}}`))
	collector := EventIDForLine([]byte(`{"timestamp":"2026-08-21T18:00:07Z","event":{"action":"command.executed","fidelity":"inferred"},"harness":{"name":"claude_code","collection_method":"otlp"},"session":{"id":"s1","working_directory":"/repo"},"command":{"command":"echo hi"},"gen_ai":{"tool":{"call":{"id":"toolu_1"}}}}`))
	if hook != collector {
		t.Fatalf("provenance markers split one call into two IDs: %s and %s", hook, collector)
	}
}
