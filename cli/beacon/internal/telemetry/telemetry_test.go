package telemetry

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

const testVersion = "1.3.60"

// sink is a stand-in for the PostHog batch endpoint that keeps every message it receives.
type sink struct {
	mu       sync.Mutex
	messages []map[string]any
	paths    []string
}

func (s *sink) all() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.messages...)
}

func (s *sink) byEvent(event string) []map[string]any {
	var out []map[string]any
	for _, msg := range s.all() {
		if msg["event"] == event {
			out = append(out, msg)
		}
	}
	return out
}

// setup points telemetry at a fresh home and a local sink, with an environment that has only what
// the test gives it.
func setup(t *testing.T, env map[string]string) (*sink, string) {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	s := &sink{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch struct {
			Batch []map[string]any `json:"batch"`
		}
		_ = json.Unmarshal(body, &batch)
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.messages = append(s.messages, batch.Batch...)
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	oldEndpoint := endpoint
	endpoint = server.URL + "/ingest"
	t.Cleanup(func() {
		Close()
		endpoint = oldEndpoint
	})
	return s, home
}

func options(env map[string]string, stderr io.Writer) Options {
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	return Options{
		Version:     testVersion,
		InstallMode: InstallModeUser,
		Getenv:      func(key string) string { return env[key] },
		Environ:     func() []string { return environ },
		Stderr:      stderr,
	}
}

func properties(t *testing.T, msg map[string]any) map[string]any {
	t.Helper()
	props, ok := msg["properties"].(map[string]any)
	if !ok {
		t.Fatalf("message has no properties: %v", msg)
	}
	return props
}

func TestOptedOut(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{}, false},
		{map[string]string{EnvTelemetry: "1"}, false},
		{map[string]string{EnvTelemetry: "0"}, true},
		{map[string]string{EnvTelemetry: "false"}, true},
		{map[string]string{EnvTelemetry: " OFF "}, true},
		{map[string]string{EnvTelemetry: "no"}, true},
		{map[string]string{EnvDoNotTrack: "1"}, true},
		{map[string]string{EnvDoNotTrack: "true"}, true},
		{map[string]string{EnvDoNotTrack: "0"}, false},
		{map[string]string{EnvDoNotTrack: "false"}, false},
	}
	for _, tc := range cases {
		if got := OptedOut(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("OptedOut(%v) = %v, want %v", tc.env, got, tc.want)
		}
	}
}

func TestInvokingAgent(t *testing.T) {
	cases := []struct {
		environ []string
		want    string
	}{
		{nil, AgentNone},
		{[]string{"CLAUDECODE=1"}, AgentClaudeCode},
		{[]string{"CLAUDE_CODE_ENTRYPOINT=cli"}, AgentClaudeCode},
		{[]string{"CURSOR_AGENT=1"}, AgentCursor},
		// Every Cursor terminal has a trace ID, typed in by a person or not.
		{[]string{"CURSOR_TRACE_ID=abc"}, AgentNone},
		{[]string{"CODEX_SANDBOX=seatbelt"}, AgentCodex},
		{[]string{"CODEX_THREAD_ID=019a"}, AgentCodex},
		{[]string{"CODEX_MANAGED_BY_NPM=1"}, AgentCodex},
		{[]string{"CODEX_HOME=/tmp/codex"}, AgentNone},
		{[]string{"GEMINI_CLI=1"}, AgentGeminiCLI},
		{[]string{"GEMINI_CLI_HOME=/tmp/g"}, AgentNone},
		{[]string{"OPENCODE=1"}, AgentOpenCode},
		{[]string{"OPENCODE_DB=/tmp/db"}, AgentNone},
		{[]string{"CLINE_ACTIVE=true"}, AgentCline},
		{[]string{"CLINE_DIR=/tmp/cline"}, AgentNone},
		{[]string{"AI_AGENT=Amp Code"}, "amp_code"},
		{[]string{"AI_AGENT=/Users/someone/x@y"}, "userssomeonexy"},
		{[]string{"AI_AGENT=none"}, AgentNone},
		{[]string{"AI_AGENT=" + strings.Repeat("a", 80)}, strings.Repeat("a", maxAgentNameLen)},
		{[]string{"AI_AGENT=amp", "CLAUDECODE=1"}, AgentClaudeCode},
		{[]string{"CLAUDECODE="}, AgentNone},
	}
	for _, tc := range cases {
		if got := InvokingAgent(tc.environ); got != tc.want {
			t.Errorf("InvokingAgent(%v) = %q, want %q", tc.environ, got, tc.want)
		}
	}
}

