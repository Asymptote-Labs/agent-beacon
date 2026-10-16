package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/gitlink"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func writeSharedEvent(t *testing.T, logPath, harness, session, url string) {
	t.Helper()
	event := schema.NewEvent(schema.NewEventOptions{Action: asymptoteobserve.ActionSessionShared, Category: "session", Harness: schema.HarnessInfo{Name: harness}})
	event.Session = &schema.SessionInfo{ID: session}
	event.Raw = map[string]interface{}{"shared_url": url}
	line, _ := json.Marshal(event)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestGitHookPostCommitWritesAKnownShareURL(t *testing.T) {
	relaxGitBudgets(t)
	root := gitCommandRepo(t)
	top := mustGit(t, root, "rev-parse", "--show-toplevel")
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	for _, f := range []string{"x.go", "y.go"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeRuntimeEvent(t, logPath, top, "cur-1", "x.go")
	writeRuntimeEvent(t, logPath, top, "cur-2", "y.go")
	writeSharedEvent(t, logPath, "cursor", "cur-1", "https://beacon.sh/s/abc123")
	writeSharedEvent(t, logPath, "cursor", "cur-2", "https://evil.example/s/abc123")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "x")
	if out, err := runGit(t, "hook", "post-commit", "--cwd", root, "--log-path", logPath); err != nil || out != "" {
		t.Fatalf("hook: %v %q", err, out)
	}
	note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", "HEAD")
	if !strings.Contains(note, "beacon:cursor/cur-1 https://beacon.sh/s/abc123") || !strings.Contains(note, "beacon:cursor/cur-2") || strings.Contains(note, "evil.example") {
		t.Fatalf("note %q", note)
	}
	out, err := runGit(t, "notes", "--cwd", root, "--json")
	if err != nil || !strings.Contains(out, `"url": "https://beacon.sh/s/abc123"`) {
		t.Fatalf("notes --json: %v\n%s", err, out)
	}
	out, _ = runGit(t, "notes", "--cwd", root)
	if !strings.Contains(out, "cursor/cur-1  https://beacon.sh/s/abc123") {
		t.Fatalf("notes:\n%s", out)
	}
}

func TestGitPrePushSharesLinkedSessions(t *testing.T) {
	relaxGitBudgets(t)
	t.Setenv(gitlink.HookStdinEnv, "1") // as Beacon's hook block runs it
	root, remote := gitCommandTeam(t)
	env := newShareEnv(t)
	srv := newFakeShareServer(t)
	srv.signIn(account.ScopeTracesWrite)
	prevOpts := shareOpts
	shareOpts = shareOptions{logPath: env.logPath}
	t.Cleanup(func() { shareOpts = prevOpts })

	old := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "agent work")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:claude_code/"+shareTestSession+"\nbeacon:cursor/someone-else", sha)
	if out, err := runGit(t, "setup", "--cwd", root, "--share-sessions"); err != nil || !strings.Contains(out, "shares the agent sessions linked") {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if out, _ := runGit(t, "status", "--cwd", root); !strings.Contains(out, "sessions    on: pushes share linked sessions to beacon.sh (direct)") {
		t.Fatalf("status:\n%s", out)
	}

	stdin := "refs/heads/main " + sha + " refs/heads/main " + old + "\n"
	var stderr bytes.Buffer
	rootCmd.SetIn(strings.NewReader(stdin))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, "origin", remote); err != nil {
		t.Fatalf("pre-push: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr %q", stderr.String())
	}
	if len(srv.events) != 4 {
		t.Fatalf("server events = %d", len(srv.events))
	}
	want := "beacon:claude_code/" + shareTestSession + " https://beacon.sh/s/k3xabc\nbeacon:cursor/someone-else"
	if note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != want {
		t.Fatalf("local note %q", note)
	}
	if note := mustGit(t, remote, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != want {
		t.Fatalf("remote note %q", note)
	}
	if urls := localShareURLs(env.logPath); urls["claude_code/"+shareTestSession] != "https://beacon.sh/s/k3xabc" {
		t.Fatalf("session.shared not recorded: %v", urls)
	}

	// A second push has nothing left to share and makes no share request.
	before := len(srv.requests)
	rootCmd.SetIn(strings.NewReader(stdin))
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, "origin", remote); err != nil || stderr.Len() != 0 {
		t.Fatalf("second pre-push: %v %q", err, stderr.String())
	}
	if len(srv.requests) != before {
		t.Fatalf("second push made share requests: %v", srv.requests[before:])
	}
}

