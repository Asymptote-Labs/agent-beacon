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
		"DEL":           `{"servers": {"a": {"url": "https://x.example", "headers": {"X": "a\u007fb"}}}}`,
		"NUL":           `{"servers": {"a": {"url": "https://x.example", "headers": {"X": "a\u0000b"}}}}`,
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
	if servers["org-linear"].(map[string]any)["url"] != "https://mcp.linear.app/mcp" {
		t.Fatalf("codex org-linear = %#v", servers["org-linear"])
	}
	if h := servers["org-corp-docs"].(map[string]any)["http_headers"].(map[string]any); h["X-Team"] != `eng "core"` {
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
	if owned := codexOwned(after); !reflect.DeepEqual(owned, []string{"org-corp-docs", "org-linear"}) {
		t.Fatalf("owned = %v", owned)
	}

	// The next declaration drops linear and changes corp-docs.
	res := Apply(Options{Paths: p, Runtimes: []string{RuntimeCodex}}, mustParse(t, `{"servers": {"corp-docs": {"url": "https://mcp2.corp.example/mcp"}}}`))
	if res[0].Action != ActionWrite || !reflect.DeepEqual(res[0].Servers, []string{"org-corp-docs"}) {
		t.Fatalf("second apply = %+v", res[0])
	}
	doc, _ := parseTOML(read(t, p.CodexConfig))
	servers := doc[codexTable].(map[string]any)
	if _, ok := servers["org-linear"]; ok {
		t.Fatal("a server dropped from the declaration was not removed")
	}
	if servers["org-corp-docs"].(map[string]any)["url"] != "https://mcp2.corp.example/mcp" {
		t.Fatalf("org-corp-docs = %#v", servers["org-corp-docs"])
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
	before := "[mcp_servers.org-linear]\nurl = \"https://someone-else.example/mcp\"\n"
	write(t, p.CodexConfig, before)
	res := Apply(Options{Paths: p, Runtimes: []string{RuntimeCodex}}, mustParse(t, twoServers))[0]
	if !reflect.DeepEqual(res.Conflicts, []string{"org-linear"}) || !reflect.DeepEqual(res.Servers, []string{"org-corp-docs"}) {
		t.Fatalf("result = %+v", res)
	}
	doc, _ := parseTOML(read(t, p.CodexConfig))
	if doc[codexTable].(map[string]any)["org-linear"].(map[string]any)["url"] != "https://someone-else.example/mcp" {
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

	want := map[string][]Pushed{
		RuntimeClaude: {{Name: "corp-docs", URL: "https://mcp.corp.example/mcp"}, {Name: "linear", URL: "https://mcp.linear.app/mcp"}},
		RuntimeCodex:  {{Name: "org-corp-docs", URL: "https://mcp.corp.example/mcp"}, {Name: "org-linear", URL: "https://mcp.linear.app/mcp"}},
	}
	for _, st := range Status(Options{Paths: p}) {
		if st.Error != "" {
			t.Fatalf("%s: %s", st.Runtime, st.Error)
		}
		if !reflect.DeepEqual(st.Servers, want[st.Runtime]) {
			t.Errorf("%s servers = %+v", st.Runtime, st.Servers)
		}
		if len(st.Warnings) != 1 {
			t.Errorf("%s warnings = %v", st.Runtime, st.Warnings)
		}
	}
}

func TestReapplyKeepsCommentsThatIntroduceTheNextTable(t *testing.T) {
	p := testPaths(t)
	opts := Options{Paths: p, Runtimes: []string{RuntimeCodex}}
	Apply(opts, mustParse(t, twoServers))
	// Another tool appends a commented table after Beacon's.
	f, err := os.OpenFile(p.CodexConfig, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n# CI profile, do not remove\n# second line\n[profiles.ci]\nmodel = \"gpt-5\"\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	for i := 0; i < 2; i++ {
		res := Apply(opts, mustParse(t, `{"servers": {"linear": {"url": "https://mcp.linear.app/mcp"}}}`))[0]
		if res.Action == ActionFailed {
			t.Fatalf("apply %d: %+v", i, res)
		}
	}
	after := read(t, p.CodexConfig)
	if !strings.Contains(after, "# CI profile, do not remove\n# second line\n[profiles.ci]\n") {
		t.Fatalf("the comment above [profiles.ci] was lost:\n%s", after)
	}
	if strings.Contains(after, "corp-docs") {
		t.Fatalf("a dropped server is still there:\n%s", after)
	}

	Remove(opts)
	if got := read(t, p.CodexConfig); got != "# CI profile, do not remove\n# second line\n[profiles.ci]\nmodel = \"gpt-5\"\n" {
		t.Fatalf("after remove:\n%q", got)
	}
}

func TestNewDirectoriesAreReadableUnderARestrictiveUmask(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	old := syscallUmask(0o077)
	defer syscallUmask(old)
	p := testPaths(t)
	Apply(Options{Paths: p}, mustParse(t, twoServers))
	for _, dir := range []string{filepath.Dir(p.ClaudeDropIn), filepath.Dir(filepath.Dir(p.ClaudeDropIn)), filepath.Dir(p.CodexConfig)} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("%s mode = %v, want 0755", dir, info.Mode().Perm())
		}
	}
}

