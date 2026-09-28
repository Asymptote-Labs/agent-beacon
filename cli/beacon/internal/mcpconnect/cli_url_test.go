package mcpconnect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeClaude stands in for `claude mcp add/remove --scope user`, editing ~/.claude.json the way
// the real CLI does, and records every invocation.
type fakeClaude struct {
	t     *testing.T
	path  string
	calls [][]string
	// broken makes `mcp add` succeed without writing anything.
	broken bool
}

func (f *fakeClaude) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name != "/opt/bin/claude" {
		return nil, fmt.Errorf("unexpected binary %s", name)
	}
	doc := map[string]any{}
	if data, err := os.ReadFile(f.path); err == nil {
		_ = json.Unmarshal(data, &doc)
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	switch {
	case len(args) >= 3 && args[0] == "mcp" && args[1] == "add":
		if f.broken {
			return []byte("Added"), nil
		}
		entry := map[string]any{"type": "http", "url": args[7]}
		if len(args) == 10 && args[8] == "--header" {
			name, value, _ := strings.Cut(args[9], ": ")
			entry["headers"] = map[string]any{name: value}
		}
		servers[args[6]] = entry
	case len(args) >= 3 && args[0] == "mcp" && args[1] == "remove":
		delete(servers, args[2])
	default:
		return nil, fmt.Errorf("unexpected args %v", args)
	}
	doc["mcpServers"] = servers
	data, _ := json.MarshalIndent(doc, "", "  ")
	return nil, os.WriteFile(f.path, data, 0o600)
}

func claudeOptions(t *testing.T, home string) (Options, *fakeClaude) {
	opts := fileOptions(home)
	fake := &fakeClaude{t: t, path: filepath.Join(home, ".claude.json")}
	opts.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/opt/bin/claude", nil
		}
		return "", errors.New("not found")
	}
	opts.Run = fake.run
	return opts, fake
}

func TestClaudeUsesItsOwnCLIWhenOnPath(t *testing.T) {
	home := isolate(t)
	opts, fake := claudeOptions(t, home)
	opts.TokenEnv = "BEACON_MCP_TOKEN"
	writeFile(t, fake.path, `{"numStartups": 3}`)
	target := mustTarget(t, "claude")
	item := connect(t, opts, target)
	if item.Method != "claude CLI" {
		t.Fatalf("method = %q", item.Method)
	}
	want := [][]string{{"/opt/bin/claude", "mcp", "add", "--transport", "http", "--scope", "user", ServerName, testURL, "--header", "Authorization: Bearer ${BEACON_MCP_TOKEN}"}}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %q\nwant %q", fake.calls, want)
	}
	if backups, _ := filepath.Glob(fake.path + ".beacon.*.bak"); len(backups) != 1 {
		t.Fatalf("the CLI path did not back up ~/.claude.json first: %v", backups)
	}
	disconnect(t, opts, target)
	if last := fake.calls[len(fake.calls)-1]; !reflect.DeepEqual(last, []string{"/opt/bin/claude", "mcp", "remove", ServerName, "--scope", "user"}) {
		t.Fatalf("disconnect ran %q", last)
	}
	if strings.Contains(readFile(t, fake.path), ServerName) {
		t.Fatal("the entry is still there")
	}
}

func TestClaudeCLIThatDoesNotWriteTheEntryIsAnError(t *testing.T) {
	home := isolate(t)
	opts, fake := claudeOptions(t, home)
	fake.broken = true
	plan, _ := Plan(opts, []Target{mustTarget(t, "claude")})
	applied, _ := Apply(context.Background(), opts, plan)
	if applied[0].Err == nil || !strings.Contains(applied[0].Err.Error(), "does not hold") {
		t.Fatalf("err = %v", applied[0].Err)
	}
	if _, err := os.Stat(manifestPath(home)); !os.IsNotExist(err) {
		t.Fatal("a failed connect was recorded as Beacon's")
	}
}

func TestClaudeCLIForceReplacesThroughRemoveThenAdd(t *testing.T) {
	home := isolate(t)
	opts, fake := claudeOptions(t, home)
	opts.Force = true
	writeFile(t, fake.path, `{"mcpServers": {"beacon-managed": {"type": "http", "url": "https://elsewhere.example"}}}`)
	connect(t, opts, mustTarget(t, "claude"))
	if len(fake.calls) != 2 || fake.calls[0][2] != "remove" || fake.calls[1][2] != "add" {
		t.Fatalf("calls = %q", fake.calls)
	}
}

func TestDisconnectFallsBackToTheFileWhenTheCLIIsGone(t *testing.T) {
	home := isolate(t)
	opts, fake := claudeOptions(t, home)
	writeFile(t, fake.path, `{"numStartups": 3}`)
	connect(t, opts, mustTarget(t, "claude"))
	disconnect(t, fileOptions(home), mustTarget(t, "claude"))
	got, _ := decodeJSONC(readFile(t, fake.path))
	if servers, _ := got["mcpServers"].(map[string]any); servers[ServerName] != nil {
		t.Fatal("the entry survived disconnect without the CLI")
	}
	if got["numStartups"] == nil {
		t.Fatal("disconnect removed unrelated state")
	}
}

