package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/activesession"
)

// shareCurrentEnv adds a second, older Claude Code session and a Gemini CLI session (log only) in
// the project directory, so each resolution step can be told apart from "most recent".
func shareCurrentEnv(t *testing.T) *shareEnv {
	t.Helper()
	env := newShareEnv(t)
	recordPath := filepath.Join(env.home, "active.json")
	prev := shareActiveSessionsPath
	shareActiveSessionsPath = func() string { return recordPath }
	t.Cleanup(func() { shareActiveSessionsPath = prev })
	gem := schema.NewEvent(schema.NewEventOptions{Action: "prompt.submitted", Category: "prompt", Harness: schema.HarnessInfo{Name: "gemini_cli", CollectionMethod: schema.CollectionMethodHook}})
	gem.Timestamp = schema.FormatTimestamp(time.Now().Add(-time.Hour))
	gem.Session = &schema.SessionInfo{ID: "gem-session-1", WorkingDirectory: env.projectDir}
	gem.Prompt = &schema.PromptInfo{Text: "Add a flag"}
	if _, err := writer.AppendEvent(gem, writer.Options{Path: env.logPath, UserMode: true}); err != nil {
		t.Fatal(err)
	}
	return env
}

func sharePrintedExternalID(t *testing.T, stdout string) string {
	t.Helper()
	var payload sharePayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, stdout)
	}
	return payload.ExternalID
}

// shareEnvSessions stubs the Claude Code store with the fixture session (newest) and an older one,
// so a session chosen by BEACON_SESSION_ID is told apart from the most recent.
const shareOlderSession = "5f0c1d2e-bbbb-4bbb-8ccc-000000000009"

func shareEnvSessions(t *testing.T, env *shareEnv) {
	t.Helper()
	stubHandoffSources(t, stubHandoffSource{
		harness: handoff.HarnessClaude,
		sessions: []handoff.Session{
			{Harness: handoff.HarnessClaude, ID: shareTestSession, Directory: env.projectDir, UpdatedAt: time.Now()},
			{Harness: handoff.HarnessClaude, ID: shareOlderSession, Directory: env.projectDir, UpdatedAt: time.Now().Add(-30 * time.Minute)},
		},
		events: shareFixtureEvents(env.projectDir),
	})
}

func shareEnvFor(harness, id string) func(string) string {
	return func(key string) string {
		switch key {
		case shareSessionEnv:
			return id
		case shareHarnessEnv:
			return harness
		}
		return ""
	}
}

