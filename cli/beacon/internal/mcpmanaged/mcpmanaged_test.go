package mcpmanaged

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		ClaudeDropIn:       filepath.Join(dir, "ClaudeCode", "managed-settings.d", ClaudeDropInName),
		CodexConfig:        filepath.Join(dir, "codex", "config.toml"),
		ClaudeManagedPrefs: filepath.Join(dir, "com.anthropic.claudecode.plist"),
		CodexRequirements:  filepath.Join(dir, "codex", "requirements.toml"),
	}
}

func mustParse(t *testing.T, text string) Declaration {
	t.Helper()
	d, err := ParseDeclaration([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func actions(results []Result) map[string]Action {
	out := map[string]Action{}
	for _, r := range results {
		out[r.Runtime] = r.Action
	}
	return out
}

const twoServers = `{"servers": {
  "linear": {"url": "https://mcp.linear.app/mcp"},
  "corp-docs": {"url": "https://mcp.corp.example/mcp", "headers": {"X-Team": "eng \"core\""}}
}}`

func TestParseDeclarationRefusesWhatClaudeCodeWouldRefuse(t *testing.T) {
	cases := map[string]string{
		"no servers":    `{}`,
		"unknown field": `{"servers": {"a": {"url": "https://x.example", "command": "npx"}}}`,
		"http":          `{"servers": {"a": {"url": "http://x.example/mcp"}}}`,
		"not a url":     `{"servers": {"a": {"url": "x.example"}}}`,
		"variable url":  `{"servers": {"a": {"url": "https://${HOST}/mcp"}}}`,
		"variable hdr":  `{"servers": {"a": {"url": "https://x.example", "headers": {"Authorization": "Bearer ${TOKEN}"}}}}`,
		"bad name":      `{"servers": {"a b": {"url": "https://x.example"}}}`,
		"dotted name":   `{"servers": {"a.b": {"url": "https://x.example"}}}`,
		"bad header":    `{"servers": {"a": {"url": "https://x.example", "headers": {"X Y": "1"}}}}`,
		"newline":       `{"servers": {"a": {"url": "https://x.example", "headers": {"X": "1\n[evil]"}}}}`,
		"two values":    `{"servers": {}} {}`,
	}
	for name, text := range cases {
		if _, err := ParseDeclaration([]byte(text)); err == nil {
			t.Errorf("%s: accepted %s", name, text)
		}
	}
	if d := mustParse(t, `{"servers": {}}`); len(d.Servers) != 0 {
		t.Fatal("an empty server list is a valid declaration")
	}
}

func TestApplyWritesBothRuntimesAndIsIdempotent(t *testing.T) {
	p := testPaths(t)
	d := mustParse(t, twoServers)
	got := actions(Apply(Options{Paths: p}, d))
	if got[RuntimeClaude] != ActionWrite || got[RuntimeCodex] != ActionWrite {
		t.Fatalf("first apply: %v", got)
	}

	var claude struct {
		ManagedMcpServers map[string]map[string]any `json:"managedMcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, p.ClaudeDropIn)), &claude); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]any{
		"linear":    {"type": "http", "url": "https://mcp.linear.app/mcp"},
		"corp-docs": {"type": "http", "url": "https://mcp.corp.example/mcp", "headers": map[string]any{"X-Team": `eng "core"`}},
	}
	if !reflect.DeepEqual(claude.ManagedMcpServers, want) {
		t.Fatalf("claude drop-in = %#v", claude.ManagedMcpServers)
	}

	codex, err := parseTOML(read(t, p.CodexConfig))
	if err != nil {
		t.Fatal(err)
	}
	servers := codex[codexTable].(map[string]any)
	if servers["linear"].(map[string]any)["url"] != "https://mcp.linear.app/mcp" {
		t.Fatalf("codex linear = %#v", servers["linear"])
	}
	if h := servers["corp-docs"].(map[string]any)["http_headers"].(map[string]any); h["X-Team"] != `eng "core"` {
		t.Fatalf("codex corp-docs headers = %#v", h)
	}

	if testenv.HasPOSIXFileModes() {
		for _, path := range []string{p.ClaudeDropIn, p.CodexConfig} {
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0o644 {
				t.Errorf("%s mode = %v; every account's runtime must read it", path, info.Mode().Perm())
			}
		}
	}

	got = actions(Apply(Options{Paths: p}, d))
	if got[RuntimeClaude] != ActionUnchanged || got[RuntimeCodex] != ActionUnchanged {
		t.Fatalf("second apply: %v", got)
	}
}

func TestCodexKeepsEverythingElseAndOnlyReplacesItsOwnTables(t *testing.T) {
	p := testPaths(t)
	before := `# fleet defaults
model = "gpt-5"

[mcp_servers.admin-own]
url = "https://admin.example/mcp"

[profiles.fast]
model = "gpt-5-mini"
`
	write(t, p.CodexConfig, before)

	Apply(Options{Paths: p, Runtimes: []string{RuntimeCodex}}, mustParse(t, twoServers))
	after := read(t, p.CodexConfig)
	if !strings.HasPrefix(after, before[:len(before)-1]) {
		t.Fatalf("the existing content was not kept verbatim:\n%s", after)
	}
	if owned := codexOwned(after); !reflect.DeepEqual(owned, []string{"corp-docs", "linear"}) {
		t.Fatalf("owned = %v", owned)
	}

	// The next declaration drops linear and changes corp-docs.
	res := Apply(Options{Paths: p, Runtimes: []string{RuntimeCodex}}, mustParse(t, `{"servers": {"corp-docs": {"url": "https://mcp2.corp.example/mcp"}}}`))
	if res[0].Action != ActionWrite || !reflect.DeepEqual(res[0].Servers, []string{"corp-docs"}) {
		t.Fatalf("second apply = %+v", res[0])
	}
	doc, _ := parseTOML(read(t, p.CodexConfig))
	servers := doc[codexTable].(map[string]any)
	if _, ok := servers["linear"]; ok {
		t.Fatal("a server dropped from the declaration was not removed")
	}
	if servers["corp-docs"].(map[string]any)["url"] != "https://mcp2.corp.example/mcp" {
		t.Fatalf("corp-docs = %#v", servers["corp-docs"])
	}
	if _, ok := servers["admin-own"]; !ok {
		t.Fatal("a table Beacon did not write was removed")
	}

	res = Remove(Options{Paths: p, Runtimes: []string{RuntimeCodex}})
	if res[0].Action != ActionRemove {
		t.Fatalf("remove = %+v", res[0])
	}
	if got := read(t, p.CodexConfig); got != before {
		t.Fatalf("remove did not restore the original:\n%q\nwant\n%q", got, before)
	}
	if res = Remove(Options{Paths: p, Runtimes: []string{RuntimeCodex}}); res[0].Action != ActionAbsent {
		t.Fatalf("second remove = %+v", res[0])
	}
}

func TestCodexLeavesASameNamedServerItDidNotWrite(t *testing.T) {
	p := testPaths(t)
	before := "[mcp_servers.linear]\nurl = \"https://someone-else.example/mcp\"\n"
	write(t, p.CodexConfig, before)
	res := Apply(Options{Paths: p, Runtimes: []string{RuntimeCodex}}, mustParse(t, twoServers))[0]
	if !reflect.DeepEqual(res.Conflicts, []string{"linear"}) || !reflect.DeepEqual(res.Servers, []string{"corp-docs"}) {
		t.Fatalf("result = %+v", res)
	}
	doc, _ := parseTOML(read(t, p.CodexConfig))
	if doc[codexTable].(map[string]any)["linear"].(map[string]any)["url"] != "https://someone-else.example/mcp" {
		t.Fatal("a server Beacon did not write was changed")
	}
}

func TestCodexRefusesAFileItCannotEditSafely(t *testing.T) {
	p := testPaths(t)
	for name, text := range map[string]string{
		"invalid":   "model = \n",
		"not table": "mcp_servers = 3\n",
	} {
		write(t, p.CodexConfig, text)
		res := Apply(Options{Paths: p, Runtimes: []string{RuntimeCodex}}, mustParse(t, twoServers))[0]
		if res.Action != ActionFailed {
			t.Errorf("%s: %+v", name, res)
		}
		if read(t, p.CodexConfig) != text {
			t.Errorf("%s: the file was changed", name)
		}
	}
}

func TestSymlinkedFilesAreRefused(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	p := testPaths(t)
	target := filepath.Join(t.TempDir(), "elsewhere.toml")
	write(t, target, "")
	if err := os.MkdirAll(filepath.Dir(p.CodexConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p.CodexConfig); err != nil {
		t.Fatal(err)
	}
	res := Apply(Options{Paths: p, Runtimes: []string{RuntimeCodex}}, mustParse(t, twoServers))[0]
	if res.Action != ActionFailed {
		t.Fatalf("result = %+v", res)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	p := testPaths(t)
	got := actions(Apply(Options{Paths: p, DryRun: true}, mustParse(t, twoServers)))
	if got[RuntimeClaude] != ActionWrite || got[RuntimeCodex] != ActionWrite {
		t.Fatalf("dry run: %v", got)
	}
	for _, path := range []string{p.ClaudeDropIn, p.CodexConfig} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s was written by a dry run", path)
		}
	}
}

func TestEmptyDeclarationRemovesClaudeDropIn(t *testing.T) {
	p := testPaths(t)
	Apply(Options{Paths: p}, mustParse(t, twoServers))
	got := actions(Apply(Options{Paths: p}, mustParse(t, `{"servers": {}}`)))
	if got[RuntimeClaude] != ActionRemove || got[RuntimeCodex] != ActionRemove {
		t.Fatalf("empty apply: %v", got)
	}
	if _, err := os.Stat(p.ClaudeDropIn); !os.IsNotExist(err) {
		t.Fatal("the Claude drop-in is still there")
	}
}

func TestStatusReportsPushedServersAndWarnings(t *testing.T) {
	p := testPaths(t)
	Apply(Options{Paths: p}, mustParse(t, twoServers))
	write(t, p.ClaudeManagedPrefs, "plist")
	write(t, p.CodexRequirements, "[mcp_servers.other.identity]\nurl = \"https://other.example\"\n")

	want := []Pushed{{Name: "corp-docs", URL: "https://mcp.corp.example/mcp"}, {Name: "linear", URL: "https://mcp.linear.app/mcp"}}
	for _, st := range Status(Options{Paths: p}) {
		if st.Error != "" {
			t.Fatalf("%s: %s", st.Runtime, st.Error)
		}
		if !reflect.DeepEqual(st.Servers, want) {
			t.Errorf("%s servers = %+v", st.Runtime, st.Servers)
		}
		if len(st.Warnings) != 1 {
			t.Errorf("%s warnings = %v", st.Runtime, st.Warnings)
		}
	}
}