func TestResolveURL(t *testing.T) {
	cases := []struct {
		flag, ingest, want string
		derived            bool
		err                error
	}{
		{"https://mcp.example.test/", "https://api.example.test", "https://mcp.example.test", false, nil},
		{"", "https://api.example.test/", "https://api.example.test/mcp", true, nil},
		{"", "", "", false, ErrNoURL},
	}
	for _, tc := range cases {
		got, derived, err := ResolveURL(tc.flag, tc.ingest)
		if got != tc.want || derived != tc.derived || !errors.Is(err, tc.err) {
			t.Errorf("ResolveURL(%q, %q) = %q, %v, %v", tc.flag, tc.ingest, got, derived, err)
		}
	}
}

// metadataServer serves RFC 9728 metadata naming resource, and counts requests.
func metadataServer(t *testing.T, resource func(base string) string) (*httptest.Server, *int32) {
	var hits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("the URL check sent credentials")
		}
		if r.URL.Path != "/.well-known/oauth-protected-resource" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": resource(srv.URL), "authorization_servers": []string{srv.URL}})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCheckURLAcceptsTheAdvertisedResource(t *testing.T) {
	srv, hits := metadataServer(t, func(base string) string { return base + "/mcp" })
	got, err := CheckURL(context.Background(), srv.Client(), srv.URL+"/mcp", false)
	if err != nil || got != srv.URL+"/mcp" {
		t.Fatalf("got %q, %v", got, err)
	}
	if *hits != 1 {
		t.Fatalf("the check made %d requests, want 1", *hits)
	}
}

func TestCheckURLRejectsAnExplicitURLTheServerDoesNotName(t *testing.T) {
	srv, _ := metadataServer(t, func(base string) string { return base + "/mcp" })
	_, err := CheckURL(context.Background(), srv.Client(), srv.URL+"/other", false)
	if err == nil || !strings.Contains(err.Error(), "--url "+srv.URL+"/mcp") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckURLAdoptsTheCanonicalURLForADerivedOne(t *testing.T) {
	// The backend serves MCP on its own host and names it; the ingest host's metadata names it too.
	canonical, _ := metadataServer(t, func(base string) string { return base })
	ingest, _ := metadataServer(t, func(string) string { return canonical.URL })
	got, err := CheckURL(context.Background(), http.DefaultClient, ingest.URL+"/mcp", true)
	if err != nil || got != canonical.URL {
		t.Fatalf("got %q, %v; want %q", got, err, canonical.URL)
	}
}

func TestCheckURLFailures(t *testing.T) {
	redirect := httptest.NewServer(http.RedirectHandler("https://example.invalid/", http.StatusFound))
	t.Cleanup(redirect.Close)
	notJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>") }))
	t.Cleanup(notJSON.Close)
	noResource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"x": 1}`) }))
	t.Cleanup(noResource.Close)
	insecure, _ := metadataServer(t, func(string) string { return "http://mcp.example.test" })
	for name, url := range map[string]string{
		"plain http to a remote host": "http://mcp.example.test/mcp",
		"not a url":                   "https://",
		"redirect is not followed":    redirect.URL + "/mcp",
		"not JSON":                    notJSON.URL + "/mcp",
		"no resource":                 noResource.URL + "/mcp",
		"insecure resource":           insecure.URL + "/mcp",
	} {
		if _, err := CheckURL(context.Background(), http.DefaultClient, url, true); err == nil {
			t.Errorf("%s: %s passed the check", name, url)
		}
	}
}

// Regression: when replacing through the CLI, a failed `mcp add` must put back the entry the
// preceding `mcp remove` took out.
func TestClaudeCLIFailedAddRestoresTheEntryItRemoved(t *testing.T) {
	home := isolate(t)
	opts, fake := claudeOptions(t, home)
	opts.Force = true
	original := `{"numStartups": 3, "mcpServers": {"beacon-managed": {"type": "http", "url": "https://elsewhere.example", "headers": {"X-Team": "a"}}}}`
	writeFile(t, fake.path, original)
	run := fake.run
	opts.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[1] == "add" {
			return []byte("boom"), errors.New("exit status 1")
		}
		return run(ctx, name, args...)
	}
	plan, _ := Plan(opts, []Target{mustTarget(t, "claude")})
	applied, _ := Apply(context.Background(), opts, plan)
	if applied[0].Err == nil {
		t.Fatal("a failed add was reported as success")
	}
	st, err := inspect(mustTarget(t, "claude"), fake.path)
	if err != nil || !st.exists || st.url != "https://elsewhere.example" {
		t.Fatalf("the removed entry was not put back: %+v %v\n%s", st.entry, err, readFile(t, fake.path))
	}
	if headers, _ := st.entry["headers"].(map[string]any); headers["X-Team"] != "a" {
		t.Fatalf("the restored entry lost fields: %+v", st.entry)
	}
	got, _ := decodeJSONC(readFile(t, fake.path))
	if got["numStartups"] == nil {
		t.Fatal("restoring the entry dropped other state")
	}
}
