package policycontract

import (
	_ "embed"
	"encoding/json"
	"testing"
)

//go:embed schema.json
var schemaJSON []byte

func TestSchemaIsValidJSON(t *testing.T) {
	if !json.Valid(schemaJSON) {
		t.Fatal("schema.json is not valid JSON")
	}
}

func TestResponseDenied(t *testing.T) {
	cases := []struct {
		raw  string
		deny bool
	}{
		{`{"decision":"deny","reason":"nope","rule_id":"r1","severity":"high","mode":"enforce"}`, true},
		{`{"decision":"allow"}`, false},
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