func TestReplacingAFileKeepsItsOwnerAndGroup(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	p := testPaths(t)
	write(t, p.CodexConfig, "model = \"gpt-5\"\n")
	if err := os.Chmod(p.CodexConfig, 0o640); err != nil {
		t.Fatal(err)
	}
	type call struct{ uid, gid int }
	var calls []call
	prev := chownFile
	t.Cleanup(func() { chownFile = prev })
	chownFile = func(name string, uid, gid int) error {
		calls = append(calls, call{uid, gid})
		return nil
	}
	// The replacement must ask for exactly the owner and group the file has.
	have := fileOwner(mustStat(t, p.CodexConfig))
	if res := Apply(Options{Paths: p, Runtimes: []string{RuntimeCodex}}, mustParse(t, twoServers))[0]; res.Action != ActionWrite {
		t.Fatalf("apply = %+v", res)
	}
	if len(calls) != 1 || calls[0] != (call{have.uid, have.gid}) {
		t.Fatalf("chown calls = %v, want one with %d:%d", calls, have.uid, have.gid)
	}
	if mode := mustStat(t, p.CodexConfig).Mode().Perm(); mode != 0o640 {
		t.Fatalf("mode = %v, want 0640", mode)
	}

	// A file Beacon creates has no owner to keep.
	calls = nil
	Apply(Options{Paths: p, Runtimes: []string{RuntimeClaude}}, mustParse(t, twoServers))
	if len(calls) != 0 {
		t.Fatalf("chown on a new file: %v", calls)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestReapplyKeepsCommentsAfterBeaconsLastTable(t *testing.T) {
	p := testPaths(t)
	opts := Options{Paths: p, Runtimes: []string{RuntimeCodex}}
	write(t, p.CodexConfig, "model = \"gpt-5\"\n")
	Apply(opts, mustParse(t, twoServers))
	f, err := os.OpenFile(p.CodexConfig, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n# managed by puppet\n# [profiles.ci] disabled for now\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	for i := 0; i < 2; i++ {
		if res := Apply(opts, mustParse(t, `{"servers": {"linear": {"url": "https://mcp.linear.app/mcp"}}}`))[0]; res.Action == ActionFailed {
			t.Fatalf("apply %d: %+v", i, res)
		}
		if after := read(t, p.CodexConfig); !strings.Contains(after, "# managed by puppet\n# [profiles.ci] disabled for now\n") {
			t.Fatalf("apply %d lost the trailing comments:\n%s", i, after)
		}
	}
	Remove(opts)
	if got := read(t, p.CodexConfig); got != "model = \"gpt-5\"\n\n# managed by puppet\n# [profiles.ci] disabled for now\n" {
		t.Fatalf("after remove:\n%q", got)
	}
}

// A person's own [mcp_servers.linear] in ~/.codex/config.toml is merged with the system file table
// by table. A pushed table of the same name would give their stdio server a `url`, and Codex then
// refuses to load their whole config, so pushed servers never take a declared name in Codex.
func TestCodexNamesCannotMergeIntoAPersonsOwnServer(t *testing.T) {
	p := testPaths(t)
	Apply(Options{Paths: p, Runtimes: []string{RuntimeCodex}}, mustParse(t, twoServers))
	doc, err := parseTOML(read(t, p.CodexConfig))
	if err != nil {
		t.Fatal(err)
	}
	for name := range doc[codexTable].(map[string]any) {
		if !strings.HasPrefix(name, CodexPrefix) {
			t.Errorf("pushed Codex server %q has no %q prefix", name, CodexPrefix)
		}
	}
}

func TestCodexReplacesTablesFromBeforeThePrefix(t *testing.T) {
	p := testPaths(t)
	write(t, p.CodexConfig, "[mcp_servers.linear]\n"+CodexMarker+"\nurl = \"https://mcp.linear.app/mcp\"\n")
	res := Apply(Options{Paths: p, Runtimes: []string{RuntimeCodex}}, mustParse(t, twoServers))[0]
	if res.Action != ActionWrite || len(res.Conflicts) != 0 {
		t.Fatalf("apply = %+v", res)
	}
	if owned := codexOwned(read(t, p.CodexConfig)); !reflect.DeepEqual(owned, []string{"org-corp-docs", "org-linear"}) {
		t.Fatalf("owned = %v", owned)
	}
}
