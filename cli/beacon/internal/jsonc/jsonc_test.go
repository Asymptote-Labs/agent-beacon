package jsonc

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecodeJSONCMatchesStrictJSONWithoutComments(t *testing.T) {
	doc := `{
  // a line comment with "quotes" and {braces}
  "url": "https://host//not-a-comment/*nor-this*/",
  /* a block
     comment */ "n": 1.50,
  "escaped": "say \"hi\" \\ // still a string",
  "list": [1, 2, 3,],
  "nested": {"a": true,},
}`
	got, err := Decode(doc)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Decode(`{"url":"https://host//not-a-comment/*nor-this*/","n":1.50,"escaped":"say \"hi\" \\ // still a string","list":[1,2,3],"nested":{"a":true}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestParseJSONCRejectsMalformedDocuments(t *testing.T) {
	for _, doc := range []string{
		`{"a": }`,
		`{"a": 1 "b": 2}`,
		`{"a": 1`,
		`{"a": "unterminated}`,
		`{/* unterminated comment "a": 1}`,
		`{"a": nope}`,
		`{a: 1}`,
		`{"a": 1} trailing`,
		`{,}`,
	} {
		if _, err := Decode(doc); err == nil {
			t.Errorf("Decode(%q) accepted a malformed document", doc)
		}
	}
}

func TestDecodeJSONCTreatsBlankAsEmptyObjectAndRejectsNonObjects(t *testing.T) {
	for _, doc := range []string{"", "  \n", "// only a comment\n"} {
		got, err := Decode(doc)
		if err != nil || len(got) != 0 {
			t.Errorf("Decode(%q) = %v, %v; want an empty object", doc, got, err)
		}
	}
	if _, err := Decode(`[1, 2]`); err == nil {
		t.Error("a top-level array was accepted as a config object")
	}
}

const entryName = "beacon-managed"

var beaconEntry = Ordered{{"url", "https://mcp.example.test"}, {"type", "http"}}

// Every insertion must be undone exactly by the matching removal, whatever the formatting of the
// document it went into. This is what makes connect → disconnect leave a file as it found it even
// when the file changed in between (so the backup cannot simply be restored).
func TestInsertThenRemoveJSONMemberIsTheIdentity(t *testing.T) {
	docs := map[string]string{
		"empty object":            "{}",
		"empty object newline":    "{}\n",
		"two-space":               "{\n  \"theme\": \"dark\"\n}\n",
		"four-space":              "{\n    \"theme\": \"dark\",\n    \"mcpServers\": {\n        \"other\": {\"url\": \"https://o\"}\n    }\n}\n",
		"tabs":                    "{\n\t\"mcpServers\": {\n\t\t\"other\": {\n\t\t\t\"command\": \"x\"\n\t\t}\n\t}\n}\n",
		"crlf":                    "{\r\n  \"mcpServers\": {\r\n    \"other\": {}\r\n  }\r\n}\r\n",
		"compact":                 `{"mcpServers":{"other":{"url":"https://o"}}}`,
		"compact spaced":          `{"mcpServers": {"other": {"url": "https://o"}, "more": {}}}`,
		"empty container":         "{\n  \"mcpServers\": {}\n}\n",
		"comment-only container":  "{\n  \"mcpServers\": {\n    // add servers here\n  }\n}\n",
		"trailing comma":          "{\n  \"mcpServers\": {\n    \"other\": {},\n  },\n}\n",
		"trailing comment":        "{\n  \"a\": 1 // why\n}\n",
		"comment before close":    "{\n  \"mcpServers\": {\n    \"other\": {}\n    // end\n  }\n}\n",
		"no final newline":        "{\n  \"a\": 1\n}",
		"unicode and escapes":     "{\n  \"名前\": \"\\u00e9\\n\",\n  \"mcpServers\": {\"x\": {}}\n}\n",
		"container not last":      "{\n  \"mcpServers\": {\n    \"other\": {}\n  },\n  \"after\": [1, 2]\n}\n",
		"deeply nested elsewhere": "{\n  \"a\": {\"b\": {\"c\": [{\"d\": {}}]}}\n}\n",
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			inserted, created, err := InsertMember(doc, []string{"mcpServers"}, entryName, beaconEntry)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := Decode(doc)
			after, err := Decode(inserted)
			if err != nil {
				t.Fatalf("insertion produced invalid JSONC: %v\n%s", err, inserted)
			}
			servers, _ := before["mcpServers"].(map[string]any)
			if servers == nil {
				servers = map[string]any{}
				before["mcpServers"] = servers
			}
			servers[entryName] = Normalize(beaconEntry)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("insertion changed more than the entry:\n%s", inserted)
			}
			removed, err := RemoveMember(inserted, []string{"mcpServers"}, entryName, created)
			if err != nil {
				t.Fatal(err)
			}
			if removed != doc {
				t.Fatalf("remove did not undo insert\n doc: %q\n ins: %q\n got: %q", doc, inserted, removed)
			}
		})
	}
}