func TestCompanyDomain(t *testing.T) {
	cases := map[string]string{
		"Ada@Example.COM ":    "example.com",
		"someone@gmail.com":   "",
		"someone@gmx.de":      "",
		"someone@gmx.net":     "",
		"someone@hotmail.fr":  "",
		"someone@acme.co.uk":  "acme.co.uk",
		"not-an-email":        "",
		"someone@":            "",
		"someone@t-online.de": "",
	}
	for email, want := range cases {
		if got := CompanyDomain(email); got != want {
			t.Errorf("CompanyDomain(%q) = %q, want %q", email, got, want)
		}
	}
	if got, want := DistinctIDForEmail(" Ada@Example.com"), DistinctIDForEmail("ada@example.com"); got != want {
		t.Fatalf("distinct ID depends on case or blanks: %s vs %s", got, want)
	}
	// sha256("ada@example.com"), the ID the websites compute for the same person.
	if got := DistinctIDForEmail("ada@example.com"); got != "b5fc85e55755f9e0d030a10ab4429b6b2944855f9a0d60077fe832becbc41d72" {
		t.Fatalf("distinct ID = %q, want the hex SHA-256 of the address", got)
	}
}

func TestDevelopmentBuildSendsAndWritesNothing(t *testing.T) {
	s, home := setup(t, nil)
	var stderr bytes.Buffer
	opts := options(nil, &stderr)
	opts.Version = "dev"
	Init(opts)
	if Enabled() {
		t.Fatal("telemetry is on in a development build")
	}
	Capture("cli_test", nil)
	Identify("ada@example.com", "")
	Close()
	if msgs := s.all(); len(msgs) != 0 {
		t.Fatalf("a development build sent %d messages", len(msgs))
	}
	if stderr.Len() != 0 {
		t.Fatalf("a development build printed %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".beacon")); !os.IsNotExist(err) {
		t.Fatalf("a development build wrote to ~/.beacon: %v", err)
	}
}

func TestOptOutSendsAndWritesNothing(t *testing.T) {
	for _, env := range []map[string]string{{EnvTelemetry: "0"}, {EnvDoNotTrack: "1"}} {
		s, home := setup(t, env)
		var stderr bytes.Buffer
		Init(options(env, &stderr))
		Capture("cli_test", nil)
		Close()
		if len(s.all()) != 0 || stderr.Len() != 0 {
			t.Fatalf("%v: sent %d messages, printed %q", env, len(s.all()), stderr.String())
		}
		if _, err := os.Stat(filepath.Join(home, ".beacon")); !os.IsNotExist(err) {
			t.Fatalf("%v: wrote to ~/.beacon: %v", env, err)
		}
	}
}

func TestInitSavesTheInstallIDAndPrintsTheNoticeOnce(t *testing.T) {
	_, home := setup(t, nil)
	var stderr bytes.Buffer
	Init(options(nil, &stderr))
	if got := strings.Count(stderr.String(), Notice); got != 1 {
		t.Fatalf("first run printed the notice %d times: %q", got, stderr.String())
	}
	profile := onboarding.Load()
	if profile.InstallID == "" {
		t.Fatal("install ID was not saved")
	}
	if profile.Prompted() {
		t.Fatal("saving the install ID marked the machine onboarded")
	}
	if testenv.HasPOSIXFileModes() {
		for _, path := range []string{onboarding.Path(), filepath.Join(home, StatePath)} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %v, want 0600", path, info.Mode().Perm())
			}
		}
	}

	stderr.Reset()
	Init(options(nil, &stderr))
	if stderr.Len() != 0 {
		t.Fatalf("second run printed %q", stderr.String())
	}
	if again := onboarding.Load(); again.InstallID != profile.InstallID {
		t.Fatalf("install ID changed from %s to %s", profile.InstallID, again.InstallID)
	}
}

