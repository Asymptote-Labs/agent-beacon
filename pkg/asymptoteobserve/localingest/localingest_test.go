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
	out, err := StampLine(line, Peer{UID: 1002, GID: 20, PID: 77}, func(uid uint32) string {
		if uid != 1002 {
			t.Fatalf("lookup asked for uid %d", uid)
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
	for name, lookup := range map[string]func(uint32) string{
		"nil lookup":   nil,
		"empty result": func(uint32) string { return "  " },
	} {
		t.Run(name, func(t *testing.T) {
			out, err := StampLine(validLine(t, nil), Peer{UID: 42}, lookup)
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
	out, err := StampLine(line, Peer{UID: 0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if user := decode(t, out)["user"].(map[string]interface{}); user["uid"] != "0" {
		t.Fatalf("user = %#v", user)
	}
}

func TestStampLineKeepsEverythingElseExactly(t *testing.T) {
	line := validLine(t, nil)
	out, err := StampLine(line, Peer{UID: 501}, func(uint32) string { return "alice" })
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
	first, err := StampLine(line, Peer{UID: 501}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StampLine(line, Peer{UID: 501}, nil)
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
	other, err := StampLine(line, Peer{UID: 502}, nil)
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
			out, err := StampLine(line, Peer{UID: 1}, nil)
			if !errors.Is(err, ErrRejected) {
				t.Fatalf("err = %v, out = %s; want ErrRejected", err, out)
			}
		})
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
	_, err := StampLine(line, Peer{UID: 1}, func(uint32) string { return strings.Repeat("n", 64) })
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected for a line over the limit once stamped", err)
	}
}

func TestStampLineAcceptsATrailingCarriageReturn(t *testing.T) {
	line := append(validLine(t, nil), '\r')
	out, err := StampLine(line, Peer{UID: 501}, func(uint32) string { return "alice" })
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasSuffix(out, []byte{'\r'}) {
		t.Fatal("carriage return kept")
	}
}

func TestStampLineNeverKeepsTheSendersEventID(t *testing.T) {
	line := validLine(t, func(e map[string]interface{}) {
		e["event"].(map[string]interface{})["id"] = "someone-elses-event-id"
	})
	out, err := StampLine(line, Peer{UID: 501}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id := decode(t, out)["event"].(map[string]interface{})["id"]; id == "someone-elses-event-id" || id == "" {
		t.Fatalf("event.id = %v; a sender must not choose it", id)
	}
}