// The newest session here is another Claude Code one: the same runtime is no contradiction.
func TestShareCurrentPrefersTheSessionEnvironment(t *testing.T) {
	env := shareCurrentEnv(t)
	shareEnvSessions(t, env)
	t.Chdir(env.projectDir)
	shareGetenv = shareEnvFor("claude_code", shareOlderSession)
	stdout, _, err := runShareCommand(t, "--print", "--log-path", env.logPath)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if got := sharePrintedExternalID(t, stdout); got != "claude_code:"+shareOlderSession {
		t.Fatalf("external id = %q, want the env session", got)
	}
	// From a subdirectory of the session's directory, too.
	sub := filepath.Join(env.projectDir, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	if stdout, _, err = runShareCommand(t, "--print", "--log-path", env.logPath); err != nil || sharePrintedExternalID(t, stdout) != "claude_code:"+shareOlderSession {
		t.Fatalf("share from a subdirectory: err = %v", err)
	}
	t.Chdir(env.projectDir)

	// --harness naming another runtime ignores inherited variables.
	shareGetenv = shareEnvFor("gemini_cli", "gem-session-1")
	stdout, _, err = runShareCommand(t, "--current", "--harness", "claude", "--print", "--log-path", env.logPath)
	if err != nil {
		t.Fatalf("share --harness claude: %v", err)
	}
	if got := sharePrintedExternalID(t, stdout); got != "claude_code:"+shareTestSession {
		t.Fatalf("external id = %q, want the claude session", got)
	}
	// --harness naming the env's own runtime takes it as is.
	stdout, _, err = runShareCommand(t, "--harness", "gemini", "--print", "--log-path", env.logPath)
	if err != nil || sharePrintedExternalID(t, stdout) != "gemini_cli:gem-session-1" {
		t.Fatalf("share --harness gemini: err = %v", err)
	}

	// An env id that cannot be found is an error, never some other session.
	shareGetenv = shareEnvFor("gemini_cli", "missing-session")
	if _, _, err := runShareCommand(t, "--current", "--print", "--log-path", env.logPath); err == nil || !strings.Contains(err.Error(), "BEACON_SESSION_ID=missing-session") {
		t.Fatalf("missing env session: err = %v", err)
	}
}

// A runtime started from a Claude Code shell inherits its BEACON_SESSION_ID. Without --harness,
// share refuses rather than publish the parent session when something here contradicts it.
func TestShareCurrentRefusesAnInheritedSessionEnvironment(t *testing.T) {
	env := shareCurrentEnv(t)
	shareEnvSessions(t, env)
	srv := newFakeShareServer(t)
	srv.signIn(account.ScopeTracesWrite)
	t.Chdir(env.projectDir)
	shareGetenv = shareEnvFor("claude_code", shareOlderSession)

	// A Codex session, started from the Claude Code shell, is the newest here.
	if err := activesession.Record(shareActiveSessionsPath(), handoff.HarnessCodex, "codex-session-1", env.projectDir, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{}, {"--current"}, {"refresh"}} {
		_, _, err := runShareCommand(t, append(args, "--log-path", env.logPath)...)
		if err == nil || !strings.Contains(err.Error(), "codex-session-1") || !strings.Contains(err.Error(), shareOlderSession) || !strings.Contains(err.Error(), "--harness") {
			t.Fatalf("share %v: err = %v, want a refusal naming both sessions", args, err)
		}
	}
	// Another directory: the env session did not run here.
	other := t.TempDir()
	t.Chdir(other)
	_, _, err := runShareCommand(t, "--log-path", env.logPath)
	if err == nil || !strings.Contains(err.Error(), "not in "+other) {
		t.Fatalf("share from another directory: err = %v", err)
	}
	if len(srv.requests) != 0 {
		t.Fatalf("requests = %v", srv.requests)
	}
}

func TestShareCurrentUsesTheActiveSessionRecord(t *testing.T) {
	env := shareCurrentEnv(t)
	t.Chdir(env.projectDir)
	// The Claude session is the most recent; the hook last saw Gemini here.
	if err := activesession.Record(shareActiveSessionsPath(), "gemini_cli", "gem-session-1", env.projectDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runShareCommand(t, "--current", "--print", "--log-path", env.logPath)
	if err != nil {
		t.Fatalf("share --current: %v", err)
	}
	if got := sharePrintedExternalID(t, stdout); got != "gemini_cli:gem-session-1" {
		t.Fatalf("external id = %q, want the active session", got)
	}
	// A --harness hint passes over another runtime's entry.
	stdout, _, err = runShareCommand(t, "--harness", "claude_code", "--print", "--log-path", env.logPath)
	if err != nil {
		t.Fatalf("share --harness: %v", err)
	}
	if got := sharePrintedExternalID(t, stdout); got != "claude_code:"+shareTestSession {
		t.Fatalf("external id = %q", got)
	}
	// A stale entry is ignored.
	shareNow = func() time.Time { return time.Now().Add(48 * time.Hour) }
	t.Cleanup(func() { shareNow = time.Now })
	stdout, _, err = runShareCommand(t, "--print", "--log-path", env.logPath)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if got := sharePrintedExternalID(t, stdout); got != "claude_code:"+shareTestSession {
		t.Fatalf("stale record used: %q", got)
	}
}

func TestShareCurrentPassesOverAnOlderActiveEntry(t *testing.T) {
	env := newShareEnv(t)
	t.Chdir(env.projectDir)
	recordPath := filepath.Join(env.home, "active.json")
	prev := shareActiveSessionsPath
	shareActiveSessionsPath = func() string { return recordPath }
	t.Cleanup(func() { shareActiveSessionsPath = prev })
	hourAgo := time.Now().Add(-time.Hour)
	stubHandoffSources(t, stubHandoffSource{
		harness: handoff.HarnessClaude,
		sessions: []handoff.Session{{
			Harness: handoff.HarnessClaude, ID: shareTestSession, Directory: env.projectDir, UpdatedAt: hourAgo,
		}},
		events: shareFixtureEvents(env.projectDir),
	})
	// The hook saw Claude Code here an hour ago; Copilot CLI, which only exports OTLP and never
	// writes the record, has worked here since.
	if err := activesession.Record(recordPath, handoff.HarnessClaude, shareTestSession, env.projectDir, hourAgo); err != nil {
		t.Fatal(err)
	}
	copilot := schema.NewEvent(schema.NewEventOptions{Action: "prompt.submitted", Category: "prompt", Harness: schema.HarnessInfo{Name: handoff.HarnessCopilot, CollectionMethod: schema.CollectionMethodOTLP}})
	copilot.Timestamp = schema.FormatTimestamp(time.Now().Add(-5 * time.Minute))
	copilot.Session = &schema.SessionInfo{ID: "copilot-session-1", WorkingDirectory: env.projectDir}
	copilot.Prompt = &schema.PromptInfo{Text: "Bump the version"}
	if _, err := writer.AppendEvent(copilot, writer.Options{Path: env.logPath, UserMode: true}); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runShareCommand(t, "--print", "--log-path", env.logPath)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if got := sharePrintedExternalID(t, stdout); got != handoff.HarnessCopilot+":copilot-session-1" {
		t.Fatalf("external id = %q, want the newer Copilot session", got)
	}

	// Once the hook sees Claude Code again, its entry is the newest and is chosen.
	if err := activesession.Record(recordPath, handoff.HarnessClaude, shareTestSession, env.projectDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runShareCommand(t, "--json", "--print", "--log-path", env.logPath)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if got := sharePrintedExternalID(t, stdout); got != "claude_code:"+shareTestSession {
		t.Fatalf("external id = %q, want the active Claude Code session", got)
	}
}

// shareSymlinkedProject returns a symlink to the project directory, and the project's physical
// path, which is what a hook records; the shell reports the symlink.
func shareSymlinkedProject(t *testing.T, env *shareEnv) (link, physical string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinked directories need POSIX symlinks")
	}
	physical, err := filepath.EvalSymlinks(env.projectDir)
	if err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(physical, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	return link, physical
}

func TestShareCurrentMatchesTheActiveRecordThroughASymlink(t *testing.T) {
	env := shareCurrentEnv(t)
	link, physical := shareSymlinkedProject(t, env)
	srv := newFakeShareServer(t)
	srv.signIn(account.ScopeTracesWrite)
	t.Chdir(link)
	if err := activesession.Record(shareActiveSessionsPath(), "gemini_cli", "gem-session-1", physical, time.Now()); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runShareCommand(t, "--current", "--json", "--log-path", env.logPath)
	if err != nil {
		t.Fatalf("share --current: %v\n%s", err, stderr)
	}
	var result struct {
		Data shareResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.SelectedBy != "active" || result.Data.ExternalID != "gemini_cli:gem-session-1" {
		t.Fatalf("result = %+v, want the active session", result.Data)
	}
}

func TestShareCurrentSeesAnInheritedEnvironmentThroughASymlink(t *testing.T) {
	env := shareCurrentEnv(t)
	shareEnvSessions(t, env)
	link, physical := shareSymlinkedProject(t, env)
	t.Chdir(link)
	shareGetenv = shareEnvFor("claude_code", shareOlderSession)
	if err := activesession.Record(shareActiveSessionsPath(), handoff.HarnessCodex, "codex-session-1", physical, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _, err := runShareCommand(t, "--print", "--log-path", env.logPath)
	if err == nil || !strings.Contains(err.Error(), "codex-session-1") {
		t.Fatalf("err = %v, want the inherited-environment refusal", err)
	}
}
