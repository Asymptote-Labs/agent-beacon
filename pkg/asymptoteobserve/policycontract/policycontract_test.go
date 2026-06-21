package policycontract

import (
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

//go:embed schema.json
var schemaJSON []byte

func TestSchemaIsValidJSON(t *testing.T) {
	if !json.Valid(schemaJSON) {
		t.Fatal("schema.json is not valid JSON")
	}
}

func TestRequestRoundTrip(t *testing.T) {
	req := Request{
		Version:  Version,
		Phase:    PhasePreTool,
		Platform: "claude",
		Event: asymptoteobserve.Event{
			Event:   asymptoteobserve.EventInfo{Action: "command.executed", Category: "command"},
			Command: &asymptoteobserve.CommandInfo{Command: "claude --dangerously-skip-permissions"},
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Request
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != Version || got.Phase != PhasePreTool || got.Platform != "claude" {
		t.Fatalf("envelope round trip mismatch: %+v", got)
	}
	if got.Event.Command == nil || got.Event.Command.Command != req.Event.Command.Command {
		t.Fatalf("event round trip mismatch: %+v", got.Event)
	}
}

func TestResponseDenied(t *testing.T) {
	cases := []struct {
		raw  string
		deny bool
	}{
		{`{"decision":"deny","reason":"nope","rule_id":"r1","severity":"high","mode":"enforce"}`, true},
		{`{"decision":"allow"}`, false},
		{`{"decision":""}`, false},
		{`{"decision":"DENY"}`, false}, // case-sensitive: only exact "deny" denies
		{`{}`, false},
	}
	for _, c := range cases {
		var resp Response
		if err := json.Unmarshal([]byte(c.raw), &resp); err != nil {
			t.Fatalf("unmarshal %q: %v", c.raw, err)
		}
		if got := resp.Denied(); got != c.deny {
			t.Fatalf("Denied(%q) = %v, want %v", c.raw, got, c.deny)
		}
	}
}
