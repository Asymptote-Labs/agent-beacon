package localingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func validLine(t *testing.T, mutate func(map[string]interface{})) []byte {
	t.Helper()
	event := map[string]interface{}{
		"timestamp":      "2026-10-29T09:03:00.123456789Z",
		"sequence":       json.Number("18446744073709551615"),
		"vendor":         asymptoteobserve.Vendor,
		"product":        asymptoteobserve.Product,
		"schema_version": asymptoteobserve.SchemaVersion,
		"event":          map[string]interface{}{"action": "tool.invoked", "category": "tool", "id": "fixed-id"},
		"user":           map[string]interface{}{"name": "alice", "uid": "501"},
		"harness":        map[string]interface{}{"name": "claude_code", "collection_method": "hook"},
		"gen_ai":         map[string]interface{}{"usage": map[string]interface{}{"cost_usd": json.Number("0.10")}},
		"message":        "line one\nline two",
	}
	if mutate != nil {
		mutate(event)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decode(t *testing.T, line []byte) map[string]interface{} {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var out map[string]interface{}
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", line, err)
	}
	return out
}

func TestStampLineReplacesTheClaimedUserWithThePeer(t *testing.T) {
	line := validLine(t, nil)
	out, err := StampLine(line, Peer{UID: "1002", GID: 20, PID: 77}, func(uid string) string {
		if uid != "1002" {
			t.Fatalf("lookup asked for uid %s", uid)
		}
		return "mallory"
	})
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, out)
	user := got["user"].(map[string]interface{})
	if user["uid"] != "1002" || user["name"] != "mallory" || len(user) != 2 {
		t.Fatalf("user = %#v, want the peer's uid and resolved name only", user)
	}
}

func TestStampLineLeavesOutANameItCannotResolve(t *testing.T) {
	for name, lookup := range map[string]func(string) string{
		"nil lookup":   nil,
		"empty result": func(string) string { return "  " },
	} {
		t.Run(name, func(t *testing.T) {
			out, err := StampLine(validLine(t, nil), Peer{UID: "42"}, lookup)
			if err != nil {
				t.Fatal(err)
			}
			user := decode(t, out)["user"].(map[string]interface{})
			if _, ok := user["name"]; ok {
				t.Fatalf("user = %#v: a name the kernel did not vouch for must not be kept", user)
			}
			if user["uid"] != "42" {
				t.Fatalf("uid = %v, want 42", user["uid"])
			}
		})
	}
}