func TestCaptureSendsSuperPropertiesAsTheInstall(t *testing.T) {
	env := map[string]string{"CI": "true", "CLAUDECODE": "1"}
	s, _ := setup(t, env)
	opts := options(env, io.Discard)
	opts.InstallMode = InstallModeSystem
	Init(opts)
	Capture("cli_test", map[string]any{"count": 3})
	Close()

	msgs := s.byEvent("cli_test")
	if len(msgs) != 1 {
		t.Fatalf("got %d cli_test events, want 1", len(msgs))
	}
	if s.paths[0] != "/ingest/batch/" {
		t.Fatalf("posted to %s", s.paths[0])
	}
	installID := onboarding.Load().InstallID
	if msgs[0]["distinct_id"] != installID {
		t.Fatalf("distinct_id = %v, want the install ID %s", msgs[0]["distinct_id"], installID)
	}
	props := properties(t, msgs[0])
	want := map[string]any{
		PropProduct:              ProductBeacon,
		PropSurface:              SurfaceCLI,
		PropBeaconVersion:        testVersion,
		PropInstallMode:          InstallModeSystem,
		PropIsCI:                 true,
		PropInvokingAgent:        AgentClaudeCode,
		propProcessPersonProfile: false,
		"count":                  float64(3),
	}
	for key, value := range want {
		if props[key] != value {
			t.Errorf("%s = %v, want %v", key, props[key], value)
		}
	}
	for _, key := range []string{PropOS, PropArch, PropInstallMethod} {
		if s, _ := props[key].(string); s == "" {
			t.Errorf("%s is empty", key)
		}
	}
	if _, ok := msgs[0]["$groups"]; ok {
		t.Errorf("an anonymous event has groups: %v", msgs[0]["$groups"])
	}
	assertNoPII(t, s.all())
}

func TestIdentifyAliasesTheInstallAndSetsThePerson(t *testing.T) {
	s, home := setup(t, nil)
	Init(options(nil, io.Discard))
	installID := onboarding.Load().InstallID
	Identify(" Ada@Example.com ", "user_123")
	Capture("cli_after", nil)
	Close()

	id := DistinctIDForEmail("ada@example.com")
	aliases := s.byEvent("$create_alias")
	if len(aliases) != 1 {
		t.Fatalf("got %d aliases, want 1", len(aliases))
	}
	aliasProps := properties(t, aliases[0])
	if aliasProps["distinct_id"] != id || aliasProps["alias"] != installID {
		t.Fatalf("alias = %v, want distinct_id %s alias %s", aliasProps, id, installID)
	}

	sets := s.byEvent("$set")
	if len(sets) != 1 {
		t.Fatalf("got %d $set events, want 1", len(sets))
	}
	if sets[0]["distinct_id"] != id {
		t.Fatalf("$set distinct_id = %v", sets[0]["distinct_id"])
	}
	setProps := properties(t, sets[0])
	set, _ := setProps["$set"].(map[string]any)
	if set[PersonEmail] != "ada@example.com" || set[PersonHasBeacon] != true || set[PersonBeaconUserID] != "user_123" {
		t.Fatalf("$set = %v", set)
	}
	setOnce, _ := setProps["$set_once"].(map[string]any)
	if setOnce[PersonFirstProduct] != ProductBeacon || setOnce[PersonFirstSurface] != SurfaceCLI {
		t.Fatalf("$set_once = %v", setOnce)
	}

	after := s.byEvent("cli_after")
	if len(after) != 1 || after[0]["distinct_id"] != id {
		t.Fatalf("event after identify = %v", after)
	}
	props := properties(t, after[0])
	if _, ok := props[propProcessPersonProfile]; ok {
		t.Fatal("an identified event is still personless")
	}
	if groups, _ := props["$groups"].(map[string]any); groups[GroupCompany] != "example.com" {
		t.Fatalf("$groups = %v", props["$groups"])
	}
	assertNoPII(t, s.all())

	// The next run is still the person, without aliasing again.
	st := loadState(home)
	if st.PHDistinctID != id || st.Company != "example.com" {
		t.Fatalf("state = %+v", st)
	}
	s2, _ := setupSink(t)
	Init(options(nil, io.Discard))
	Capture("cli_next", nil)
	Identify("ada@example.com", "user_123")
	Close()
	if len(s2.byEvent("$create_alias")) != 0 {
		t.Fatal("identified again under the same email and aliased twice")
	}
	if next := s2.byEvent("cli_next"); len(next) != 1 || next[0]["distinct_id"] != id {
		t.Fatalf("next run's event = %v", next)
	}
}