func TestGitPrePushWithoutCredentialsSharesNothingQuietly(t *testing.T) {
	t.Setenv(gitlink.HookStdinEnv, "1") // as Beacon's hook block runs it
	root, remote := gitCommandTeam(t)
	env := newShareEnv(t) // signed out, and any request fails the test
	prevOpts := shareOpts
	shareOpts = shareOptions{logPath: env.logPath}
	t.Cleanup(func() { shareOpts = prevOpts })
	old := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "agent work")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:claude_code/"+shareTestSession, sha)
	if _, err := runGit(t, "setup", "--cwd", root, "--share-sessions"); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	rootCmd.SetIn(strings.NewReader("refs/heads/main " + sha + " refs/heads/main " + old + "\n"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	start := time.Now()
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, "origin", remote); err != nil || stderr.Len() != 0 {
		t.Fatalf("pre-push: %v %q", err, stderr.String())
	}
	if time.Since(start) > gitShareBudget {
		t.Fatal("pre-push exceeded its budget")
	}
	if note := mustGit(t, remote, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != "beacon:claude_code/"+shareTestSession {
		t.Fatalf("remote note %q", note)
	}
}

func TestGitSetupShareSessionsRules(t *testing.T) {
	root, _ := gitCommandTeam(t)
	if _, err := runGit(t, "setup", "--cwd", root, "--share-sessions", "--share-notes=false"); err == nil {
		t.Fatal("--share-sessions with --share-notes=false accepted")
	}
	prev := shareIsTTY
	shareIsTTY = func() bool { return false }
	t.Cleanup(func() { shareIsTTY = prev })
	if _, err := runGit(t, "setup", "--cwd", root, "--share-sessions", "--share-visibility", "public"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("public without --yes: %v", err)
	}
	if out, err := runGit(t, "setup", "--cwd", root, "--share-sessions", "--share-visibility", "public", "--yes"); err != nil || !strings.Contains(out, "beacon.shareVisibility = public") {
		t.Fatalf("public --yes: %v\n%s", err, out)
	}
	if out, err := runGit(t, "setup", "--cwd", root, "--share-notes=false"); err != nil || !strings.Contains(out, "unset     beacon.shareSessions") {
		t.Fatalf("share-notes off: %v\n%s", err, out)
	}
	if out, _ := runGit(t, "status", "--cwd", root); !strings.Contains(out, "sessions    off") {
		t.Fatalf("status:\n%s", out)
	}
}

// Called straight from a hook manager (gitlink.HookCall), without Beacon's block to copy and
// replay stdin, the pre-push hook must leave stdin to the steps after it (`git lfs pre-push`).
// Without the refs it cannot know what is being pushed (another branch, --tags, --delete), so it
// shares no sessions, says so once, and still pushes the commit links.
func TestGitPrePushWithoutTheBlockLeavesStdinAloneAndSharesNothing(t *testing.T) {
	root, remote := gitCommandTeam(t)
	env := newShareEnv(t)
	srv := newFakeShareServer(t)
	srv.signIn(account.ScopeTracesWrite)
	prevOpts := shareOpts
	shareOpts = shareOptions{logPath: env.logPath}
	t.Cleanup(func() { shareOpts = prevOpts })
	t.Setenv(gitlink.HookStdinEnv, "")

	old := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "agent work")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:claude_code/"+shareTestSession, sha)
	if _, err := runGit(t, "setup", "--cwd", root, "--share-sessions"); err != nil {
		t.Fatal(err)
	}
	stdin := "refs/heads/main " + sha + " refs/heads/main " + old + "\n"
	reader := strings.NewReader(stdin)
	rootCmd.SetIn(reader)
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	var stderr bytes.Buffer
	before := len(srv.requests)
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, "origin", remote); err != nil {
		t.Fatalf("pre-push: %v", err)
	}
	if reader.Len() != len(stdin) {
		t.Fatalf("the hook read %d bytes of stdin that the next step needs", len(stdin)-reader.Len())
	}
	if len(srv.requests) != before {
		t.Fatalf("shared sessions without knowing the pushed refs: %v", srv.requests[before:])
	}
	if !strings.Contains(stderr.String(), "commit links only") {
		t.Fatalf("stderr %q, want the hint", stderr.String())
	}
	if note := mustGit(t, remote, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != "beacon:claude_code/"+shareTestSession {
		t.Fatalf("remote note %q, want the bare link pushed and nothing shared", note)
	}
}

