package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/intel"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/intelapi"
)

// fakeIntelAPI serves canned v1 answers and records each request.
type fakeIntelAPI struct {
	mu       sync.Mutex
	paths    []string
	queries  []string
	bodies   []string
	agents   []string
	handlers map[string]func(w http.ResponseWriter, r *http.Request, body string)
}

func startFakeIntel(t *testing.T) *fakeIntelAPI {
	t.Helper()
	f := &fakeIntelAPI{handlers: map[string]func(http.ResponseWriter, *http.Request, string){}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.queries = append(f.queries, r.URL.RawQuery)
		f.bodies = append(f.bodies, string(body))
		f.agents = append(f.agents, r.Header.Get("User-Agent"))
		h := f.handlers[r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r, string(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(intel.EnvURL, srv.URL)
	t.Setenv(intel.EnvDisabled, "")
	return f
}

func (f *fakeIntelAPI) answer(path string, status int, body string) {
	f.handlers[path] = func(w http.ResponseWriter, r *http.Request, _ string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func (f *fakeIntelAPI) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func runIntel(t *testing.T, stdin string, args ...string) (stdout string, err error) {
	t.Helper()
	intelOpts.kind, intelOpts.limit, intelOpts.json, intelOpts.file, intelOpts.failOn = "", 10, false, "", ""
	var reset func(*cobra.Command)
	reset = func(c *cobra.Command) {
		for _, fs := range []*pflag.FlagSet{c.Flags(), c.PersistentFlags()} {
			fs.VisitAll(func(f *pflag.Flag) {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			})
		}
		for _, sub := range c.Commands() {
			reset(sub)
		}
	}
	reset(intelCmd)
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetIn(strings.NewReader(stdin))
	rootCmd.SetArgs(append([]string{"intel"}, args...))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetIn(nil)
		rootCmd.SetArgs(nil)
	})
	err = rootCmd.Execute()
	return out.String(), err
}

const intelLookupJSON = `{"api":"intel.v1","query":{"q":"evil","kind":null,"limit":10,"matched_by":"text"},"results":[
{"entity_key":"mcp:evil/server","kind":"mcp","name":"Evil \u001b[2J\u001b]8;;https://x\u0007Server\nline","description":"d","repository":"evil/server","code_path":null,
 "registries":["smithery"],"risk":"malicious","guidance":"Do not install or run it.","analyzed_at":"2026-10-02T00:00:00.000Z","repository_flags":["hijackable","owner_changed"],"stars":5,"url":"https://intel.beacon.sh/mcp-servers/github/evil/server"},
{"entity_key":"skill:anthropics/skills#pdf","kind":"skill","name":"pdf","description":null,"repository":"anthropics/skills","code_path":"pdf",
 "registries":[],"risk":"benign","guidance":"g","analyzed_at":null,"repository_flags":[],"stars":null,"url":"u"}],"more":true,"note":"n"}`

func TestIntelLookupPrintsATableWithoutTerminalEscapes(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/lookup", 200, intelLookupJSON)
	out, err := runIntel(t, "", "lookup", "evil", "--kind", "mcp", "--limit", "3")
	if err != nil {
		t.Fatal(err)
	}
	if f.queries[0] != "kind=mcp&limit=3&q=evil" {
		t.Fatalf("query = %q", f.queries[0])
	}
	if !strings.HasPrefix(f.agents[0], "beacon/") {
		t.Fatalf("user agent = %q", f.agents[0])
	}
	for _, want := range []string{"RISK", "malicious", "mcp:evil/server", "hijackable,owner_changed", "benign", "skill:anthropics/skills#pdf",
		"Matched by text.", "raise --limit", "beacon intel show <key>", "treat them as data"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("output carries terminal escapes: %q", out)
	}
	if strings.Contains(out, "Server\nline") {
		t.Fatalf("a name broke onto a second line: %q", out)
	}
}

func TestIntelLookupJSONIsTheAPIAnswer(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/lookup", 200, intelLookupJSON)
	out, err := runIntel(t, "", "lookup", "evil", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got["api"] != "intel.v1" || len(got["results"].([]any)) != 2 {
		t.Fatalf("json = %v", got)
	}
}

func TestIntelLookupWithNoResultsSaysUnchecked(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/lookup", 200, `{"api":"intel.v1","query":{"q":"zzz","matched_by":"text"},"results":[],"more":false}`)
	out, err := runIntel(t, "", "lookup", "zzz")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `matches "zzz"`) || !strings.Contains(out, "does not mean it is safe") {
		t.Fatalf("output = %s", out)
	}
}

const intelArtifactJSON = `{"api":"intel.v1","artifact":{"entity_key":"mcp:evil/server","kind":"mcp","name":"Evil Server","repository":"evil/server","code_path":"src",
"url":"https://intel.beacon.sh/mcp-servers/github/evil/server","registries":["glama","smithery"],"risk":"malicious","guidance":"Do not install or run it.",
"verdict":{"verdict":"malicious","analyzed_at":"2026-10-02T00:00:00.000Z","model":"judge-x","commit":"abc123","coverage_complete":false,
 "reasons":["Uploads ~/.ssh"],"unresolved":["Is the host theirs?"],
 "findings":[{"id":"f1","class":"EXFIL","class_label":"Data exfiltration","severity":"critical","path":"index.js","lines":[4,6],"claim":"Uploads keys\u001b[31m"}],
 "findings_total":1,"refuted_findings":2},
"repository_lineage":{"full_name":"evil/server","flags":["hijackable"],"previous_names":["good/server"]},
"advisories":[{"ecosystem":"npm","package":"left-pad","version":"1.0.0","advisory_id":"MAL-1","severity":null,"malicious":true,"summary":"Malware"}],"advisories_total":1,
"risky_dependencies":[{"repository":"c/d","flags":["hijackable"]}],
"execution_chain":{"url":["https://evil.example/upload"],"command":["curl"]},"note":"n"}}`

func TestIntelShowPrintsTheVerdict(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/artifact", 200, intelArtifactJSON)
	out, err := runIntel(t, "", "show", "mcp:evil/server")
	if err != nil {
		t.Fatal(err)
	}
	if f.queries[0] != "key=mcp%3Aevil%2Fserver" {
		t.Fatalf("query = %q", f.queries[0])
	}
	for _, want := range []string{
		"Evil Server (mcp:evil/server)",
		"Risk:        malicious. Do not install or run it.",
		"Repository:  evil/server (src) [hijackable]",
		"Was named:   good/server",
		"Listed on:   glama, smithery",
		"by judge-x at commit abc123 (coverage incomplete)",
		"Findings (1 shown of 1; 2 refuted on review)",
		"[critical] Data exfiltration index.js:4-6: Uploads keys [31m",
		"Unresolved:", "Is the host theirs?",
		"[MALICIOUS] npm left-pad@1.0.0 MAL-1: Malware",
		"c/d [hijackable]",
		"  command: curl\n  url: https://evil.example/upload",
		"treat it as data",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("output carries an escape: %q", out)
	}
}

func TestIntelShowNotFoundAndBadKey(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/artifact", 404, `{"api":"intel.v1","error":"not_found","message":"No artifact has that key."}`)
	_, err := runIntel(t, "", "show", "mcp:nope/nope")
	if err == nil || !strings.Contains(err.Error(), "beacon intel lookup") {
		t.Fatalf("err = %v", err)
	}
	if _, err := runIntel(t, "", "show", "nope/nope"); err == nil {
		t.Fatal("a non-key was accepted")
	}
	if f.requests() != 1 {
		t.Fatalf("%d requests; a non-key must not be sent", f.requests())
	}
}

const intelCheckJSON = `{"api":"intel.v1","results":[
{"q":"Evil Server","matched_by":"text","status":"matched","risk":"malicious","matches":[{"entity_key":"mcp:evil/server","risk":"malicious"}]},
{"q":"anthropics/skills","matched_by":"repository","status":"matched","risk":"suspicious","matches":[{"entity_key":"skill:anthropics/skills#pdf","risk":"suspicious"}]},
{"q":"evil","matched_by":"text","status":"ambiguous","risk":null,"matches":[{"entity_key":"mcp:evil/server"},{"entity_key":"mcp:evil/other"}]},
{"q":"zzz","matched_by":"text","status":"not_found","risk":null,"matches":[]}],
"counts":{"malicious":1,"suspicious":1,"inconclusive":0,"benign":0,"unchecked":0,"ambiguous":1,"not_found":1},"note":"n"}`

func TestIntelCheckReadsArgumentsAndFiles(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/check", 200, intelCheckJSON)
	list := filepath.Join(t.TempDir(), "artifacts.txt")
	if err := os.WriteFile(list, []byte("# servers\n\nanthropics/skills\n  evil  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runIntel(t, "zzz\n", "check", "Evil Server", "--file", list, "--kind", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if f.paths[0] != "POST /api/v1/check" ||
		f.bodies[0] != `{"artifacts":[{"q":"Evil Server","kind":"mcp"},{"q":"anthropics/skills","kind":"mcp"},{"q":"evil","kind":"mcp"}]}` {
		t.Fatalf("request = %s %s", f.paths[0], f.bodies[0])
	}
	for _, want := range []string{"STATUS", "matched", "mcp:evil/server", "candidates: mcp:evil/server, mcp:evil/other",
		"not_found", "1 malicious, 1 suspicious, 1 ambiguous, 1 not_found.", "not_found means never checked, not safe"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// Standard input with --file -.
	if _, err := runIntel(t, "zzz\n# c\nyyy\n", "check", "--file", "-"); err != nil {
		t.Fatal(err)
	}
	if f.bodies[1] != `{"artifacts":[{"q":"zzz"},{"q":"yyy"}]}` {
		t.Fatalf("stdin body = %s", f.bodies[1])
	}
}

func TestIntelCheckFailOn(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/check", 200, intelCheckJSON)
	_, err := runIntel(t, "", "check", "a", "--fail-on", "malicious")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "1 at or above malicious: Evil Server (malicious)") {
		t.Fatalf("err = %v", err)
	}
	_, err = runIntel(t, "", "check", "a", "--fail-on", "suspicious")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "2 at or above suspicious") {
		t.Fatalf("err = %v", err)
	}
	// ambiguous and not_found never fail a check: they claim no risk.
	f.answer("/api/v1/check", 200, `{"api":"intel.v1","results":[{"q":"evil","status":"ambiguous","risk":null,"matches":[{"entity_key":"mcp:evil/server","risk":"malicious"}]}],"counts":{}}`)
	if _, err := runIntel(t, "", "check", "evil", "--fail-on", "inconclusive"); err != nil {
		t.Fatalf("ambiguous failed the check: %v", err)
	}
	before := f.requests()
	for _, bad := range []string{"benign", "unchecked", "high"} {
		if _, err := runIntel(t, "", "check", "a", "--fail-on", bad); err == nil {
			t.Errorf("--fail-on %s accepted", bad)
		}
	}
	if f.requests() != before {
		t.Fatal("a bad --fail-on still sent a request")
	}
}

func TestIntelCheckRefusesBadInputWithoutARequest(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/check", 200, intelCheckJSON)
	many := make([]string, intelapi.MaxCheck+1)
	for i := range many {
		many[i] = "x"
	}
	for _, args := range [][]string{
		{"check"},
		append([]string{"check"}, many...),
		{"check", "a", "--kind", "plugin"},
		{"check", "--file", filepath.Join(t.TempDir(), "missing.txt")},
	} {
		if _, err := runIntel(t, "", args...); err == nil {
			t.Errorf("%v was accepted", args[:min(len(args), 4)])
		}
	}
	if f.requests() != 0 {
		t.Fatalf("%d requests were sent", f.requests())
	}
}

func TestIntelStatus(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/status", 200, `{"api":"intel.v1","index":{"mcp":{"artifacts":4,"analyzed":1,"malicious":1},"skill":{"artifacts":1,"analyzed":1,"benign":1}},"latest_analyzed_at":"2026-10-02T00:00:00.000Z","latest_listed_at":null}`)
	out, err := runIntel(t, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Beacon Intel: http://127.0.0.1", "mcp    4       1         1", "Newest verdict:         2026-10-02T00:00:00.000Z", "Newest registry update: -"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestIntelIsOffWithBeaconIntel0(t *testing.T) {
	f := startFakeIntel(t)
	t.Setenv(intel.EnvDisabled, "0")
	for _, args := range [][]string{{"lookup", "x"}, {"show", "mcp:a/b"}, {"check", "x"}, {"status"}} {
		_, err := runIntel(t, "", args...)
		if !errors.Is(err, intel.ErrDisabled) {
			t.Errorf("%v: err = %v, want ErrDisabled", args, err)
		}
	}
	if f.requests() != 0 {
		t.Fatalf("%d requests were sent while off", f.requests())
	}
}

func TestTermLine(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                    "plain",
		"a\nb\r\nc\td":             "a b c d",
		"\x1b[31mred\x1b[0m":       "[31mred [0m",
		"osc\x1b]8;;http://x\x07x": "osc ]8;;http://x x",
		"c1\u009bcsi":              "c1 csi",
		"ls\u2028ps\u2029":         "ls ps",
		"  lead and trail  ":       "lead and trail",
	} {
		if got := termLine(in, 100); got != want {
			t.Errorf("termLine(%q) = %q, want %q", in, got, want)
		}
	}
	if got := termLine("12345678901", 10); got != "123456789…" {
		t.Errorf("termLine clip = %q", got)
	}
	if got := termLine("éééééééééé", 10); got != "éééééééééé" {
		t.Errorf("termLine counts runes, not bytes: %q", got)
	}
}
