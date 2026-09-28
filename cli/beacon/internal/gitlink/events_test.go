package gitlink

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestSanitizeRemoteURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/org/repo.git":                             "https://github.com/org/repo.git",
		"https://x-access-token:ghp_SECRET@github.com/org/repo.git":   "https://github.com/org/repo.git",
		"https://user@gitlab.example.com/group/repo":                  "https://gitlab.example.com/group/repo",
		"https://gitlab.example.com/group/repo.git?private_token=abc": "https://gitlab.example.com/group/repo.git",
		"ssh://git@github.com:22/org/repo.git":                        "ssh://github.com:22/org/repo.git",
		"git@github.com:org/repo.git":                                 "github.com:org/repo.git",
		"github.com:org/repo.git":                                     "github.com:org/repo.git",
		"/srv/git/repo.git":                                           "/srv/git/repo.git",
		"../sibling":                                                  "../sibling",
		"file:///srv/git/repo.git":                                    "file:///srv/git/repo.git",
		"  https://tok@host/r \n":                                     "https://host/r",
		"":                                                            "",
	} {
		if got := SanitizeRemoteURL(raw); got != want {
			t.Errorf("SanitizeRemoteURL(%q) = %q, want %q", raw, got, want)
		}
	}
	// A URL net/url rejects still loses anything that could be user info.
	if got := SanitizeRemoteURL("https://to%ken:se cret@host/r"); strings.Contains(got, "se cret") || strings.Contains(got, "to%ken") {
		t.Errorf("unparseable URL kept credentials: %q", got)
	}
}

func TestLinkEventsShape(t *testing.T) {
	result := Result{
		Commit:       Commit{SHA: "0123456789abcdef0123456789abcdef01234567"},
		RepoRoot:     "/work/repo",
		ChangedFiles: 3,
		Candidates: []Candidate{
			{Link: Link{"claude_code", "s1"}, Files: []string{"a.go", "b.go"}},
			{Link: Link{"cursor", "c1"}, Files: []string{"a.go"}, AlreadyLinked: true},
		},
		Added:       []Link{{"claude_code", "s1"}},
		NoteUpdated: true,
	}
	events := LinkEvents(result, CommitContext{Branch: "main", RemoteURL: "https://github.com/org/repo.git"}, "1.2.3")
	if len(events) != 1 {
		t.Fatalf("got %d events, want one per added link", len(events))
	}
	e := events[0]
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if e.Event.Action != LinkAction || e.Event.Fidelity != "inferred" || e.Harness.Name != "claude_code" || e.Harness.CollectionMethod != "" {
		t.Fatalf("event info %+v harness %+v", e.Event, e.Harness)
	}
	if e.Session == nil || e.Session.ID != "s1" || e.Session.WorkingDirectory != "/work/repo" || e.Repository != "/work/repo" || e.Branch != "main" {
		t.Fatalf("session/repository %+v %q %q", e.Session, e.Repository, e.Branch)
	}
	v := e.VCS
	if v == nil || v.Ref.Head.Revision != result.Commit.SHA || v.Ref.Head.Name != "main" || v.Ref.Head.Type != "branch" ||
		v.Repository.URL.Full != "https://github.com/org/repo.git" ||
		v.Attribution.Method != AttributionFileOverlap || v.Attribution.ChangedFiles != 3 || v.Attribution.MatchedFiles != 2 {
		t.Fatalf("vcs %+v", v)
	}
	if e.File != nil || e.Command != nil || e.Tool != nil || e.GenAI != nil || e.Prompt != nil {
		t.Fatal("a link event must carry no file, command, tool or content fields")
	}
	data, _ := json.Marshal(e)
	for _, key := range []string{`"vcs":{"ref":{"head":{"revision":"0123456789abcdef0123456789abcdef01234567","name":"main","type":"branch"}}`, `"url":{"full":`, `"attribution":{"method":"file_overlap"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("serialized event lacks %s:\n%s", key, data)
		}
	}
	// Detached HEAD: no branch, no ref type, no remote block.
	detached := LinkEvents(result, CommitContext{}, "")
	if h := detached[0].VCS.Ref.Head; h.Name != "" || h.Type != "" || detached[0].VCS.Repository != nil {
		t.Fatalf("detached %+v", detached[0].VCS)
	}
	if LinkEvents(Result{}, CommitContext{}, "") != nil {
		t.Fatal("no links, no events")
	}
}

func TestResolveCommitContext(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	r.write("a", "1")
	first := r.commitAt("first", t0)
	r.write("a", "2")
	r.commitAt("second", t0.Add(time.Minute))
	r.git("remote", "add", "origin", "https://tok:secret@example.com/org/repo.git")
	r.git("remote", "add", "fork", "git@example.com:me/repo.git")
	repo := r.open()
	head, _ := ResolveCommit(ctx, repo.Git, "HEAD")
	if cc := ResolveCommitContext(ctx, repo, head); cc.Branch != "main" || cc.RemoteURL != "https://example.com/org/repo.git" {
		t.Fatalf("HEAD context %+v", cc)
	}
	r.git("config", "branch.main.remote", "fork")
	if cc := ResolveCommitContext(ctx, repo, head); cc.RemoteURL != "example.com:me/repo.git" {
		t.Fatalf("tracked remote %+v", cc)
	}
	// An older commit's branch is unknown.
	old, _ := ResolveCommit(ctx, repo.Git, first)
	if cc := ResolveCommitContext(ctx, repo, old); cc.Branch != "" {
		t.Fatalf("old commit branch %q", cc.Branch)
	}
	r.git("checkout", "-q", "--detach")
	if cc := ResolveCommitContext(ctx, repo, head); cc.Branch != "" || cc.RemoteURL != "https://example.com/org/repo.git" {
		t.Fatalf("detached %+v", cc)
	}
}

func TestLinkEventsAreNeverEvidence(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	repo := r.open()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	events := LinkEvents(Result{
		Commit: Commit{SHA: strings.Repeat("a", 40)}, RepoRoot: repo.Root, ChangedFiles: 1,
		Candidates: []Candidate{{Link: Link{"codex_cli", "s"}, Files: []string{"x.go"}}},
		Added:      []Link{{"codex_cli", "s"}},
	}, CommitContext{Branch: "main"}, "")
	var b strings.Builder
	for _, e := range events {
		e.Timestamp = schema.FormatTimestamp(t0)
		data, _ := json.Marshal(e)
		b.Write(append(data, '\n'))
	}
	if err := os.WriteFile(logPath, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEvidence(ctx, repo, logPath, t0.Add(-time.Hour), t0.Add(time.Hour))
	if err != nil || len(got) != 0 {
		t.Fatalf("link events read as evidence: %v %v", got, err)
	}
}