func TestIdentifyWithAPersonalEmailHasNoCompany(t *testing.T) {
	s, _ := setup(t, nil)
	Init(options(nil, io.Discard))
	Identify("someone@gmail.com", "")
	Capture("cli_after", nil)
	Close()
	after := s.byEvent("cli_after")
	if len(after) != 1 {
		t.Fatalf("got %d events", len(after))
	}
	if groups, ok := properties(t, after[0])["$groups"]; ok {
		t.Fatalf("a gmail address got groups %v", groups)
	}
	set, _ := properties(t, s.byEvent("$set")[0])["$set"].(map[string]any)
	if _, ok := set[PersonBeaconUserID]; ok {
		t.Fatal("beacon_user_id set without a signed-in account")
	}
}

func TestCloseIsCappedWhenTheEndpointHangs(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	oldEndpoint := endpoint
	endpoint = server.URL
	t.Cleanup(func() { endpoint = oldEndpoint })

	Init(options(nil, io.Discard))
	Capture("cli_test", nil)
	start := time.Now()
	Close()
	if elapsed := time.Since(start); elapsed > FlushTimeout+time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
}

func TestCaptureWithoutInitIsANoOp(t *testing.T) {
	Close()
	Capture("cli_test", nil)
	Identify("ada@example.com", "")
	if Enabled() {
		t.Fatal("enabled without Init")
	}
}

// setupSink replaces only the endpoint, keeping the home of an earlier setup.
func setupSink(t *testing.T) (*sink, string) {
	t.Helper()
	s := &sink{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch struct {
			Batch []map[string]any `json:"batch"`
		}
		_ = json.Unmarshal(body, &batch)
		s.mu.Lock()
		s.messages = append(s.messages, batch.Batch...)
		s.mu.Unlock()
	}))
	t.Cleanup(server.Close)
	endpoint = server.URL
	return s, ""
}

// assertNoPII fails when any property outside a $set holds a path or an email.
func assertNoPII(t *testing.T, msgs []map[string]any) {
	t.Helper()
	var walk func(path string, value any)
	walk = func(path string, value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if key == propSet {
					continue
				}
				walk(path+"."+key, child)
			}
		case []any:
			for _, child := range v {
				walk(path, child)
			}
		case string:
			for _, needle := range []string{"/Users/", "/home/", "@", `\Users\`} {
				if strings.Contains(v, needle) {
					t.Errorf("%s = %q contains %q", path, v, needle)
				}
			}
		}
	}
	for _, msg := range msgs {
		walk(msg["event"].(string), msg["properties"])
	}
}