// Pre-push sharing to a trace that already exists keeps the title set on it earlier (with --title
// or a rename on beacon.sh) and its visibility.
func TestGitPrePushKeepsAnExistingTracesTitle(t *testing.T) {
	relaxGitBudgets(t)
	root, remote := gitCommandTeam(t)
	env := newShareEnv(t)
	srv := newFakeShareServer(t)
	srv.signIn(account.ScopeTracesWrite)
	// Shared from elsewhere (another log, so this machine's log has no record of it), titled.
	elsewhere := filepath.Join(t.TempDir(), "other.jsonl")
	if _, stderr, err := runShareCommand(t, "--cwd", env.projectDir, "--title", "Login fix", "--visibility", "private", "--log-path", elsewhere); err != nil {
		t.Fatalf("share: %v\n%s", err, stderr)
	}
	prevOpts := shareOpts
	shareOpts = shareOptions{logPath: env.logPath}
	t.Cleanup(func() { shareOpts = prevOpts })
	t.Setenv(gitlink.HookStdinEnv, "1")

	old := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "agent work")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:claude_code/"+shareTestSession, sha)
	if _, err := runGit(t, "setup", "--cwd", root, "--share-sessions"); err != nil {
		t.Fatal(err)
	}
	rootCmd.SetIn(strings.NewReader("refs/heads/main " + sha + " refs/heads/main " + old + "\n"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	var stderr bytes.Buffer
	before := len(srv.requests)
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, "origin", remote); err != nil || stderr.Len() != 0 {
		t.Fatalf("pre-push: %v %q", err, stderr.String())
	}
	if len(srv.requests) == before {
		t.Fatal("pre-push made no share request")
	}
	if srv.trace.Title != "Login fix" || srv.trace.Visibility != "private" {
		t.Fatalf("trace title %q visibility %q, want the ones set earlier", srv.trace.Title, srv.trace.Visibility)
	}
}

