package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/intel"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/intelapi"
)

type intelFake struct {
	mu     sync.Mutex
	urls   []string
	bodies []string
	answer func(path, body string) (int, string)
}

func (f *intelFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.urls)
}

// newIntelServer returns a server whose Beacon Intel client talks to a fake API.
func newIntelServer(t *testing.T, answer func(path, body string) (int, string)) (*Server, *intelFake) {
	t.Helper()
	f := &intelFake{answer: answer}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.urls = append(f.urls, r.Method+" "+r.URL.String())
		f.bodies = append(f.bodies, string(body))
		f.mu.Unlock()
		status, out := f.answer(r.URL.Path, string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(srv.Close)
	s := New(Options{LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	s.newIntel = func() (*intelapi.Client, error) {
		return &intelapi.Client{BaseURL: srv.URL, HTTPClient: srv.Client(), UserAgent: "beacon-mcp/test"}, nil
	}
	s.registryEnv = noRegistryEnv
	return s, f
}

// noRegistryEnv keeps a test from reading the machine's own npm, pip and uv configuration.
func noRegistryEnv(bool) intel.RegistryEnv { return intel.RegistryEnv{} }

func call(t *testing.T, s *Server, name string, args map[string]interface{}) (string, bool) {
	t.Helper()
	res, err := s.callTool(context.Background(), callToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s content = %+v", name, res.Content)
	}
	return res.Content[0].Text, res.IsError
}

func TestIntelToolsAreListedWithSchemas(t *testing.T) {
	s := New(Options{LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	if err := s.HasExpectedTools(); err != nil {
		t.Fatal(err)
	}
	listed := map[string]Tool{}
	for _, tool := range s.listTools() {
		listed[tool.Name] = tool
	}
	for _, name := range intelTools() {
		tool, ok := listed[name]
		if !ok {
			t.Fatalf("%s not listed", name)
		}
		if tool.InputSchema["type"] != "object" || len(tool.Description) < 80 {
			t.Fatalf("%s: schema %v, description %q", name, tool.InputSchema, tool.Description)
		}
	}
	if !strings.Contains(listed["intel_lookup"].Description, "never that it is safe") {
		t.Fatal("intel_lookup must say no result is not safe")
	}
	if !strings.Contains(listed["intel_check_installed"].Description, "never leave the machine") {
		t.Fatal("intel_check_installed must say what stays local")
	}
}

func TestIntelLookupPassesTheAnswerThrough(t *testing.T) {
	answer := `{"api":"intel.v1","query":{"q":"evil","matched_by":"text"},"results":[{"entity_key":"mcp:evil/server","risk":"malicious"}],"more":false,"note":"n"}`
	s, f := newIntelServer(t, func(string, string) (int, string) { return 200, answer })
	text, isErr := call(t, s, "intel_lookup", map[string]interface{}{"q": "evil", "kind": "mcp", "limit": float64(3)})
	if isErr {
		t.Fatal(text)
	}
	if f.urls[0] != "GET /api/v1/lookup?kind=mcp&limit=3&q=evil" {
		t.Fatalf("request = %s", f.urls[0])
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(text), &got); err != nil || got["api"] != "intel.v1" {
		t.Fatalf("text = %s (%v)", text, err)
	}
}

func TestIntelToolErrorsAreToolErrors(t *testing.T) {
	s, f := newIntelServer(t, func(path, _ string) (int, string) {
		return 404, `{"api":"intel.v1","error":"not_found","message":"No artifact has that key."}`
	})
	text, isErr := call(t, s, "intel_get_artifact", map[string]interface{}{"key": "mcp:nope/nope"})
	if !isErr || !strings.Contains(text, "intel_lookup") {
		t.Fatalf("not found = %q, %v", text, isErr)
	}
	for _, tc := range []struct {
		name string
		args map[string]interface{}
	}{
		{"intel_lookup", map[string]interface{}{}},
		{"intel_lookup", map[string]interface{}{"q": "x", "kind": "plugin"}},
		{"intel_get_artifact", map[string]interface{}{"key": "evil/server"}},
		{"intel_check", map[string]interface{}{}},
		{"intel_check", map[string]interface{}{"artifacts": []interface{}{}}},
		{"intel_check", map[string]interface{}{"artifacts": []interface{}{float64(3)}}},
		{"intel_check", map[string]interface{}{"artifacts": []interface{}{map[string]interface{}{"q": "x", "kind": float64(1)}}}},
		{"intel_check", map[string]interface{}{"artifacts": []interface{}{map[string]interface{}{"q": "x", "kind": "plugin"}}}},
		{"intel_check", map[string]interface{}{"artifacts": []interface{}{""}}},
	} {
		text, isErr := call(t, s, tc.name, tc.args)
		if !isErr {
			t.Errorf("%s %v succeeded: %s", tc.name, tc.args, text)
		}
	}
	if f.count() != 1 {
		t.Fatalf("%d requests; only the valid artifact call should be sent", f.count())
	}
}

func TestIntelCheckSendsStringsAndObjects(t *testing.T) {
	s, f := newIntelServer(t, func(string, string) (int, string) {
		return 200, `{"api":"intel.v1","results":[],"counts":{}}`
	})
	_, isErr := call(t, s, "intel_check", map[string]interface{}{"artifacts": []interface{}{
		"@playwright/mcp", map[string]interface{}{"q": "anthropics/skills", "kind": "skill"}, map[string]interface{}{"q": "x", "kind": nil},
	}})
	if isErr {
		t.Fatal("check failed")
	}
	if f.bodies[0] != `{"artifacts":[{"q":"@playwright/mcp"},{"q":"anthropics/skills","kind":"skill"},{"q":"x"}]}` {
		t.Fatalf("body = %s", f.bodies[0])
	}
}

func TestIntelToolsAreOffWithBeaconIntel0(t *testing.T) {
	t.Setenv(intel.EnvDisabled, "0")
	s := New(Options{LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	s.registryEnv = noRegistryEnv
	s.inventory = func(bool) inventory.Result {
		return inventory.Result{Skills: []inventory.Skill{{Runtime: "claude_code", SkillName: "pdf", Exists: true}}}
	}
	for _, tc := range []struct {
		name string
		args map[string]interface{}
	}{
		{"intel_lookup", map[string]interface{}{"q": "x"}},
		{"intel_get_artifact", map[string]interface{}{"key": "mcp:a/b"}},
		{"intel_check", map[string]interface{}{"artifacts": []interface{}{"x"}}},
		{"intel_check_installed", map[string]interface{}{"include_private_names": true}},
	} {
		text, isErr := call(t, s, tc.name, tc.args)
		if !isErr || !strings.Contains(text, "BEACON_INTEL=0") {
			t.Errorf("%s = %q, %v", tc.name, text, isErr)
		}
	}
	// A dry run sends nothing, so it still works.
	if text, isErr := call(t, s, "intel_check_installed", map[string]interface{}{"dry_run": true}); isErr {
		t.Errorf("dry run while off: %s", text)
	}
}

func fakeInventory(nServers int) inventory.Result {
	res := inventory.Result{
		Skills: []inventory.Skill{
			{Runtime: "claude_code", SkillName: "pdf", SourceScope: "user", Exists: true},
			{Runtime: "claude_code", SkillName: "beacon-memory-recall", SourceScope: "user", Exists: true},
		},
	}
	res.MCPServers = append(res.MCPServers,
		inventory.MCPServer{Runtime: "claude_code", ServerName: "private-db", SourceScope: "user", CommandPresent: true,
			Definition: map[string]interface{}{"command": "node", "args": []interface{}{"/Users/me/acme/db.js"}, "env": map[string]interface{}{"DB_PASSWORD": "hunter2"}}},
		inventory.MCPServer{Runtime: "cursor", ServerName: "beacon", SourceScope: "user", CommandPresent: true,
			Definition: map[string]interface{}{"command": "beacon", "args": []interface{}{"mcp", "serve"}}},
	)
	for i := 0; i < nServers; i++ {
		res.MCPServers = append(res.MCPServers, inventory.MCPServer{
			Runtime: "claude_code", ServerName: fmt.Sprintf("s%d", i), SourceScope: "user", CommandPresent: true,
			Definition: map[string]interface{}{"command": "npx", "args": []interface{}{"-y", fmt.Sprintf("pkg-%02d@latest", i), "--api-key", "sk-live-secret"}},
		})
	}
	return res
}

func TestIntelCheckInstalledDryRunSendsNothing(t *testing.T) {
	s, f := newIntelServer(t, func(string, string) (int, string) { return 500, "" })
	s.inventory = func(includeProject bool) inventory.Result {
		if includeProject {
			t.Error("include_project false was not passed through")
		}
		return fakeInventory(2)
	}
	text, isErr := call(t, s, "intel_check_installed", map[string]interface{}{"dry_run": true, "include_project": false})
	if isErr {
		t.Fatal(text)
	}
	if f.count() != 0 {
		t.Fatalf("a dry run sent %d requests", f.count())
	}
	var report installedReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatal(err)
	}
	var sent []string
	for _, item := range report.Sent {
		sent = append(sent, item.Kind+":"+item.Q)
	}
	if strings.Join(sent, " ") != "mcp:npm:pkg-00 mcp:npm:pkg-01" || !report.DryRun {
		t.Fatalf("sent = %v", sent)
	}
	reasons := map[string]string{}
	for _, sk := range report.Skipped {
		reasons[sk.Name] = sk.Reason
	}
	if reasons["private-db"] != intel.ReasonNoPackage || reasons["beacon"] != intel.ReasonBeacon || reasons["beacon-memory-recall"] != intel.ReasonBeacon ||
		reasons["pdf"] != intel.ReasonPrivateSkill {
		t.Fatalf("skipped = %+v", report.Skipped)
	}
}

func TestIntelCheckInstalledChecksInBatchesAndMapsResultsBack(t *testing.T) {
	s, f := newIntelServer(t, func(path, body string) (int, string) {
		var req struct {
			Artifacts []intelapi.CheckItem `json:"artifacts"`
		}
		_ = json.Unmarshal([]byte(body), &req)
		var results []string
		for _, a := range req.Artifacts {
			switch {
			case a.Q == "npm:pkg-07":
				results = append(results, fmt.Sprintf(`{"q":%q,"status":"matched","risk":"malicious","guidance":"Do not install or run it.","matches":[{"entity_key":"mcp:npm:pkg-07"},{"entity_key":"b"},{"entity_key":"c"},{"entity_key":"d"}]}`, a.Q))
			default:
				results = append(results, fmt.Sprintf(`{"q":%q,"status":"not_found","risk":null,"matches":[]}`, a.Q))
			}
		}
		return 200, `{"api":"intel.v1","results":[` + strings.Join(results, ",") + `],"counts":{}}`
	})
	s.inventory = func(bool) inventory.Result { return fakeInventory(60) }
	text, isErr := call(t, s, "intel_check_installed", map[string]interface{}{})
	if isErr {
		t.Fatal(text)
	}
	if f.count() != 2 {
		t.Fatalf("%d requests, want 2 batches for 60 artifacts", f.count())
	}
	var first, second struct {
		Artifacts []intelapi.CheckItem `json:"artifacts"`
	}
	_ = json.Unmarshal([]byte(f.bodies[0]), &first)
	_ = json.Unmarshal([]byte(f.bodies[1]), &second)
	if len(first.Artifacts) != intelapi.MaxCheck || len(second.Artifacts) != 10 {
		t.Fatalf("batches = %d + %d", len(first.Artifacts), len(second.Artifacts))
	}
	for _, body := range f.bodies {
		for _, leak := range []string{"sk-live-secret", "--api-key", "hunter2", "DB_PASSWORD", "/Users/me", "acme", "private-db", "latest", "beacon-memory-recall", `"beacon"`, "pdf"} {
			if strings.Contains(body, leak) {
				t.Fatalf("a request carried %q: %s", leak, body)
			}
		}
	}
	var report installedReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 60 || report.Counts["malicious"] != 1 || report.Counts["not_found"] != 59 {
		t.Fatalf("results %d, counts %v", len(report.Results), report.Counts)
	}
	var evil installedResult
	for _, r := range report.Results {
		if r.Q == "npm:pkg-07" {
			evil = r
		}
	}
	if evil.Risk != "malicious" || len(evil.FoundIn) != 1 || evil.FoundIn[0].Name != "s7" || evil.FoundIn[0].Runtime != "claude_code" ||
		evil.Basis != intel.BasisNPM || len(evil.Matches) != intelInstalledMatches {
		t.Fatalf("pkg-07 = %+v", evil)
	}
	if evil.NameOnly {
		t.Fatal("a package match was marked name_only")
	}
	if !strings.Contains(report.Note, "not safe") {
		t.Fatalf("note = %q", report.Note)
	}
}

func TestIntelCheckInstalledRefusesAMismatchedAnswer(t *testing.T) {
	s, _ := newIntelServer(t, func(string, string) (int, string) {
		return 200, `{"api":"intel.v1","results":[],"counts":{}}`
	})
	s.inventory = func(bool) inventory.Result { return fakeInventory(1) }
	text, isErr := call(t, s, "intel_check_installed", map[string]interface{}{})
	if !isErr || !strings.Contains(text, "0 results for 1 artifacts") {
		t.Fatalf("= %q, %v", text, isErr)
	}
}

func TestIntelCheckInstalledWithNothingToSend(t *testing.T) {
	s, f := newIntelServer(t, func(string, string) (int, string) { return 500, "" })
	s.inventory = func(bool) inventory.Result {
		return inventory.Result{MCPServers: fakeInventory(0).MCPServers}
	}
	text, isErr := call(t, s, "intel_check_installed", map[string]interface{}{})
	if isErr || f.count() != 0 || !strings.Contains(text, "No MCP server or skill on this machine has a public identifier") {
		t.Fatalf("= %q, %v, %d requests", text, isErr, f.count())
	}
}

func TestIntelToolsOverStdio(t *testing.T) {
	s, _ := newIntelServer(t, func(string, string) (int, string) {
		return 200, `{"api":"intel.v1","index":{},"results":[{"entity_key":"mcp:a/b","risk":"benign"}],"query":{"matched_by":"repository"}}`
	})
	input := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"intel_lookup","arguments":{"q":"a/b"}}}` + "\n"
	var out strings.Builder
	if err := s.ServeStdio(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		ID     int        `json:"id"`
		Result toolResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(out.String()), &resp); err != nil || resp.ID != 7 || resp.Result.IsError ||
		!strings.Contains(resp.Result.Content[0].Text, `"mcp:a/b"`) {
		t.Fatalf("response = %s (%v)", out.String(), err)
	}
}

func TestIntelToolsShareTheCloudContract(t *testing.T) {
	s := New(Options{LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	listed := map[string]Tool{}
	for _, tool := range s.listTools() {
		listed[tool.Name] = tool
	}
	for _, spec := range intelapi.Tools(intelToolNames) {
		tool := listed[spec.Name]
		if tool.Description != spec.Description || tool.InputSchema["additionalProperties"] != false {
			t.Errorf("%s does not carry the shared spec: %+v", spec.Name, tool.InputSchema)
		}
	}
}

func TestIntelToolArgumentErrorsComeFirst(t *testing.T) {
	t.Setenv(intel.EnvDisabled, "0")
	s := New(Options{LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	for name, args := range map[string]map[string]interface{}{
		"intel_lookup":          {"q": "x", "user": "someone"},
		"intel_get_artifact":    {"key": "evil/server"},
		"intel_check":           {"artifacts": []interface{}{}},
		"intel_check_installed": {"dry_run": "true"},
	} {
		text, isErr := call(t, s, name, args)
		if !isErr || strings.Contains(text, "BEACON_INTEL=0") {
			t.Errorf("%s = %q; want the argument error, not the off switch", name, text)
		}
	}
}

func TestIntelCheckInstalledRefusesMalformedArguments(t *testing.T) {
	s, f := newIntelServer(t, func(string, string) (int, string) { return 500, "" })
	s.inventory = func(bool) inventory.Result { return fakeInventory(2) }
	for _, args := range []map[string]interface{}{
		{"dry_run": "true"},
		{"dry_run": 1},
		{"dry_run": nil},
		{"include_private_names": "yes", "dry_run": true},
		{"include_project": "false"},
		{"dryrun": true},
	} {
		text, isErr := call(t, s, "intel_check_installed", args)
		if !isErr {
			t.Errorf("%v was accepted: %s", args, text)
		}
	}
	if f.count() != 0 {
		t.Fatalf("malformed arguments sent %d requests", f.count())
	}
}

func TestIntelCheckInstalledHoldsBackAScopeFromTheUsersNpmrc(t *testing.T) {
	s, f := newIntelServer(t, func(string, string) (int, string) { return 500, "" })
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte("@acme:registry=https://npm.acme.internal/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.registryEnv = func(bool) intel.RegistryEnv { return intel.RegistryEnv{Home: home} }
	s.inventory = func(bool) inventory.Result {
		return inventory.Result{MCPServers: []inventory.MCPServer{
			{Runtime: "cursor", ServerName: "billing", SourceScope: "user", CommandPresent: true,
				Definition: map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@acme/billing-mcp"}}},
			{Runtime: "cursor", ServerName: "playwright", SourceScope: "user", CommandPresent: true,
				Definition: map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@playwright/mcp"}}},
		}}
	}
	text, isErr := call(t, s, "intel_check_installed", map[string]interface{}{"dry_run": true})
	if isErr {
		t.Fatal(text)
	}
	var report installedReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Sent) != 1 || report.Sent[0].Q != "npm:@playwright/mcp" || len(report.Skipped) != 1 || report.Skipped[0].Reason != intel.ReasonPrivatePackage {
		t.Fatalf("report = %+v", report)
	}
	if strings.Contains(text, "acme.internal") || f.count() != 0 {
		t.Fatalf("the registry leaked or a request was sent: %s", text)
	}
}

func TestIntelCheckInstalledMarksNameMatches(t *testing.T) {
	s, f := newIntelServer(t, func(path, body string) (int, string) {
		// The package matched nothing, so the index matched its text against names; the skill
		// matched by name.
		return 200, `{"api":"intel.v1","results":[
			{"q":"npm:some-mcp","matched_by":"text","status":"matched","risk":"benign","guidance":"No issues found.","matches":[{"entity_key":"mcp:other/some-mcp"}]},
			{"q":"pdf","matched_by":"text","status":"matched","risk":"benign","guidance":"No issues found.","matches":[{"entity_key":"skill:a/b#pdf"}]}
		],"counts":{}}`
	})
	s.inventory = func(bool) inventory.Result {
		return inventory.Result{
			MCPServers: []inventory.MCPServer{{Runtime: "cursor", ServerName: "x", SourceScope: "user", CommandPresent: true,
				Definition: map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "some-mcp"}}}},
			Skills: []inventory.Skill{{Runtime: "claude_code", SkillName: "pdf", SourceScope: "user", Exists: true}},
		}
	}
	text, isErr := call(t, s, "intel_check_installed", map[string]interface{}{"include_private_names": true})
	if isErr || f.count() != 1 {
		t.Fatalf("= %q, %v", text, isErr)
	}
	var report installedReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatal(err)
	}
	for _, r := range report.Results {
		if !r.NameOnly || !strings.HasPrefix(r.Guidance, "Matched by name only") || !strings.HasSuffix(r.Guidance, "No issues found.") {
			t.Errorf("%s = %+v, want name_only", r.Q, r)
		}
	}
}
