package mcpconnect

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/jsonc"
)

func TestArrayAppendAndRemoveAreInverse(t *testing.T) {
	for _, doc := range []string{
		"{\n  \"servers\": {}\n}\n",
		"{\n  \"inputs\": []\n}\n",
		"{\n  \"inputs\": [\n    {\"id\": \"other\"}\n  ]\n}\n",
		"{\"inputs\":[{\"id\":\"other\"}]}",
	} {
		appended, created, err := jsonc.AppendElement(doc, nil, "inputs", vscodeInput())
		if err != nil {
			t.Fatal(err)
		}
		back, err := jsonc.RemoveElements(appended, nil, "inputs", isBeaconInput, created)
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