// When the uploads' context ends before they finish, the URLs of the uploads that finished are
// still written into the notes: the note updates run on their own context. The slow upload ends the
// hook's context itself, so the test does not depend on how long reading the notes and stores takes.
func TestGitShareOnPushWritesFinishedURLsWhenUploadsRunOutOfTime(t *testing.T) {
	relaxGitBudgets(t)
	root, _ := gitCommandTeam(t)
	env := newShareEnv(t)
	srv := newFakeShareServer(t)
	srv.signIn(account.ScopeTracesWrite)
	prevOpts := shareOpts
	shareOpts = shareOptions{logPath: env.logPath}
	t.Cleanup(func() { shareOpts = prevOpts })
	stubHandoffSources(t, stubHandoffSource{
		harness: handoff.HarnessClaude,
		sessions: []handoff.Session{
			{Harness: handoff.HarnessClaude, ID: "sess-fast", Directory: env.projectDir, UpdatedAt: time.Now()},
			{Harness: handoff.HarnessClaude, ID: "sess-slow", Directory: env.projectDir, UpdatedAt: time.Now()},
		},
		events: shareFixtureEvents(env.projectDir),
	})
	ctx, cancel := context.WithTimeout(context.Background(), gitShareBudget)
	defer cancel()
	prevUpload := gitShareUpload
	gitShareUpload = func(uploadCtx context.Context, _ *account.ShareClient, subject shareSubject, _ shareUploadOptions) (shareResult, shareSubject, error) {
		if subject.session.ID == "sess-fast" {
			return shareResult{SharedURL: "https://beacon.sh/s/fast01"}, subject, nil
		}
		cancel() // the second upload outlasts the hook
		<-uploadCtx.Done()
		return shareResult{}, subject, uploadCtx.Err()
	}
	t.Cleanup(func() { gitShareUpload = prevUpload })

	old := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "agent work")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:claude_code/sess-fast\nbeacon:claude_code/sess-slow", sha)
	repo, err := gitlink.OpenRepo(context.Background(), gitlink.Git{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	report, err := gitShareSessionsOnPush(ctx, repo, "origin", "refs/heads/main "+sha+" refs/heads/main "+old+"\n", "")
	if err == nil {
		t.Fatal("running out of time was not reported")
	}
	if report.Noted != 1 {
		t.Fatalf("report %+v, want the note updated", report)
	}
	if report.OutOfTime {
		t.Fatalf("report %+v: a cancelled hook is not one that ran out of time", report)
	}
	want := "beacon:claude_code/sess-fast https://beacon.sh/s/fast01\nbeacon:claude_code/sess-slow"
	if note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != want {
		t.Fatalf("note %q, want %q", note, want)
	}
}

// The per-push cap counts only sessions this machine has: links to teammates' or pruned sessions
// ahead of one's own do not use up its slots. Each runtime's store is listed once per push and the
// runtime log read once, however many links there are.
func TestGitShareOnPushCapsOnlySessionsThatResolve(t *testing.T) {
	relaxGitBudgets(t)
	root, _ := gitCommandTeam(t)
	env := newShareEnv(t)
	srv := newFakeShareServer(t)
	srv.signIn(account.ScopeTracesWrite)
	prevOpts := shareOpts
	shareOpts = shareOptions{logPath: env.logPath}
	t.Cleanup(func() { shareOpts = prevOpts })
	logReads := 0
	prevLogSessions := gitShareLogSessions
	gitShareLogSessions = func(logPath string) ([]handoff.Session, error) {
		logReads++
		return prevLogSessions(logPath)
	}
	t.Cleanup(func() { gitShareLogSessions = prevLogSessions })
	storeListings := 0
	prevListStore := gitShareListStore
	gitShareListStore = func(sources []handoff.Source, harness string) ([]handoff.Session, error) {
		storeListings++
		return prevListStore(sources, harness)
	}
	t.Cleanup(func() { gitShareListStore = prevListStore })
	var uploaded []string
	prevUpload := gitShareUpload
	gitShareUpload = func(ctx context.Context, _ *account.ShareClient, subject shareSubject, _ shareUploadOptions) (shareResult, shareSubject, error) {
		uploaded = append(uploaded, subject.session.ID)
		return shareResult{SharedURL: "https://beacon.sh/s/mine01"}, subject, nil
	}
	t.Cleanup(func() { gitShareUpload = prevUpload })

	var lines []string
	for i := 0; i < gitShareMaxSessions+2; i++ {
		lines = append(lines, fmt.Sprintf("beacon:claude_code/teammate-%02d", i))
	}
	lines = append(lines, "beacon:claude_code/"+shareTestSession)
	old := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "agent work")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", strings.Join(lines, "\n"), sha)
	repo, err := gitlink.OpenRepo(context.Background(), gitlink.Git{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	// The hook's deadline, as runGitHookPrePush passes it: the note writes get what it leaves.
	ctx, cancel := context.WithTimeout(context.Background(), gitShareBudget)
	defer cancel()
	if _, err := gitShareSessionsOnPush(ctx, repo, "origin", "refs/heads/main "+sha+" refs/heads/main "+old+"\n", ""); err != nil {
		t.Fatal(err)
	}
	if len(uploaded) != 1 || uploaded[0] != shareTestSession {
		t.Fatalf("uploaded %v, want only this machine's session", uploaded)
	}
	if logReads != 1 {
		t.Fatalf("runtime log read %d times for the teammates' links, want once", logReads)
	}
	if storeListings != 1 {
		t.Fatalf("the Claude Code store was listed %d times for %d links, want once per push", storeListings, len(lines))
	}
	if note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", sha); !strings.Contains(note, "beacon:claude_code/"+shareTestSession+" https://beacon.sh/s/mine01") {
		t.Fatalf("note %q lacks the URL", note)
	}
}

// The share phase is held to the clock: an upload that never returns, and ignores its context, does
// not hold up the push. The notes still get the URLs of the uploads that finished, the notes ref is
// pushed, and the hook returns within its production budget. The upload budget is short against
// that budget but long enough for a slow runner to read the notes and stores before it, so the fast
// upload lands.
func TestGitPrePushAbandonsAnUploadThatNeverReturns(t *testing.T) {
	t.Setenv(gitlink.HookStdinEnv, "1")
	root, remote := gitCommandTeam(t)
	env := newShareEnv(t)
	srv := newFakeShareServer(t)
	srv.signIn(account.ScopeTracesWrite)
	prevOpts := shareOpts
	shareOpts = shareOptions{logPath: env.logPath}
	t.Cleanup(func() { shareOpts = prevOpts })
	stubHandoffSources(t, stubHandoffSource{
		harness: handoff.HarnessClaude,
		sessions: []handoff.Session{
			{Harness: handoff.HarnessClaude, ID: "sess-fast", Directory: env.projectDir, UpdatedAt: time.Now()},
			{Harness: handoff.HarnessClaude, ID: "sess-stuck", Directory: env.projectDir, UpdatedAt: time.Now()},
		},
		events: shareFixtureEvents(env.projectDir),
	})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	prevUpload, prevBudget := gitShareUpload, gitShareUploadBudget
	gitShareUploadBudget = 3 * time.Second
	gitShareUpload = func(ctx context.Context, _ *account.ShareClient, subject shareSubject, _ shareUploadOptions) (shareResult, shareSubject, error) {
		if subject.session.ID == "sess-fast" {
			return shareResult{SharedURL: "https://beacon.sh/s/fast01"}, subject, nil
		}
		<-release // stuck in local work that never looks at ctx
		return shareResult{SharedURL: "https://beacon.sh/s/late01"}, subject, nil
	}
	t.Cleanup(func() { gitShareUpload, gitShareUploadBudget = prevUpload, prevBudget })

	old := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "agent work")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "add", "-m", "beacon:claude_code/sess-fast\nbeacon:claude_code/sess-stuck", sha)
	if out, err := runGit(t, "setup", "--cwd", root, "--share-sessions"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	rootCmd.SetIn(strings.NewReader("refs/heads/main " + sha + " refs/heads/main " + old + "\n"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	var stderr bytes.Buffer
	start := time.Now()
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, "origin", remote); err != nil {
		t.Fatalf("pre-push: %v", err)
	}
	if elapsed := time.Since(start); elapsed > gitShareBudget {
		t.Fatalf("pre-push took %s, past its budget", elapsed)
	}
	if !strings.Contains(stderr.String(), "ran out of time") {
		t.Fatalf("stderr %q", stderr.String())
	}
	want := "beacon:claude_code/sess-fast https://beacon.sh/s/fast01\nbeacon:claude_code/sess-stuck"
	if note := mustGit(t, root, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != want {
		t.Fatalf("local note %q, want %q", note, want)
	}
	if note := mustGit(t, remote, "notes", "--ref="+gitlink.NotesRef, "show", sha); note != want {
		t.Fatalf("remote note %q: the notes ref was not pushed", note)
	}
	// The abandoned upload is recorded as the share phase running out of time, against the hook's
	// production budget. The times are lower bounds only: the upload deadline cannot pass early.
	hook := onlyBudgetEvent(t, gitRuntimeLogPath())
	if hook.Name != "pre-push" || hook.Phase != "share_sessions" || !hook.CutShort || hook.BudgetMS != gitShareBudget.Milliseconds() {
		t.Fatalf("overrun %+v, want pre-push cut short in share_sessions", hook)
	}
	if hook.PhaseElapsedMS < gitShareUploadBudget.Milliseconds() || hook.ElapsedMS < hook.PhaseElapsedMS || hook.GitSpawns < 1 {
		t.Fatalf("overrun %+v, want the share phase to have run its %s upload budget", hook, gitShareUploadBudget)
	}
}

// A share phase that returns at once leaves the notes push the rest of the hook's budget, not just
// its reserve.
func TestGitPrePushGivesThePushTheRemainingBudget(t *testing.T) {
	t.Setenv(gitlink.HookStdinEnv, "1")
	root, remote := gitCommandTeam(t)
	env := newShareEnv(t)
	prevOpts := shareOpts
	shareOpts = shareOptions{logPath: env.logPath}
	t.Cleanup(func() { shareOpts = prevOpts })
	old := mustGit(t, root, "rev-parse", "HEAD")
	mustGit(t, root, "commit", "-q", "--allow-empty", "-m", "no links")
	sha := mustGit(t, root, "rev-parse", "HEAD")
	if out, err := runGit(t, "setup", "--cwd", root, "--share-sessions"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	var left time.Duration
	prevPush := gitPushNotes
	gitPushNotes = func(ctx context.Context, repo gitlink.Repo, remote string) (gitlink.PushResult, error) {
		if deadline, ok := ctx.Deadline(); ok {
			left = time.Until(deadline)
		}
		return prevPush(ctx, repo, remote)
	}
	t.Cleanup(func() { gitPushNotes = prevPush })
	rootCmd.SetIn(strings.NewReader("refs/heads/main " + sha + " refs/heads/main " + old + "\n"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	var stderr bytes.Buffer
	if _, err := runGitStderr(t, &stderr, "hook", "pre-push", "--cwd", root, "origin", remote); err != nil {
		t.Fatalf("pre-push: %v", err)
	}
	if left < gitShareBudget-2*time.Second {
		t.Fatalf("the notes push got %s, want about the hook's %s", left, gitShareBudget)
	}
}
