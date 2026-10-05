package cmd

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// testCursorAdminKey is distinctive so a test can prove it never lands in any output or file.
const testCursorAdminKey = "key_beacon_test_7f3a9c1e5b"

var cursorUsageTestNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// fakeCursorAdminAPI serves /teams/filtered-usage-events from a fixed list, honoring the window.
type fakeCursorAdminAPI struct {
	mu       sync.Mutex
	events   []map[string]any
	requests []map[string]any
	auth     []string
}

func (f *fakeCursorAdminAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/teams/filtered-usage-events" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		user, _, _ := r.BasicAuth()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		f.mu.Lock()
		f.requests = append(f.requests, body)
		f.auth = append(f.auth, user)
		start, _ := body["startDate"].(float64)
		end, _ := body["endDate"].(float64)
		var page []map[string]any
		for _, ev := range f.events {
			ts := ev["timestamp"].(int64)
			if float64(ts) >= start && float64(ts) <= end {
				page = append(page, ev)
			}
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"totalUsageEventsCount": len(page),
			"pagination":            map[string]any{"hasNextPage": false},
			"usageEventsDisplay":    page,
		})
	})
}

type cursorUsageFixture struct {
	api      *fakeCursorAdminAPI
	url      string
	root     string
	state    string
	log      string
	globalDB string
}

