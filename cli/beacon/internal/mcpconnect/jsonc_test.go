package mcpconnect

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
	got, err := decodeJSONC(doc)
	if err != nil {
		t.Fatal(err)
	}
	want, err := decodeJSONC(`{"url":"https://host//not-a-comment/*nor-this*/","n":1.50,"escaped":"say \"hi\" \\ // still a string","list":[1,2,3],"nested":{"a":true}}`)
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
		if _, err := decodeJSONC(doc); err == nil {
			t.Errorf("decodeJSONC(%q) accepted a malformed document", doc)
		}
	}
}

func TestDecodeJSONCTreatsBlankAsEmptyObjectAndRejectsNonObjects(t *testing.T) {
	for _, doc := range []string{"", "  \n", "// only a comment\n"} {
		got, err := decodeJSONC(doc)
		if err != nil || len(got) != 0 {
			t.Errorf("decodeJSONC(%q) = %v, %v; want an empty object", doc, got, err)
		}
	}
	if _, err := decodeJSONC(`[1, 2]`); err == nil {
		t.Error("a top-level array was accepted as a config object")
	}
}

var beaconEntry = ordered{{"url", "https://mcp.example.test"}, {"type", "http"}}

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
			inserted, created, err := insertJSONMember(doc, []string{"mcpServers"}, ServerName, beaconEntry)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := decodeJSONC(doc)
			after, err := decodeJSONC(inserted)
			if err != nil {
				t.Fatalf("insertion produced invalid JSONC: %v\n%s", err, inserted)
			}
			setPath(before, []string{"mcpServers"}, ServerName, normalizeJSON(beaconEntry))
			if !reflect.DeepEqual(normalizeValue(before), normalizeValue(after)) {
				t.Fatalf("insertion changed more than the entry:\n%s", inserted)
			}
			removed, err := removeJSONMember(inserted, []string{"mcpServers"}, ServerName, created)
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
			got, _, err := insertJSONMember(tc.doc, []string{"mcpServers"}, ServerName, beaconEntry)
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
		if _, _, err := insertJSONMember(doc, []string{"mcpServers"}, ServerName, beaconEntry); err == nil {
			t.Errorf("%s: insert accepted %q", name, doc)
		}
	}
	if _, _, err := insertJSONMember(`{"mcpServers": {"beacon-managed": {}}}`, []string{"mcpServers"}, ServerName, beaconEntry); err == nil {
		t.Error("insert over an existing entry was accepted")
	}
	if _, err := removeJSONMember(`{"mcpServers": {"beacon-managed": {}, "beacon-managed": {}}}`, []string{"mcpServers"}, ServerName, 0); err == nil {
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
		got, err := removeJSONMember(tc.doc, []string{"s"}, ServerName, 0)
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
	inserted, created, err := insertJSONMember(doc, []string{"mcp", "servers"}, ServerName, beaconEntry)
	if err != nil || created != 2 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	// Someone else adds a server next to Beacon's: the container is no longer Beacon's to remove.
	shared, _, err := insertJSONMember(inserted, []string{"mcp", "servers"}, "theirs", ordered{{"url", "https://t"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := removeJSONMember(shared, []string{"mcp", "servers"}, ServerName, created)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"theirs"`) || strings.Contains(got, ServerName) {
		t.Fatalf("prune removed a container that still held another server:\n%s", got)
	}
}

func TestArrayAppendAndRemoveAreInverse(t *testing.T) {
	for _, doc := range []string{
		"{\n  \"servers\": {}\n}\n",
		"{\n  \"inputs\": []\n}\n",
		"{\n  \"inputs\": [\n    {\"id\": \"other\"}\n  ]\n}\n",
		"{\"inputs\":[{\"id\":\"other\"}]}",
	} {
		appended, created, err := appendJSONElement(doc, nil, "inputs", vscodeInput())
		if err != nil {
			t.Fatal(err)
		}
		back, err := removeJSONElements(appended, nil, "inputs", isBeaconInput, created)
		if err != nil {
			t.Fatal(err)
		}
		if back != doc {
			t.Errorf("array round trip\n doc: %q\n app: %q\n got: %q", doc, appended, back)
		}
	}
}

// The verifier is the backstop under the text splicing: it must reject any edit that changes
// something other than Beacon's own entry, whatever the splice got wrong.
func TestVerifierRejectsEditsBeyondBeaconsEntry(t *testing.T) {
	opts := Options{URL: testURL}
	json := mustTarget(t, "cursor")
	before := `{"theme": "dark", "mcpServers": {"other": {"url": "https://o"}}}`
	good := `{"theme": "dark", "mcpServers": {"other": {"url": "https://o"}, "beacon-managed": {"url": "https://mcp.example.test"}}}`
	if err := verifyConnect(json, before, good, opts); err != nil {
		t.Fatalf("a correct edit was rejected: %v", err)
	}
	for name, bad := range map[string]string{
		"changed another key":   `{"theme": "light", "mcpServers": {"other": {"url": "https://o"}, "beacon-managed": {"url": "https://mcp.example.test"}}}`,
		"dropped another entry": `{"theme": "dark", "mcpServers": {"beacon-managed": {"url": "https://mcp.example.test"}}}`,
		"wrong entry":           `{"theme": "dark", "mcpServers": {"other": {"url": "https://o"}, "beacon-managed": {"url": "https://evil"}}}`,
		"added a key":           `{"theme": "dark", "x": 1, "mcpServers": {"other": {"url": "https://o"}, "beacon-managed": {"url": "https://mcp.example.test"}}}`,
		"does not parse":        `{"theme": "dark",`,
	} {
		if err := verifyConnect(json, before, bad, opts); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	toml := mustTarget(t, "codex")
	tBefore := "model = \"o3\"\n"
	if err := verifyConnect(toml, tBefore, "model = \"o3\"\n\n[mcp_servers.beacon-managed]\nurl = \"https://mcp.example.test\"\n", opts); err != nil {
		t.Fatalf("a correct TOML edit was rejected: %v", err)
	}
	if err := verifyConnect(toml, tBefore, "model = \"o4\"\n\n[mcp_servers.beacon-managed]\nurl = \"https://mcp.example.test\"\n", opts); err == nil {
		t.Error("a TOML edit that changed the model was accepted")
	}
	if err := verifyDisconnect(toml, "a = 1\n[mcp_servers.beacon-managed]\nurl = \"x\"\n", "", Record{}); err == nil {
		t.Error("a disconnect that dropped an unrelated key was accepted")
	}
}

func TestStripTOMLTableHandlesQuotingAndSubtables(t *testing.T) {
	doc := "a = 1\n\n[mcp_servers.\"beacon-managed\"]\nurl = \"x\"\n\n[mcp_servers.beacon-managed.env]\nK = \"v\"\n\n[mcp_servers.beacon-managed-other]\nurl = \"y\"\n"
	got, found := stripTOMLTable(doc, "mcp_servers", ServerName)
	if !found {
		t.Fatal("not found")
	}
	want := "a = 1\n\n[mcp_servers.beacon-managed-other]\nurl = \"y\"\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