func TestStampLineAddsAUserToAnEventWithout(t *testing.T) {
	line := validLine(t, func(e map[string]interface{}) { delete(e, "user") })
	out, err := StampLine(line, Peer{UID: "0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if user := decode(t, out)["user"].(map[string]interface{}); user["uid"] != "0" {
		t.Fatalf("user = %#v", user)
	}
}

func TestStampLineKeepsEverythingElseExactly(t *testing.T) {
	line := validLine(t, nil)
	out, err := StampLine(line, Peer{UID: "501"}, func(string) string { return "alice" })
	if err != nil {
		t.Fatal(err)
	}
	// The sender claimed exactly what the kernel reports, so apart from the id, which is always
	// derived here, the line must come back byte for byte: numbers keep their spelling and
	// nothing is reordered or re-escaped.
	withoutID := func(b []byte) []byte {
		e := decode(t, b)
		delete(e["event"].(map[string]interface{}), "id")
		out, _ := json.Marshal(e)
		return out
	}
	if !bytes.Equal(withoutID(out), withoutID(line)) {
		t.Fatalf("line changed:\n in: %s\nout: %s", line, out)
	}
	if bytes.IndexByte(out, '\n') >= 0 {
		t.Fatal("stamped line contains a raw newline")
	}
	got := decode(t, out)
	if got["sequence"].(json.Number).String() != "18446744073709551615" {
		t.Fatalf("sequence = %v, lost precision", got["sequence"])
	}
}

func TestStampLineNamesAnEventThatArrivesWithoutAnID(t *testing.T) {
	line := validLine(t, func(e map[string]interface{}) {
		delete(e["event"].(map[string]interface{}), "id")
	})
	first, err := StampLine(line, Peer{UID: "501"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StampLine(line, Peer{UID: "501"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the derived id is not deterministic")
	}
	got := decode(t, first)
	id, _ := got["event"].(map[string]interface{})["id"].(string)
	if id == "" {
		t.Fatal("no event.id was derived")
	}
	// The id is derived from the stamped line without its id, exactly as the writers derive it.
	delete(got["event"].(map[string]interface{}), "id")
	unstamped, _ := json.Marshal(got)
	if want := asymptoteobserve.EventIDForLine(unstamped); id != want {
		t.Fatalf("event.id = %q, want %q", id, want)
	}
	// A different account sending the same bytes is a different event.
	other, err := StampLine(line, Peer{UID: "502"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decode(t, other)["event"].(map[string]interface{})["id"] == id {
		t.Fatal("two accounts' events share an id")
	}
}

func TestStampLineRejects(t *testing.T) {
	cases := map[string][]byte{
		"empty":               nil,
		"not json":            []byte("hello"),
		"array":               []byte(`[1,2]`),
		"null":                []byte(`null`),
		"string":              []byte(`"x"`),
		"two objects":         append(validLine(t, nil), validLine(t, nil)...),
		"trailing garbage":    append(validLine(t, nil), []byte(" x")...),
		"truncated":           validLine(t, nil)[:40],
		"wrong vendor":        validLine(t, func(e map[string]interface{}) { e["vendor"] = "evil" }),
		"wrong product":       validLine(t, func(e map[string]interface{}) { e["product"] = "other" }),
		"no schema version":   validLine(t, func(e map[string]interface{}) { delete(e, "schema_version") }),
		"event not an object": validLine(t, func(e map[string]interface{}) { e["event"] = "tool.invoked" }),
		"no action":           validLine(t, func(e map[string]interface{}) { delete(e["event"].(map[string]interface{}), "action") }),
		"blank action":        validLine(t, func(e map[string]interface{}) { e["event"].(map[string]interface{})["action"] = " " }),
		"too long":            validLine(t, func(e map[string]interface{}) { e["message"] = strings.Repeat("a", MaxLineBytes) }),
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := StampLine(line, Peer{UID: "1"}, nil)
			if !errors.Is(err, ErrRejected) {
				t.Fatalf("err = %v, out = %s; want ErrRejected", err, out)
			}
		})
	}
}

// A line must be exactly one JSON value. In particular, Decoder.More is not a
// top-level EOF check: unmatched closing delimiters must not be silently dropped.
func TestStampLineRejectsTrailingClosingDelimiters(t *testing.T) {
	for _, suffix := range []string{"}", "]", " }", "\t]", "} {", "] null"} {
		t.Run(suffix, func(t *testing.T) {
			line := append(validLine(t, nil), suffix...)
			if json.Valid(line) {
				t.Fatal("fixture unexpectedly contains valid JSON")
			}
			if out, err := StampLine(line, Peer{UID: "501"}, nil); !errors.Is(err, ErrRejected) {
				t.Fatalf("err = %v, out = %s; want ErrRejected for invalid JSON", err, out)
			}
		})
	}
}

func TestStampLineAcceptsTrailingJSONWhitespace(t *testing.T) {
	line := append(validLine(t, nil), " \t\r"...)
	if !json.Valid(line) {
		t.Fatal("fixture is not valid JSON")
	}
	if _, err := StampLine(line, Peer{UID: "501"}, nil); err != nil {
		t.Fatalf("valid trailing whitespace rejected: %v", err)
	}
}

func TestStampLineRejectsALineThatOutgrowsTheLimitWhenStamped(t *testing.T) {
	base := validLine(t, func(e map[string]interface{}) { delete(e, "user") })
	pad := MaxLineBytes - len(base) - 10
	line := validLine(t, func(e map[string]interface{}) {
		delete(e, "user")
		e["message"] = strings.Repeat("a", pad+len("line one\nline two"))
	})
	if len(line) > MaxLineBytes {
		t.Fatalf("fixture is %d bytes, want it under the limit before stamping", len(line))
	}
	_, err := StampLine(line, Peer{UID: "1"}, func(string) string { return strings.Repeat("n", 64) })
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected for a line over the limit once stamped", err)
	}
}

func TestStampLineAcceptsATrailingCarriageReturn(t *testing.T) {
	line := append(validLine(t, nil), '\r')
	out, err := StampLine(line, Peer{UID: "501"}, func(string) string { return "alice" })
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasSuffix(out, []byte{'\r'}) {
		t.Fatal("carriage return kept")
	}
}

func stampedID(t *testing.T, out []byte) string {
	t.Helper()
	id, _ := decode(t, out)["event"].(map[string]interface{})["id"].(string)
	return id
}

// A hook names its event by EventIDForLine before sending. When it claimed the account it is, the
// server keeps that id: it is the one the shared mode would have written.
func TestStampLineKeepsAnIDDerivedFromWhatIsWritten(t *testing.T) {
	line := validLine(t, func(e map[string]interface{}) {
		e["user"] = map[string]interface{}{"uid": "501", "name": "alice"}
		delete(e["event"].(map[string]interface{}), "id")
	})
	want := asymptoteobserve.EventIDForLine(line)
	withID := validLine(t, func(e map[string]interface{}) {
		e["user"] = map[string]interface{}{"uid": "501", "name": "alice"}
		e["event"].(map[string]interface{})["id"] = want
	})
	out, err := StampLine(withID, Peer{UID: "501"}, func(string) string { return "alice" })
	if err != nil {
		t.Fatal(err)
	}
	if got := stampedID(t, out); got != want {
		t.Fatalf("event.id = %s, want the sender's derived %s", got, want)
	}
}

// A call-keyed id names a tool call, not an account, so it survives stamping: the hook and the OTLP
// path give one call one id.
func TestStampLineKeepsACallKeyedID(t *testing.T) {
	call := func(e map[string]interface{}) {
		e["event"] = map[string]interface{}{"action": "tool.invoked", "category": "tool"}
		e["session"] = map[string]interface{}{"id": "s-1"}
		e["tool"] = map[string]interface{}{"name": "Bash"}
		e["command"] = map[string]interface{}{"command": "ls"}
		e["gen_ai"] = map[string]interface{}{"tool": map[string]interface{}{"name": "Bash", "call": map[string]interface{}{"id": "toolu_1"}}}
		e["user"] = map[string]interface{}{"uid": "0", "name": "root"}
	}
	plain := validLine(t, call)
	sent := asymptoteobserve.EventIDForLine(plain)
	line := validLine(t, func(e map[string]interface{}) {
		call(e)
		e["event"].(map[string]interface{})["id"] = sent
	})
	out, err := StampLine(line, Peer{UID: "501"}, func(string) string { return "alice" })
	if err != nil {
		t.Fatal(err)
	}
	stamped := decode(t, out)
	delete(stamped["event"].(map[string]interface{}), "id")
	again, _ := json.Marshal(stamped)
	if got, want := stampedID(t, out), asymptoteobserve.EventIDForLine(again); got != want {
		t.Fatalf("event.id = %s, want %s derived from what is written", got, want)
	}
}

// A line that claimed another account carries an id derived from that claim; once the account is
// corrected, the id is derived again from what is written.
func TestStampLineRederivesTheIDOfALineThatClaimedAnotherAccount(t *testing.T) {
	claimed := validLine(t, func(e map[string]interface{}) {
		e["user"] = map[string]interface{}{"uid": "0", "name": "root"}
		delete(e["event"].(map[string]interface{}), "id")
	})
	sent := asymptoteobserve.EventIDForLine(claimed)
	line := validLine(t, func(e map[string]interface{}) {
		e["user"] = map[string]interface{}{"uid": "0", "name": "root"}
		e["event"].(map[string]interface{})["id"] = sent
	})
	out, err := StampLine(line, Peer{UID: "501"}, func(string) string { return "alice" })
	if err != nil {
		t.Fatal(err)
	}
	if got := stampedID(t, out); got == sent || got == "" {
		t.Fatalf("event.id = %q; an id derived from a false claim must not be kept", got)
	}
}

// An id the sender chose itself (a session-store importer's source coordinate) stays stable for
// that account, so a re-swept record keeps its id, but cannot be another account's.
func TestStampLineScopesASenderChosenIDToItsAccount(t *testing.T) {
	line := validLine(t, func(e map[string]interface{}) {
		e["event"].(map[string]interface{})["id"] = "claude:session-1:record-42"
	})
	first, err := StampLine(line, Peer{UID: "501"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := StampLine(line, Peer{UID: "501"}, nil)
	other, _ := StampLine(line, Peer{UID: "502"}, nil)
	a, b, c := stampedID(t, first), stampedID(t, again), stampedID(t, other)
	if a == "claude:session-1:record-42" || a == "" {
		t.Fatalf("event.id = %q; a sender-chosen id must be scoped", a)
	}
	if a != b {
		t.Fatal("a scoped id is not stable across resends")
	}
	if a == c {
		t.Fatal("two accounts sending the same chosen id got the same event.id")
	}
	if a != scopedEventID("501", "claude:session-1:record-42") {
		t.Fatal("scoped id does not match its definition")
	}
	// Pinned: the derivation is a contract, and changing it would rename every scoped id already
	// written through the socket. A Unix account is named by its decimal uid, as it always was.
	if a != "1e67ad0d-6971-5cce-9a8a-565d5e4b8fa6" {
		t.Fatalf("scoped id = %s; the derivation changed", a)
	}
}

// An event at the limit, which only the id would push over, is written without one, the way the
// writers handle it, rather than lost.
func TestStampLineDropsTheIDRatherThanTheEvent(t *testing.T) {
	user := map[string]interface{}{"uid": "501", "name": "alice"}
	base := validLine(t, func(e map[string]interface{}) {
		e["user"] = user
		e["message"] = ""
		delete(e["event"].(map[string]interface{}), "id")
	})
	line := validLine(t, func(e map[string]interface{}) {
		e["user"] = user
		e["message"] = strings.Repeat("m", MaxLineBytes-len(base)-5)
		delete(e["event"].(map[string]interface{}), "id")
	})
	if len(line) > MaxLineBytes {
		t.Fatal("fixture over the limit")
	}
	out, err := StampLine(line, Peer{UID: "501"}, func(string) string { return "alice" })
	if err != nil {
		t.Fatalf("an event the id alone would push over the limit was rejected: %v", err)
	}
	if len(out) > MaxLineBytes || stampedID(t, out) != "" {
		t.Fatalf("got %d bytes, id %q; want the line within the limit and without an id", len(out), stampedID(t, out))
	}
}