func newCursorUsageFixture(t *testing.T, signedInEmail string) *cursorUsageFixture {
	t.Helper()
	root := t.TempDir()
	testenv.SetHome(t, root)
	f := &cursorUsageFixture{
		api:      &fakeCursorAdminAPI{},
		root:     root,
		state:    filepath.Join(root, "state", "cursor-usage.json"),
		log:      filepath.Join(root, "logs", "runtime.jsonl"),
		globalDB: filepath.Join(root, "state.vscdb"),
	}
	f.api.events = []map[string]any{
		cursorAdminEvent(cursorUsageTestNow.Add(-2*time.Hour), 1000, 4.5),
		cursorAdminEvent(cursorUsageTestNow.Add(-time.Hour), 2000, 0),
	}
	srv := httptest.NewServer(f.api.handler(t))
	t.Cleanup(srv.Close)
	f.url = srv.URL

	db, err := sql.Open("sqlite", f.globalDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	if signedInEmail != "" {
		if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES ('cursorAuth/cachedEmail', ?)`, signedInEmail); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES ('cursorAuth/accessToken', 'cursor-session-token')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	t.Setenv(defaultCursorAdminKeyEnv, testCursorAdminKey)
	previousNow := cursorUsageNow
	cursorUsageNow = func() time.Time { return cursorUsageTestNow }
	t.Cleanup(func() { cursorUsageNow = previousNow })
	return f
}

func cursorAdminEvent(ts time.Time, input int, chargedCents float64) map[string]any {
	return map[string]any{
		"timestamp":        ts.UnixMilli(),
		"model":            "claude-4.5-sonnet",
		"kind":             "USAGE_EVENT_KIND_USAGE_BASED",
		"isTokenBasedCall": true,
		"tokenUsage": map[string]any{
			"inputTokens": input, "outputTokens": 50, "cacheReadTokens": 300, "cacheWriteTokens": 7, "totalCents": 1.25,
		},
		"chargedCents": chargedCents,
		"userEmail":    "dev@example.com",
		"userId":       42,
	}
}

// run executes `beacon endpoint cursor usage <args>` through the real command tree, with every flag
// in the subtree reset first because this CLI keeps flag values in package variables.
func (f *cursorUsageFixture) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var reset func(*cobra.Command)
	reset = func(c *cobra.Command) {
		c.Flags().VisitAll(func(fl *pflag.Flag) {
			_ = fl.Value.Set(fl.DefValue)
			fl.Changed = false
		})
		for _, sub := range c.Commands() {
			reset(sub)
		}
	}
	reset(endpointCursorUsageCmd)
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	full := append([]string{"endpoint", "cursor", "usage"}, args...)
	if len(args) > 0 && (args[0] == "sync" || args[0] == "status") {
		full = append(full, "--state", f.state, "--global-db", f.globalDB)
		if args[0] == "sync" {
			full = append(full, "--log-path", f.log, "--base-url", f.url)
		}
	}
	rootCmd.SetArgs(full)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	out := stdout.String() + stderr.String()
	if strings.Contains(out, testCursorAdminKey) {
		t.Fatalf("command output contains the admin key:\n%s", out)
	}
	if err != nil && strings.Contains(err.Error(), testCursorAdminKey) {
		t.Fatalf("error contains the admin key: %v", err)
	}
	return stdout.String(), err
}

func (f *cursorUsageFixture) logged(t *testing.T) []schema.Event {
	t.Helper()
	file, err := os.Open(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out []schema.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var ev schema.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

// assertKeyNowhereOnDisk walks everything the command could have written.
func (f *cursorUsageFixture) assertKeyNowhereOnDisk(t *testing.T) {
	t.Helper()
	_ = filepath.Walk(f.root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(data, []byte(testCursorAdminKey)) {
			t.Errorf("%s contains the admin key", path)
		}
		return nil
	})
}

func TestCursorUsageSyncWritesTokenUsageForTheSignedInAccount(t *testing.T) {
	f := newCursorUsageFixture(t, "dev@example.com")
	out, err := f.run(t, "sync")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cursor usage sync (email:dev@example.com): 2 fetched, 2 written") {
		t.Fatalf("output = %q", out)
	}
	if len(f.api.requests) != 1 {
		t.Fatalf("requests = %d", len(f.api.requests))
	}
	if f.api.requests[0]["email"] != "dev@example.com" {
		t.Fatalf("the default scope must be the signed-in account: %v", f.api.requests[0])
	}
	if f.api.auth[0] != testCursorAdminKey {
		t.Fatalf("basic auth user = %q", f.api.auth[0])
	}

	events := f.logged(t)
	if len(events) != 2 {
		t.Fatalf("logged %d events", len(events))
	}
	for _, ev := range events {
		if ev.Event.Action != "token.usage" || ev.Harness.Name != "cursor" || ev.Harness.CollectionMethod != schema.CollectionMethodPoll {
			t.Fatalf("event = %+v %+v", ev.Event, ev.Harness)
		}
	}
	if *events[0].GenAI.Usage.InputTokens != 1000 || *events[0].GenAI.Usage.CostUSD != 0.045 {
		t.Fatalf("usage = %+v", events[0].GenAI.Usage)
	}

	// A second run writes nothing new.
	out, err = f.run(t, "sync")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0 written, 2 already collected") || len(f.logged(t)) != 2 {
		t.Fatalf("repeat sync: %q", out)
	}
	f.assertKeyNowhereOnDisk(t)
}

func TestCursorUsageSyncRequiresTheKeyInTheEnvironment(t *testing.T) {
	f := newCursorUsageFixture(t, "dev@example.com")
	t.Setenv(defaultCursorAdminKeyEnv, "")
	_, err := f.run(t, "sync")
	if err == nil || !strings.Contains(err.Error(), "set CURSOR_ADMIN_API_KEY") {
		t.Fatalf("err = %v", err)
	}
	if len(f.api.requests) != 0 {
		t.Fatal("a request was made without a key")
	}
}

func TestCursorUsageSyncReadsACustomKeyVariable(t *testing.T) {
	f := newCursorUsageFixture(t, "dev@example.com")
	t.Setenv(defaultCursorAdminKeyEnv, "")
	t.Setenv("ACME_CURSOR_KEY", testCursorAdminKey)
	if _, err := f.run(t, "sync", "--api-key-env", "ACME_CURSOR_KEY"); err != nil {
		t.Fatal(err)
	}
	if len(f.api.auth) != 1 || f.api.auth[0] != testCursorAdminKey {
		t.Fatalf("auth = %v", f.api.auth)
	}
}

func TestCursorUsageSyncWithoutASignedInAccountAsksWhoseUsage(t *testing.T) {
	f := newCursorUsageFixture(t, "")
	_, err := f.run(t, "sync")
	if err == nil || !strings.Contains(err.Error(), "--team") {
		t.Fatalf("err = %v", err)
	}
	if len(f.api.requests) != 0 {
		t.Fatal("collected without a scope")
	}
}

func TestCursorUsageSyncScopes(t *testing.T) {
	f := newCursorUsageFixture(t, "dev@example.com")
	if _, err := f.run(t, "sync", "--team"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.api.requests[0]["email"]; ok {
		t.Fatalf("--team must not filter by email: %v", f.api.requests[0])
	}
	if _, err := f.run(t, "sync", "--user-id", "42"); err != nil {
		t.Fatal(err)
	}
	if f.api.requests[1]["userId"] != float64(42) {
		t.Fatalf("--user-id body = %v", f.api.requests[1])
	}
	if _, err := f.run(t, "sync", "--email", "lead@example.com"); err != nil {
		t.Fatal(err)
	}
	if f.api.requests[2]["email"] != "lead@example.com" {
		t.Fatalf("--email body = %v", f.api.requests[2])
	}
	if _, err := f.run(t, "sync", "--team", "--email", "x@example.com"); err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("conflicting scopes err = %v", err)
	}
	if _, err := f.run(t, "sync", "--user-id", "abc"); err == nil || !strings.Contains(err.Error(), "numeric") {
		t.Fatalf("non-numeric user id err = %v", err)
	}
}

func TestCursorUsageSyncPrintIsADryRun(t *testing.T) {
	f := newCursorUsageFixture(t, "dev@example.com")
	out, err := f.run(t, "sync", "--print")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines != 2 || !strings.Contains(out, `"token.usage"`) {
		t.Fatalf("print output = %q", out)
	}
	if len(f.logged(t)) != 0 {
		t.Fatal("--print wrote the log")
	}
	if _, err := os.Stat(f.state); !os.IsNotExist(err) {
		t.Fatal("--print wrote state")
	}
}

func TestCursorUsageSyncJSONAndSince(t *testing.T) {
	f := newCursorUsageFixture(t, "dev@example.com")
	f.api.events = append(f.api.events, cursorAdminEvent(cursorUsageTestNow.Add(-20*24*time.Hour), 5, 1))
	out, err := f.run(t, "sync", "--json", "--since", "2026-09-01")
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Emitted     int    `json:"emitted"`
		WindowStart string `json:"window_start"`
	}
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("json output %q: %v", out, err)
	}
	if summary.Emitted != 3 {
		t.Fatalf("summary = %+v", summary)
	}
	if _, err := f.run(t, "sync", "--since", "last tuesday"); err == nil || !strings.Contains(err.Error(), "--since") {
		t.Fatalf("bad since err = %v", err)
	}
}

func TestCursorUsageSyncRefusesPlainHTTPToARemoteHost(t *testing.T) {
	f := newCursorUsageFixture(t, "dev@example.com")
	f.url = "http://api.cursor.com"
	if _, err := f.run(t, "sync"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v", err)
	}
}

func TestCursorUsageStatus(t *testing.T) {
	f := newCursorUsageFixture(t, "dev@example.com")
	out, err := f.run(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "CURSOR_ADMIN_API_KEY is set") || !strings.Contains(out, "Scope: email:dev@example.com") || !strings.Contains(out, "No Cursor usage collected yet") {
		t.Fatalf("status = %q", out)
	}
	if _, err := f.run(t, "sync"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(defaultCursorAdminKeyEnv, "")
	out, err = f.run(t, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report cursorUsageStatusReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.APIKeySet || report.DefaultScope != "email:dev@example.com" || len(report.Scopes) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Scopes[0].CollectedTo != cursorUsageTestNow.Format(time.RFC3339) {
		t.Fatalf("collected_to = %q", report.Scopes[0].CollectedTo)
	}
	// The signed-in lookup reads the email row only; the access token in the same table never
	// reaches any output.
	if strings.Contains(out, "cursor-session-token") {
		t.Fatal("status leaked Cursor's access token")
	}
}

func TestCursorUsageStatusWithoutAScope(t *testing.T) {
	f := newCursorUsageFixture(t, "")
	out, err := f.run(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Scope: none") {
		t.Fatalf("status = %q", out)
	}
}
