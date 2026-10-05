package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/mcpconnect"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// The account token a signed-in user has. No command in this file may ever write or print it.
const fakeAccountToken = "bcn_cli_ACCOUNT_TOKEN_MUST_NOT_LEAK_7f3a9c"

// requestLog records every HTTP request the commands make, on any transport.
type requestLog struct {
	mu   sync.Mutex
	urls []string
}

func (l *requestLog) add(u string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.urls = append(l.urls, u)
}

func (l *requestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.urls...)
}

type recordingTransport struct {
	log  *requestLog
	next http.RoundTripper
}

func (r recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.log.add(req.URL.String())
	if r.next == nil {
		return nil, errors.New("network access is not allowed here")
	}
	return r.next.RoundTrip(req)
}

type mcpFixture struct {
	home     string
	server   *httptest.Server
	url      string
	requests *requestLog
	// strays records requests that went anywhere but mcpHTTPClient.
	strays *requestLog
	// auth records the Authorization header of every request to the MCP endpoint itself, which
	// only a real harness makes.
	auth *requestLog
}

// newMCPFixture isolates the home directory, stubs discovery and the terminal, serves resource
// metadata for the MCP URL, and blocks every other HTTP request made in-process.
func newMCPFixture(t *testing.T, detected ...string) *mcpFixture {
	t.Helper()
	fx := &mcpFixture{home: t.TempDir(), requests: &requestLog{}, strays: &requestLog{}, auth: &requestLog{}}
	testenv.SetHome(t, fx.home)
	for _, key := range []string{"XDG_CONFIG_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE", "CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "JENKINS_URL", "TEAMCITY_VERSION"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	fx.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			fx.auth.add(r.Header.Get("Authorization"))
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+fx.server.URL+`/.well-known/oauth-protected-resource"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/.well-known/oauth-protected-resource" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": fx.url, "authorization_servers": []string{fx.server.URL}})
	}))
	t.Cleanup(fx.server.Close)
	fx.url = fx.server.URL + "/mcp"

	// A signed-in Beacon account, whose token must never reach a harness config or the output.
	sessionPath := filepath.Join(fx.home, ".beacon", "auth", "session.json")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	session := `{"schema_version":1,"base_url":"https://beacon.sh","access_token":"` + fakeAccountToken + `","token_type":"Bearer","scopes":["profile:read","device:enroll"]}`
	if err := os.WriteFile(sessionPath, []byte(session), 0o600); err != nil {
		t.Fatal(err)
	}

	prev := struct {
		discover     func() []harness.Harness
		tty, root    func() bool
		stdin        io.Reader
		client       *http.Client
		lookPath     func(string) (string, error)
		run          func(context.Context, string, ...string) ([]byte, error)
		ingest       func() string
		defTransport http.RoundTripper
	}{mcpDiscover, mcpIsTTY, mcpIsRoot, mcpStdin, mcpHTTPClient, mcpLookPath, mcpRunCLI, mcpIngestURL, http.DefaultTransport}
	t.Cleanup(func() {
		mcpDiscover, mcpIsTTY, mcpIsRoot, mcpStdin, mcpHTTPClient = prev.discover, prev.tty, prev.root, prev.stdin, prev.client
		mcpLookPath, mcpRunCLI, mcpIngestURL, http.DefaultTransport = prev.lookPath, prev.run, prev.ingest, prev.defTransport
	})
	mcpDiscover = func() []harness.Harness {
		var out []harness.Harness
		for _, name := range detected {
			out = append(out, harness.Harness{Name: name, DisplayName: name, Detected: true})
		}
		return out
	}
	mcpIsTTY = func() bool { return true }
	mcpIsRoot = func() bool { return false }
	mcpStdin = strings.NewReader("")
	mcpHTTPClient = &http.Client{Transport: recordingTransport{log: fx.requests, next: fx.server.Client().Transport}}
	// Anything that bypasses mcpHTTPClient is recorded and refused.
	http.DefaultTransport = recordingTransport{log: fx.strays}
	mcpLookPath = func(string) (string, error) { return "", errors.New("not on PATH") }
	mcpRunCLI = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("no CLI in unit tests")
	}
	mcpIngestURL = func() string { return "" }
	return fx
}

// runMCP runs `beacon mcp <args>` and returns stdout and stderr.
func runMCP(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var reset func(c *cobra.Command)
	reset = func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace(nil)
			} else {
				_ = f.Value.Set(f.DefValue)
			}
			f.Changed = false
		})
		for _, sub := range c.Commands() {
			reset(sub)
		}
	}
	reset(mcpCmd)
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(append([]string{"mcp"}, args...))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	return stdout.String(), stderr.String(), err
}

// filesUnder lists every regular file under dir except the seeded account session.
func filesUnder(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == filepath.Join(".beacon", "auth", "session.json") {
			return nil
		}
		data, _ := os.ReadFile(path)
		out[rel] = string(data)
		return nil
	})
	return out
}

var allAutomatic = []string{"claude_code", "codex_cli", "cursor", "vscode", "gemini_cli", "opencode", "omp"}

func TestMCPConnectSubcommandsAreRegistered(t *testing.T) {
	for _, sub := range []string{"connect", "disconnect", "status"} {
		c, _, err := rootCmd.Find([]string{"mcp", sub})
		if err != nil || c.Name() != sub {
			t.Fatalf("mcp %s not registered: %v", sub, err)
		}
	}
	c, _, _ := rootCmd.Find([]string{"mcp", "connect"})
	for _, flag := range []string{"harness", "url", "token-env", "dry-run", "yes", "force"} {
		if c.Flags().Lookup(flag) == nil {
			t.Errorf("mcp connect has no --%s", flag)
		}
	}
	c, _, _ = rootCmd.Find([]string{"mcp", "status"})
	for _, flag := range []string{"harness", "check", "json", "url"} {
		if c.Flags().Lookup(flag) == nil {
			t.Errorf("mcp status has no --%s", flag)
		}
	}
}

func TestMCPConnectRefusesUnattendedRuns(t *testing.T) {
	cases := []struct {
		name    string
		arrange func(t *testing.T)
		args    []string
	}{
		{"not a terminal", func(*testing.T) { mcpIsTTY = func() bool { return false } }, nil},
		{"CI", func(t *testing.T) { t.Setenv("CI", "true") }, nil},
		{"GitHub Actions", func(t *testing.T) { t.Setenv("GITHUB_ACTIONS", "true") }, nil},
		{"root", func(*testing.T) { mcpIsRoot = func() bool { return true } }, nil},
		{"root even with --yes", func(*testing.T) { mcpIsRoot = func() bool { return true } }, []string{"--yes"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newMCPFixture(t, allAutomatic...)
			tc.arrange(t)
			_, _, err := runMCP(t, append([]string{"connect", "--url", fx.url}, tc.args...)...)
			if err == nil {
				t.Fatal("connect ran unattended")
			}
			if got := filesUnder(t, fx.home); len(got) != 0 {
				t.Fatalf("a refused connect wrote %v", got)
			}
			if reqs := fx.requests.all(); len(reqs) != 0 {
				t.Fatalf("a refused connect made requests: %v", reqs)
			}
		})
	}
}

func TestMCPConnectDryRunTouchesNothingAndMakesNoRequest(t *testing.T) {
	fx := newMCPFixture(t, allAutomatic...)
	mcpIsTTY = func() bool { return false }
	stdout, _, err := runMCP(t, "connect", "--url", fx.url, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"not checked (--dry-run)", "Dry run: nothing was written.", "Claude Code", "Codex CLI", "add"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, stdout)
		}
	}
	if got := filesUnder(t, fx.home); len(got) != 0 {
		t.Fatalf("dry run wrote %v", got)
	}
	if reqs := append(fx.requests.all(), fx.strays.all()...); len(reqs) != 0 {
		t.Fatalf("dry run made requests: %v", reqs)
	}
}

func TestMCPConnectWritesEveryDetectedHarnessThenDisconnectRestores(t *testing.T) {
	fx := newMCPFixture(t, append(allAutomatic, "copilot_cli", "pi_cli")...)
	cursorPath := filepath.Join(fx.home, ".cursor", "mcp.json")
	original := "{\n  // mine\n  \"mcpServers\": {\n    \"x\": {\"url\": \"https://x\"}\n  }\n}\n"
	if err := os.MkdirAll(filepath.Dir(cursorPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cursorPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runMCP(t, "connect", "--url", fx.url, "--yes")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, stdout, stderr)
	}
	for _, want := range []string{
		"(checked)", "Server name: beacon-managed", "Auth: OAuth",
		"✓ Claude Code", "✓ Codex CLI", "✓ Cursor", "✓ VS Code", "✓ Gemini CLI", "✓ OpenCode", "✓ Oh My Pi",
		"codex mcp login beacon-managed", "opencode mcp auth beacon-managed", "run /mcp", "/mcp reauth beacon-managed",
		"Add by hand", "copilot mcp add --transport http beacon-managed " + fx.url,
		"Not connected (Beacon does not know how these configure MCP servers): pi_cli",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	statusOut, _, err := runMCP(t, "status", "--url", fx.url, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report mcpStatusReport
	if err := json.Unmarshal([]byte(statusOut), &report); err != nil {
		t.Fatalf("%v\n%s", err, statusOut)
	}
	configured := map[string]bool{}
	for _, row := range report.Harnesses {
		if row.Configured && row.ByBeacon && row.URL == fx.url && row.Auth == "oauth" {
			configured[row.Harness] = true
		}
	}
	for _, name := range allAutomatic {
		if !configured[name] {
			t.Errorf("status does not show %s configured by Beacon: %+v", name, report.Harnesses)
		}
	}

	again, _, err := runMCP(t, "connect", "--url", fx.url, "--yes")
	if err != nil || !strings.Contains(again, "Nothing to change.") {
		t.Fatalf("a second connect was not a no-op: %v\n%s", err, again)
	}

	if _, _, err := runMCP(t, "disconnect"); err != nil {
		t.Fatal(err)
	}
	left := filesUnder(t, fx.home)
	for path := range left {
		if strings.Contains(path, ".beacon.") && strings.HasSuffix(path, ".bak") {
			delete(left, path) // backups stay, by design
		}
	}
	if len(left) != 1 || left[filepath.Join(".cursor", "mcp.json")] != original {
		t.Fatalf("disconnect did not return the home directory to its original state: %v", left)
	}
}

func TestMCPConnectAsksBeforeWriting(t *testing.T) {
	fx := newMCPFixture(t, "cursor")
	mcpStdin = strings.NewReader("n\n")
	stdout, _, err := runMCP(t, "connect", "--url", fx.url)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "[y/N]") || !strings.Contains(stdout, "Nothing was written.") {
		t.Fatalf("output:\n%s", stdout)
	}
	if got := filesUnder(t, fx.home); len(got) != 0 {
		t.Fatalf("declining wrote %v", got)
	}
	mcpStdin = strings.NewReader("y\n")
	if _, _, err := runMCP(t, "connect", "--url", fx.url); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fx.home, ".cursor", "mcp.json")); err != nil {
		t.Fatal("confirming did not write")
	}
}

// The Beacon account token (bcn_cli_) must never be copied anywhere: not into a harness config,
// the manifest, a backup, or the command's output.
func TestMCPCommandsNeverCopyTheAccountToken(t *testing.T) {
	for _, mode := range [][]string{nil, {"--token-env", "BEACON_MCP_TOKEN"}} {
		fx := newMCPFixture(t, append(allAutomatic, "copilot_cli", "cline", "kiro")...)
		t.Setenv("BEACON_MCP_TOKEN", "bcn_mcp_value_must_not_be_written")
		var outputs []string
		for _, args := range [][]string{
			append([]string{"connect", "--url", fx.url, "--yes"}, mode...),
			{"status", "--url", fx.url, "--check"},
			{"status", "--url", fx.url, "--json"},
			{"disconnect"},
		} {
			stdout, stderr, err := runMCP(t, args...)
			if err != nil {
				t.Fatalf("%v: %v\n%s%s", args, err, stdout, stderr)
			}
			outputs = append(outputs, stdout, stderr)
			for path, content := range filesUnder(t, fx.home) {
				if strings.Contains(content, fakeAccountToken) || strings.Contains(content, "bcn_cli_") {
					t.Fatalf("%v copied the account token into %s", args, path)
				}
				if strings.Contains(content, "bcn_mcp_value_must_not_be_written") {
					t.Fatalf("%v wrote the MCP token value into %s", args, path)
				}
			}
		}
		for _, out := range outputs {
			if strings.Contains(out, fakeAccountToken) || strings.Contains(out, "bcn_mcp_value_must_not_be_written") {
				t.Fatalf("a token was printed:\n%s", out)
			}
		}
	}
}

func TestMCPConnectMakesNoRequestBeyondTheURLCheck(t *testing.T) {
	fx := newMCPFixture(t, allAutomatic...)
	if _, _, err := runMCP(t, "connect", "--url", fx.url, "--yes"); err != nil {
		t.Fatal(err)
	}
	reqs := fx.requests.all()
	if len(reqs) != 1 || reqs[0] != fx.server.URL+"/.well-known/oauth-protected-resource" {
		t.Fatalf("connect made %v, want exactly the resource-metadata request", reqs)
	}
	if strays := fx.strays.all(); len(strays) != 0 {
		t.Fatalf("connect made requests outside the URL check: %v", strays)
	}
	// Status without --check, and disconnect, make none at all.
	for _, args := range [][]string{{"status", "--url", fx.url}, {"disconnect"}} {
		if _, _, err := runMCP(t, args...); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(fx.requests.all()) + len(fx.strays.all()); got != 1 {
		t.Fatalf("status or disconnect made a request (%d total)", got)
	}
}

func TestMCPConnectURLFailuresWriteNothing(t *testing.T) {
	fx := newMCPFixture(t, "cursor")
	cases := map[string][]string{
		"no URL anywhere":         {"connect", "--yes"},
		"URL the server disowns":  {"connect", "--yes", "--url", fx.server.URL + "/wrong"},
		"plain http remote":       {"connect", "--yes", "--url", "http://mcp.example.test/mcp"},
		"token value as env name": {"connect", "--yes", "--url", fx.url, "--token-env", "bcn_mcp_abc123"},
		"unknown harness":         {"connect", "--yes", "--url", fx.url, "--harness", "nope"},
	}
	for name, args := range cases {
		_, _, err := runMCP(t, args...)
		if err == nil {
			t.Errorf("%s: connect succeeded", name)
		}
		if name == "no URL anywhere" && (err == nil || !strings.Contains(err.Error(), "beacon endpoint connect")) {
			t.Errorf("the no-URL error does not point at beacon endpoint connect: %v", err)
		}
	}
	if got := filesUnder(t, fx.home); len(got) != 0 {
		t.Fatalf("a failed connect wrote %v", got)
	}
}

func TestMCPConnectDerivesTheURLFromTheManagedConnection(t *testing.T) {
	fx := newMCPFixture(t, "cursor")
	mcpIngestURL = func() string { return fx.server.URL }
	stdout, _, err := runMCP(t, "connect", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Beacon Cloud MCP: "+fx.url+" (checked)") {
		t.Fatalf("output:\n%s", stdout)
	}
}

func TestMCPConnectHarnessFlagAcceptsEndpointSpellings(t *testing.T) {
	fx := newMCPFixture(t) // nothing detected
	stdout, _, err := runMCP(t, "connect", "--url", fx.url, "--yes", "--harness", "Codex-CLI,claude,vs-code")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"✓ Codex CLI", "✓ Claude Code", "✓ VS Code"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Cursor") {
		t.Error("--harness connected a harness it did not name")
	}
}

func TestMCPConnectReportsConflictsAndForceReplaces(t *testing.T) {
	fx := newMCPFixture(t, "cursor")
	path := filepath.Join(fx.home, ".cursor", "mcp.json")
	theirs := `{"mcpServers": {"beacon-managed": {"url": "https://elsewhere.example"}}}`
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(theirs), 0o644)
	stdout, _, err := runMCP(t, "connect", "--url", fx.url, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "conflict") || !strings.Contains(stdout, "https://elsewhere.example") {
		t.Fatalf("output:\n%s", stdout)
	}
	if data, _ := os.ReadFile(path); string(data) != theirs {
		t.Fatal("a conflict was overwritten")
	}
	stdout, _, err = runMCP(t, "connect", "--url", fx.url, "--yes", "--force")
	if err != nil || !strings.Contains(stdout, "replaced in") {
		t.Fatalf("--force: %v\n%s", err, stdout)
	}
}

func TestMCPStatusCheckReportsAFailedCheck(t *testing.T) {
	fx := newMCPFixture(t, "cursor")
	stdout, _, err := runMCP(t, "status", "--url", fx.server.URL+"/wrong", "--check")
	if err == nil || !strings.Contains(stdout, "URL check: failed") {
		t.Fatalf("err = %v\n%s", err, stdout)
	}
	stdout, _, err = runMCP(t, "status", "--url", fx.url, "--check")
	if err != nil || !strings.Contains(stdout, "URL check: ok") || !strings.Contains(stdout, "Cursor") {
		t.Fatalf("err = %v\n%s", err, stdout)
	}
}

func TestMCPDisconnectWithNothingToRemove(t *testing.T) {
	newMCPFixture(t, allAutomatic...)
	stdout, _, err := runMCP(t, "disconnect")
	if err != nil || !strings.Contains(stdout, "Nothing to remove") {
		t.Fatalf("err = %v\n%s", err, stdout)
	}
}

func TestMCPConnectTokenEnvPrintsTheVariable(t *testing.T) {
	fx := newMCPFixture(t, "codex_cli")
	stdout, _, err := runMCP(t, "connect", "--url", fx.url, "--yes", "--token-env", "BEACON_MCP_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "read from $BEACON_MCP_TOKEN") || !strings.Contains(stdout, "export it as BEACON_MCP_TOKEN") {
		t.Fatalf("output:\n%s", stdout)
	}
	data, _ := os.ReadFile(filepath.Join(fx.home, ".codex", "config.toml"))
	if !strings.Contains(string(data), `bearer_token_env_var = "BEACON_MCP_TOKEN"`) {
		t.Fatalf("config.toml:\n%s", data)
	}
}

func TestMCPServerNamesDoNotCollide(t *testing.T) {
	if mcpconnect.ServerName == "beacon" {
		t.Fatal("the Beacon Cloud server must not share the local stdio server's name")
	}
}

func TestInstallSuggestsMCPConnectOnlyAfterTheWizardChoseManaged(t *testing.T) {
	var buf bytes.Buffer
	suggestMCPConnect(&buf, false)
	if buf.Len() != 0 {
		t.Fatalf("an unattended or --connect install printed %q", buf.String())
	}
	suggestMCPConnect(&buf, true)
	if !strings.Contains(buf.String(), "beacon mcp connect") {
		t.Fatalf("got %q", buf.String())
	}
}

// Regression: the backend names a canonical MCP URL different from the ingest URL plus /mcp, and
// connect writes the canonical one. status without --check makes no request, so it must compare
// against the URL connect wrote, not the derived one.
func TestMCPStatusWithoutCheckUsesTheURLConnectWrote(t *testing.T) {
	fx := newMCPFixture(t, "cursor")
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": fx.url})
	}))
	t.Cleanup(ingest.Close)
	mcpIngestURL = func() string { return ingest.URL }
	if _, _, err := runMCP(t, "connect", "--yes"); err != nil {
		t.Fatal(err)
	}
	afterConnect := len(fx.requests.all())
	stdout, _, err := runMCP(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Beacon Cloud MCP URL: "+fx.url) || strings.Contains(stdout, "other URL") {
		t.Fatalf("status compared against the derived URL:\n%s", stdout)
	}
	if n := len(fx.requests.all()); n != afterConnect {
		t.Fatalf("status made a request without --check (%d after connect, %d after status)", afterConnect, n)
	}
}

func TestMCPStatusFallsBackToTheRecordedURLWithoutAnEnrollment(t *testing.T) {
	fx := newMCPFixture(t, "cursor")
	if _, _, err := runMCP(t, "connect", "--url", fx.url, "--yes"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runMCP(t, "status")
	if err != nil || !strings.Contains(stdout, "Beacon Cloud MCP URL: "+fx.url) || strings.Contains(stdout, "other URL") {
		t.Fatalf("err = %v\n%s", err, stdout)
	}
}

// Regression: after `connect --url` on an endpoint with no enrollment, `status --check` must check
// the URL connect wrote rather than fail for want of one.
func TestMCPStatusCheckUsesTheRecordedURLWithoutAnEnrollment(t *testing.T) {
	fx := newMCPFixture(t, "cursor")
	if _, _, err := runMCP(t, "connect", "--url", fx.url, "--yes"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runMCP(t, "status", "--check")
	if err != nil || !strings.Contains(stdout, "URL check: ok") || !strings.Contains(stdout, "Beacon Cloud MCP URL: "+fx.url) {
		t.Fatalf("err = %v\n%s", err, stdout)
	}
	// With nothing recorded and no enrollment, --check still fails, and says why.
	if _, _, err := runMCP(t, "disconnect"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runMCP(t, "status", "--check")
	if err == nil || !strings.Contains(stdout, "URL check: failed") {
		t.Fatalf("err = %v\n%s", err, stdout)
	}
}

func TestMCPDisconnectDoesNotSayNothingToRemoveAfterReportingAnEntry(t *testing.T) {
	fx := newMCPFixture(t, "cursor")
	if _, _, err := runMCP(t, "connect", "--url", fx.url, "--yes"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fx.home, ".cursor", "mcp.json")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), fx.url, "https://theirs.example", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runMCP(t, "disconnect")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "left alone") || strings.Contains(stdout, "Nothing to remove") {
		t.Fatalf("output:\n%s", stdout)
	}
}