func TestInsertJSONMemberCopiesTheDocumentsFormatting(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{
			"four-space indentation",
			"{\n    \"mcpServers\": {\n        \"other\": {}\n    }\n}\n",
			"{\n    \"mcpServers\": {\n        \"other\": {},\n        \"beacon-managed\": {\n            \"url\": \"https://mcp.example.test\",\n            \"type\": \"http\"\n        }\n    }\n}\n",
		},
		{
			"single-line document stays on one line",
			`{"mcpServers":{"other":{}}}`,
			`{"mcpServers":{"other":{},"beacon-managed":{"url":"https://mcp.example.test","type":"http"}}}`,
		},
		{
			"CRLF line endings",
			"{\r\n  \"a\": 1\r\n}\r\n",
			"{\r\n  \"a\": 1,\r\n  \"mcpServers\": {\r\n    \"beacon-managed\": {\r\n      \"url\": \"https://mcp.example.test\",\r\n      \"type\": \"http\"\r\n    }\r\n  }\r\n}\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := InsertMember(tc.doc, []string{"mcpServers"}, entryName, beaconEntry)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestJSONEditRefusesShapesItCannotEditSafely(t *testing.T) {
	cases := map[string]string{
		"duplicate container":   `{"mcpServers": {}, "mcpServers": {}}`,
		"container not object":  `{"mcpServers": []}`,
		"container is a string": `{"mcpServers": "none"}`,
		"root is an array":      `[]`,
	}
	for name, doc := range cases {
		if _, _, err := InsertMember(doc, []string{"mcpServers"}, entryName, beaconEntry); err == nil {
			t.Errorf("%s: insert accepted %q", name, doc)
		}
	}
	if _, _, err := InsertMember(`{"mcpServers": {"beacon-managed": {}}}`, []string{"mcpServers"}, entryName, beaconEntry); err == nil {
		t.Error("insert over an existing entry was accepted")
	}
	if _, err := RemoveMember(`{"mcpServers": {"beacon-managed": {}, "beacon-managed": {}}}`, []string{"mcpServers"}, entryName, 0); err == nil {
		t.Error("removal of a duplicated key was accepted")
	}
}

func TestRemoveJSONMemberHandlesEntriesItDidNotPosition(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"first of several", `{"s": {"beacon-managed": {}, "b": 1}}`, `{"s": {"b": 1}}`},
		{"middle", `{"s": {"a": 1, "beacon-managed": {}, "b": 1}}`, `{"s": {"a": 1, "b": 1}}`},
		{"only, with trailing comma", "{\"s\": {\"beacon-managed\": {},}}", `{"s": {}}`},
		{"absent", `{"s": {"a": 1}}`, `{"s": {"a": 1}}`},
		{"no container", `{"a": 1}`, `{"a": 1}`},
	}
	for _, tc := range cases {
		got, err := RemoveMember(tc.doc, []string{"s"}, entryName, 0)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestPruneRemovesOnlyContainersLeftEmpty(t *testing.T) {
	doc := "{\n  \"a\": 1\n}\n"
	inserted, created, err := InsertMember(doc, []string{"mcp", "servers"}, entryName, beaconEntry)
	if err != nil || created != 2 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	// Someone else adds a server next to Beacon's: the container is no longer Beacon's to remove.
	shared, _, err := InsertMember(inserted, []string{"mcp", "servers"}, "theirs", Ordered{{"url", "https://t"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := RemoveMember(shared, []string{"mcp", "servers"}, entryName, created)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"theirs"`) || strings.Contains(got, entryName) {
		t.Fatalf("prune removed a container that still held another server:\n%s", got)
	}
}

// SetMember replaces a value where it stands, so the comments, order and layout around it survive,
// and adds a missing member the way InsertMember does.
func TestSetMemberReplacesInPlaceOrAppends(t *testing.T) {
	cases := []struct {
		name, doc, key string
		value          any
		want           string
	}{
		{
			"replace keeps comments and position",
			"{\n    // mine\n    \"a\": \"old\", /* why */\n    \"b\": 1,\n}\n",
			"a", "new",
			"{\n    // mine\n    \"a\": \"new\", /* why */\n    \"b\": 1,\n}\n",
		},
		{
			"replace with another type",
			`{"a": true, "b": 1}`,
			"b", false,
			`{"a": true, "b": false}`,
		},
		{
			"missing key is appended",
			"{\n    \"a\": 1 // keep\n}\n",
			"b", 8192,
			"{\n    \"a\": 1,\n    \"b\": 8192 // keep\n}\n",
		},
		{
			"blank document",
			"",
			"a", true,
			"{\n  \"a\": true\n}\n",
		},
	}
	for _, tc := range cases {
		got, err := SetMember(tc.doc, nil, tc.key, tc.value)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
	for _, doc := range []string{`{"a": 1, "a": 2}`, `[]`, `{"a": 1`} {
		if _, err := SetMember(doc, nil, "a", 3); err == nil {
			t.Errorf("SetMember accepted %q", doc)
		}
	}
}
