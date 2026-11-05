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
 "registries":["smithery"],"risk":"high","coverage":"complete","guidance":"Do not install or run it.","analyzed_at":"2026-10-02T00:00:00.000Z","repository_flags":["hijackable","owner_changed"],"stars":5,"url":"https://intel.beacon.sh/mcp-servers/github/evil/server"},
{"entity_key":"skill:anthropics/skills#pdf","kind":"skill","name":"pdf","description":null,"repository":"anthropics/skills","code_path":"pdf",
 "registries":[],"risk":"benign","coverage":"partial","guidance":"g","analyzed_at":null,"repository_flags":[],"stars":null,"url":"u"}],"more":true,"note":"n"}`

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
	for _, want := range []string{"RISK", "high", "mcp:evil/server", "hijackable,owner_changed", "benign (partial)", "skill:anthropics/skills#pdf",
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

func TestIntelJSONEscapesC1AndBidiControls(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/lookup", 200, `{"api":"intel.v1","query":{"q":"x"},"results":[{"entity_key":"mcp:a/b","name":"a\u009b2Jb\u202ec"}]}`)
	out, err := runIntel(t, "", "lookup", "x", "--json")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out {
		if r >= 0x80 && r < 0xa0 || r == 0x202e {
			t.Fatalf("--json printed %U raw: %q", r, out)
		}
	}
	var got struct {
		Results []struct{ Name string } `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Results[0].Name != "a\u009b2Jb\u202ec" {
		t.Fatalf("escaped JSON does not decode to the answer: %v %q", err, out)
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
"url":"https://intel.beacon.sh/mcp-servers/github/evil/server","registries":["glama","smithery"],"risk":"high","coverage":"partial","guidance":"Do not install or run it. The analysis did not cover everything.",
"verdict":{"risk":"high","coverage":"partial","analyzed_at":"2026-10-02T00:00:00.000Z","model":"judge-x","commit":"abc123",
 "reasons":["high: finding f1 confirmed as abuse of the installer"],"coverage_reasons":["partial: gap clone_failed at sub"],"unresolved":["Is the host theirs?"],
 "findings":[{"id":"f1","class":"EXFIL","class_label":"Data exfiltration","severity":"critical","level":"abuse","review":"confirmed","affects":"installer","path":"index.js","lines":[4,6],"claim":"Uploads keys\u001b[31m"}],
 "findings_total":1,"notes":[],"notes_total":2,"flags":[],"flags_total":1,"refuted_findings":2},
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
		"Risk:        high (partial). Do not install or run it. The analysis did not cover everything.",
		"Repository:  evil/server (src) [hijackable]",
		"Was named:   good/server",
		"Listed on:   glama, smithery",
		"by judge-x at commit abc123\nCoverage:    partial",
		"Why coverage is partial:", "partial: gap clone_failed at sub",
		"2 notes and 1 third-party flags are not shown",
		"Findings (1 shown of 1; 2 refuted on review)",
		"[critical, abuse, confirmed] Data exfiltration index.js:4-6: Uploads keys [31m",
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
{"q":"Evil Server","matched_by":"text","status":"matched","risk":"high","coverage":"complete","matches":[{"entity_key":"mcp:evil/server","risk":"high"}]},
{"q":"anthropics/skills","matched_by":"repository","status":"matched","risk":"medium","coverage":"partial","matches":[{"entity_key":"skill:anthropics/skills#pdf","risk":"medium"}]},
{"q":"evil","matched_by":"text","status":"ambiguous","risk":null,"matches":[{"entity_key":"mcp:evil/server"},{"entity_key":"mcp:evil/other"}]},
{"q":"zzz","matched_by":"text","status":"not_found","risk":null,"matches":[]}],
"counts":{"high":1,"medium":1,"low":0,"benign":0,"unchecked":0,"partial":1,"ambiguous":1,"not_found":1},"note":"n"}`

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
		"not_found", "medium (partial)", "1 high, 1 medium, 1 ambiguous, 1 not_found. 1 matched with partial coverage.", "not_found means never checked, not safe"} {
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
	_, err := runIntel(t, "", "check", "a", "--fail-on", "high")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "1 at or above high: Evil Server (high)") {
		t.Fatalf("err = %v", err)
	}
	_, err = runIntel(t, "", "check", "a", "--fail-on", "medium")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "2 at or above medium") {
		t.Fatalf("err = %v", err)
	}
	_, err = runIntel(t, "", "check", "a", "--fail-on", "low")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "2 at or above low") {
		t.Fatalf("err = %v", err)
	}
	// ambiguous and not_found never fail a check: they claim no risk.
	f.answer("/api/v1/check", 200, `{"api":"intel.v1","results":[{"q":"evil","status":"ambiguous","risk":null,"matches":[{"entity_key":"mcp:evil/server","risk":"high"}]}],"counts":{}}`)
	if _, err := runIntel(t, "", "check", "evil", "--fail-on", "low"); err != nil {
		t.Fatalf("ambiguous failed the check: %v", err)
	}
	before := f.requests()
	// partial also fails a match with partial coverage, at any risk.
	f.answer("/api/v1/check", 200, `{"api":"intel.v1","results":[
{"q":"pdf","status":"matched","risk":"benign","coverage":"partial"},
{"q":"ok","status":"matched","risk":"benign","coverage":"complete"},
{"q":"new","status":"matched","risk":"unchecked"}],"counts":{"benign":2,"unchecked":1,"partial":1}}`)
	if _, err := runIntel(t, "", "check", "a", "--fail-on", "low"); err != nil {
		t.Fatalf("--fail-on low failed on benign: %v", err)
	}
	_, err = runIntel(t, "", "check", "a", "--fail-on", "partial")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "1 at or above low or with partial coverage: pdf (benign, partial)") {
		t.Fatalf("--fail-on partial: %v", err)
	}
	before = f.requests()
	for _, bad := range []string{"benign", "unchecked", "critical", "malicious", "suspicious", "inconclusive"} {
		if _, err := runIntel(t, "", "check", "a", "--fail-on", bad); err == nil {
			t.Errorf("--fail-on %s accepted", bad)
		}
	}
	if f.requests() != before {
		t.Fatal("a bad --fail-on still sent a request")
	}
	// A CI script still passing a retired verdict value is told what replaced it.
	if _, err := runIntel(t, "", "check", "a", "--fail-on", "suspicious"); err == nil || !strings.Contains(err.Error(), "no longer a risk") ||
		!strings.Contains(err.Error(), "use high, medium or low") {
		t.Fatalf("retired value: %v", err)
	}
	if _, err := runIntel(t, "", "check", "a", "--fail-on", "inconclusive"); err == nil || !strings.Contains(err.Error(), "use partial") {
		t.Fatalf("retired inconclusive: %v", err)
	}
}

// An index still answering with the retired verdicts, or with a risk this build does not
// know, must not pass a gate silently.
func TestIntelCheckFailOnFailsClosed(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/check", 200, `{"api":"intel.v1","results":[
{"q":"old-evil","status":"matched","risk":"malicious"},
{"q":"old-maybe","status":"matched","risk":"inconclusive"},
{"q":"old-ok","status":"matched","risk":"benign"}],"counts":{"malicious":1,"inconclusive":1,"benign":1}}`)
	out, err := runIntel(t, "", "check", "a", "--fail-on", "high")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "1 at or above high: old-evil (malicious)") {
		t.Fatalf("retired malicious under --fail-on high: %v", err)
	}
	if !strings.Contains(out, "1 benign, 1 inconclusive, 1 malicious.") {
		t.Errorf("counts line hid a retired count:\n%s", out)
	}
	// In the retired shape a benign was covered: --fail-on partial, the hint for
	// --fail-on inconclusive, fails what inconclusive failed and passes the benign.
	_, err = runIntel(t, "", "check", "a", "--fail-on", "partial")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "2 at or above low or with partial coverage") || strings.Contains(err.Error(), "old-ok") {
		t.Fatalf("retired values under --fail-on partial: %v", err)
	}
	// An old answer of only benign matches carries no retired key, and no coverage either.
	f.answer("/api/v1/check", 200, `{"api":"intel.v1","results":[{"q":"old-ok","status":"matched","risk":"benign"},{"q":"gone","status":"not_found","risk":null}],"counts":{"benign":1,"not_found":1}}`)
	if _, err := runIntel(t, "", "check", "a", "--fail-on", "partial"); err != nil {
		t.Fatalf("old all-benign answer under --fail-on partial: %v", err)
	}
	// A current answer missing one result's coverage still fails closed.
	f.answer("/api/v1/check", 200, `{"api":"intel.v1","results":[{"q":"ok","status":"matched","risk":"benign","coverage":"complete"},{"q":"bare","status":"matched","risk":"benign"}],"counts":{"benign":2}}`)
	_, err = runIntel(t, "", "check", "a", "--fail-on", "partial")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "1 at or above low or with partial coverage: bare (benign, coverage - not recognized)") {
		t.Fatalf("current answer missing coverage: %v", err)
	}

	f.answer("/api/v1/check", 200, `{"api":"intel.v1","results":[
{"q":"odd","status":"matched","risk":"critical","coverage":"complete"},
{"q":"blank","status":"matched","risk":null},
{"q":"odd-coverage","status":"matched","risk":"benign","coverage":"sampled"}],"counts":{"critical":1}}`)
	_, err = runIntel(t, "", "check", "a", "--fail-on", "high")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "odd (risk critical not recognized)") ||
		!strings.Contains(err.Error(), "blank (risk - not recognized)") || strings.Contains(err.Error(), "odd-coverage") {
		t.Fatalf("unknown risk under --fail-on high: %v", err)
	}
	_, err = runIntel(t, "", "check", "a", "--fail-on", "partial")
	if !errors.Is(err, errIntelCheckFailed) || !strings.Contains(err.Error(), "odd-coverage (benign, coverage sampled not recognized)") {
		t.Fatalf("unknown coverage under --fail-on partial: %v", err)
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

	// A file past the limit is refused, not cut short with its tail unchecked.
	big := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("#\n", intelCheckFileMax/2)+"evil/server\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runIntel(t, "", "check", "--file", big, "--fail-on", "high"); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized --file: %v", err)
	}
	if f.requests() != 0 {
		t.Fatalf("an oversized --file still sent %d requests", f.requests())
	}
}

func TestIntelStatus(t *testing.T) {
	f := startFakeIntel(t)
	f.answer("/api/v1/status", 200, `{"api":"intel.v1","index":{"mcp":{"artifacts":4,"analyzed":1,"high":1,"partial":1},"skill":{"artifacts":1,"analyzed":1,"benign":1}},"latest_analyzed_at":"2026-10-02T00:00:00.000Z","latest_listed_at":null}`)
	out, err := runIntel(t, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Beacon Intel: http://127.0.0.1", "HIGH", "PARTIAL", "mcp    4       1         1     0       0    0       1", "Newest verdict:         2026-10-02T00:00:00.000Z", "Newest registry update: -"} {
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
		"evil\u202eredis\u2066":    "evil redis",
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
