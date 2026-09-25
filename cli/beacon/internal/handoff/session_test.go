package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func fixtureSources(t *testing.T) (storeFixture, []Source) {
	t.Helper()
	// Cline also reads VS Code's global storage under HOME; keep it inside the test.
	testenv.SetHome(t, t.TempDir())
	f := newStoreFixture(t)
	return f, DefaultSources(f.dirs)
}

func sessionKeys(sessions []Session) []string {
	keys := make([]string, 0, len(sessions))
	for _, s := range sessions {
		keys = append(keys, s.Harness+"/"+s.ID)
	}
	return keys
}

func TestListReadsEveryRuntimeStoreNewestFirst(t *testing.T) {
	_, sources := fixtureSources(t)
	sessions, err := List(sources, Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := strings.Join(sessionKeys(sessions), ",")
	want := "claude_code/claude-sess-1,opencode/ses_parent,codex_cli/codex-thread-1,cline/cline-task-1"
	if got != want {
		t.Fatalf("sessions = %s\nwant %s", got, want)
	}
}

func TestListCarriesEachRuntimesMetadata(t *testing.T) {
	f, sources := fixtureSources(t)
	sessions, err := List(sources, Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byHarness := map[string]Session{}
	for _, s := range sessions {
		byHarness[s.Harness] = s
	}

	claude := byHarness[HarnessClaude]
	if claude.Directory != "/work/api" || claude.Branch != "feat/health" {
		t.Fatalf("claude directory/branch = %q/%q", claude.Directory, claude.Branch)
	}
	if claude.Title != "Add a health endpoint" {
		t.Fatalf("claude title = %q, want the index summary on one line", claude.Title)
	}
	if !claude.UpdatedAt.Equal(claudeUpdated) {
		t.Fatalf("claude updated = %s, want %s", claude.UpdatedAt, claudeUpdated)
	}
	if claude.SourcePath != filepath.Join(f.dirs[HarnessClaude], "-work-api", "claude-sess-1.jsonl") {
		t.Fatalf("claude source = %q", claude.SourcePath)
	}

	codex := byHarness[HarnessCodex]
	if codex.Directory != "/work/web" || codex.Title != "Fix the login form" {
		t.Fatalf("codex = %+v", codex)
	}
	if !strings.Contains(codex.SourcePath, filepath.Join("2026", "09", "21")) || !codex.UpdatedAt.Equal(codexUpdated) {
		t.Fatalf("codex should stand for its newest rollout file, got %q at %s", codex.SourcePath, codex.UpdatedAt)
	}

	openCode := byHarness[HarnessOpenCode]
	if openCode.Directory != "/work/cache" || openCode.Title != "Refactor the cache" || openCode.Store != "sqlite" {
		t.Fatalf("opencode = %+v", openCode)
	}
	if !openCode.UpdatedAt.Equal(openCodeUpdated) {
		t.Fatalf("opencode updated = %s, want %s", openCode.UpdatedAt, openCodeUpdated)
	}

	cline := byHarness[HarnessCline]
	if cline.Directory != "/work/docs" || cline.Title != "Write the release notes" {
		t.Fatalf("cline = %+v", cline)
	}
	if cline.Store != "messages" {
		t.Fatalf("cline store = %q; the CLI session must win over the task-history copy of the same id", cline.Store)
	}
}

func TestListHidesSubagentsUnlessAsked(t *testing.T) {
	_, sources := fixtureSources(t)
	sessions, err := List(sources, Filter{IncludeSubagents: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var subagents []Session
	for _, s := range sessions {
		if s.Subagent {
			subagents = append(subagents, s)
		}
	}
	if len(subagents) != 2 {
		t.Fatalf("subagents = %v, want the Claude and OpenCode children", sessionKeys(subagents))
	}
	for _, s := range subagents {
		want := map[string]string{HarnessClaude: "claude-sess-1", HarnessOpenCode: "ses_parent"}[s.Harness]
		if s.ParentID != want {
			t.Fatalf("%s subagent parent = %q, want %q", s.Harness, s.ParentID, want)
		}
	}
}

func TestListFilters(t *testing.T) {
	_, sources := fixtureSources(t)
	for _, tc := range []struct {
		name   string
		filter Filter
		want   string
	}{
		{"harness", Filter{Harness: HarnessCodex}, "codex_cli/codex-thread-1"},
		{"directory", Filter{Directory: "/work/cache"}, "opencode/ses_parent"},
		{"parent directory", Filter{Directory: "/work"}, "claude_code/claude-sess-1,opencode/ses_parent,codex_cli/codex-thread-1,cline/cline-task-1"},
		{"sibling prefix is not a parent", Filter{Directory: "/work/ap"}, ""},
		{"child of the session directory is not a match", Filter{Directory: "/work/api/cmd"}, ""},
		{"limit", Filter{Limit: 2}, "claude_code/claude-sess-1,opencode/ses_parent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions, err := List(sources, tc.filter)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := strings.Join(sessionKeys(sessions), ","); got != tc.want {
				t.Fatalf("sessions = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListToleratesMissingStores(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	empty := t.TempDir()
	sources := DefaultSources(StoreDirs{
		HarnessClaude:   filepath.Join(empty, "claude"),
		HarnessCodex:    filepath.Join(empty, "codex"),
		HarnessOpenCode: filepath.Join(empty, "opencode"),
		HarnessCline:    filepath.Join(empty, "cline"),
	})
	sessions, err := List(sources, Filter{})
	if err != nil || len(sessions) != 0 {
		t.Fatalf("List over missing stores = %v, %v; want nothing and no error", sessions, err)
	}
}

type fakeSource struct {
	harness  string
	sessions []Session
	err      error
}

func (s fakeSource) Harness() string          { return s.harness }
func (s fakeSource) List() ([]Session, error) { return s.sessions, s.err }

func TestListReportsAFailingStoreAndKeepsTheOthers(t *testing.T) {
	broken := errors.New("permission denied")
	sources := []Source{
		fakeSource{harness: HarnessClaude, err: broken},
		fakeSource{harness: HarnessCodex, sessions: []Session{{Harness: HarnessCodex, ID: "c1"}}},
	}
	sessions, err := List(sources, Filter{})
	if len(sessions) != 1 || sessions[0].ID != "c1" {
		t.Fatalf("sessions = %v, want the readable store's session", sessionKeys(sessions))
	}
	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Harness != HarnessClaude || !errors.Is(err, broken) {
		t.Fatalf("err = %v, want a SourceError naming claude_code", err)
	}
}

func TestListOrdersTiesDeterministically(t *testing.T) {
	when := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	sources := []Source{
		fakeSource{harness: HarnessCodex, sessions: []Session{{Harness: HarnessCodex, ID: "b", UpdatedAt: when}, {Harness: HarnessCodex, ID: "a", UpdatedAt: when}}},
		fakeSource{harness: HarnessClaude, sessions: []Session{{Harness: HarnessClaude, ID: "z", UpdatedAt: when}, {Harness: HarnessClaude, ID: ""}}},
	}
	sessions, _ := List(sources, Filter{})
	if got := strings.Join(sessionKeys(sessions), ","); got != "claude_code/z,codex_cli/a,codex_cli/b" {
		t.Fatalf("sessions = %s; ties break on harness then id, and id-less sessions are dropped", got)
	}
}

func TestFind(t *testing.T) {
	_, sources := fixtureSources(t)
	for _, tc := range []struct {
		name, harness, id, want string
	}{
		{"exact id", "", "codex-thread-1", "codex_cli/codex-thread-1"},
		{"unique prefix", "", "claude-s", "claude_code/claude-sess-1"},
		{"subagent by exact id", "", "ses_child", "opencode/ses_child"},
		{"harness narrows", HarnessCline, "cline-task-1", "cline/cline-task-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Find(sources, tc.harness, tc.id)
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if key := got.Harness + "/" + got.ID; key != tc.want {
				t.Fatalf("Find = %s, want %s", key, tc.want)
			}
		})
	}
}

func TestFindRefusesAmbiguousShortAndUnknownIDs(t *testing.T) {
	_, sources := fixtureSources(t)

	if _, err := Find(sources, "", "ses_"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a prefix shorter than %d characters must not match, got %v", MinPrefixLength, err)
	}
	if _, err := Find(sources, "", "no-such-session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id err = %v, want ErrNotFound", err)
	}
	if _, err := Find(sources, HarnessCodex, "claude-sess-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("--harness must scope the search, got %v", err)
	}
	if _, err := Find(sources, "", "  "); err == nil {
		t.Fatal("empty id must be rejected")
	}

	var ambiguous *AmbiguousError
	_, err := Find(sources, "", "ses_pa")
	if err != nil {
		t.Fatalf("ses_pa names only the parent: %v", err)
	}
	dupes := []Source{
		fakeSource{harness: HarnessClaude, sessions: []Session{{Harness: HarnessClaude, ID: "same-id-1"}}},
		fakeSource{harness: HarnessCodex, sessions: []Session{{Harness: HarnessCodex, ID: "same-id-1"}}},
	}
	if _, err := Find(dupes, "", "same-id-1"); !errors.As(err, &ambiguous) || len(ambiguous.Candidates) != 2 {
		t.Fatalf("an id two runtimes share must be ambiguous, got %v", err)
	}
	if !strings.Contains(ambiguous.Error(), "--harness") {
		t.Fatalf("ambiguity error should say how to resolve it: %v", ambiguous)
	}
	prefixDupes := []Source{fakeSource{harness: HarnessCodex, sessions: []Session{{Harness: HarnessCodex, ID: "abcdef-1"}, {Harness: HarnessCodex, ID: "abcdef-2"}}}}
	if _, err := Find(prefixDupes, "", "abcdef"); !errors.As(err, &ambiguous) {
		t.Fatalf("a prefix naming two sessions must be ambiguous, got %v", err)
	}
	exactWins := []Source{fakeSource{harness: HarnessCodex, sessions: []Session{{Harness: HarnessCodex, ID: "abcdef"}, {Harness: HarnessCodex, ID: "abcdef-2"}}}}
	if got, err := Find(exactWins, "", "abcdef"); err != nil || got.ID != "abcdef" {
		t.Fatalf("an exact match must beat a longer prefix match, got %v, %v", got, err)
	}
}

func TestFindMentionsUnreadableStoresWhenNothingMatches(t *testing.T) {
	sources := []Source{fakeSource{harness: HarnessClaude, err: errors.New("disk on fire")}}
	_, err := Find(sources, "", "missing-id")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("err = %v, want not-found that names the unreadable store", err)
	}
}

func TestParseHarness(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "claude": HarnessClaude, "Claude-Code": HarnessClaude, "claude_code": HarnessClaude,
		"codex": HarnessCodex, "codex_cli": HarnessCodex, " opencode ": HarnessOpenCode, "cline": HarnessCline,
	} {
		got, err := ParseHarness(in)
		if err != nil || got != want {
			t.Fatalf("ParseHarness(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseHarness("cursor"); err == nil || !strings.Contains(err.Error(), "supported") {
		t.Fatalf("unsupported runtime err = %v", err)
	}
}

func TestWithinDirectory(t *testing.T) {
	for _, tc := range []struct {
		dir, root string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/b/", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
		{"", "/a", false},
		{"/a/..b", "/a", true},
	} {
		if got := withinDirectory(tc.dir, tc.root); got != tc.want {
			t.Fatalf("withinDirectory(%q, %q) = %v, want %v", tc.dir, tc.root, got, tc.want)
		}
	}
}

// A runtime records the directory it resolved and a shell reports the one the user typed. On macOS
// /var and /tmp are symlinks into /private, so the two differ for the same place.
func TestListDirectoryFilterFollowsSymlinks(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	other := t.TempDir()
	sources := []Source{fakeSource{harness: HarnessClaude, sessions: []Session{
		{Harness: HarnessClaude, ID: "recorded-real", Directory: filepath.Join(real, "pkg")},
		{Harness: HarnessClaude, ID: "recorded-link", Directory: link},
		{Harness: HarnessClaude, ID: "elsewhere", Directory: other},
		{Harness: HarnessClaude, ID: "gone", Directory: filepath.Join(base, "deleted")},
		{Harness: HarnessClaude, ID: "deleted-under-real", Directory: filepath.Join(real, "removed", "sub")},
	}}}
	for _, tc := range []struct {
		root, want string
	}{
		{link, "claude_code/deleted-under-real,claude_code/recorded-link,claude_code/recorded-real"},
		{real, "claude_code/deleted-under-real,claude_code/recorded-link,claude_code/recorded-real"},
		{filepath.Join(link, "removed"), "claude_code/deleted-under-real"},
		{filepath.Join(link, "pkg"), "claude_code/recorded-real"},
		{other, "claude_code/elsewhere"},
		{filepath.Join(base, "missing-root"), ""},
		{filepath.Join(link, "not-created"), ""},
	} {
		sessions, err := List(sources, Filter{Directory: tc.root})
		if err != nil {
			t.Fatal(err)
		}
		keys := sessionKeys(sessions)
		sort.Strings(keys)
		if got := strings.Join(keys, ","); got != tc.want {
			t.Fatalf("--dir %s = %q, want %q", tc.root, got, tc.want)
		}
	}
}

func TestResolveExisting(t *testing.T) {
	base := t.TempDir()
	real, _ := filepath.EvalSymlinks(base)
	for _, tc := range []struct{ in, want string }{
		{base, real},
		{filepath.Join(base, "a", "b"), filepath.Join(real, "a", "b")},
	} {
		if got := resolveExisting(tc.in); got != tc.want {
			t.Fatalf("resolveExisting(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFindNotFoundNamesUnreadableStores(t *testing.T) {
	sources := []Source{
		fakeSource{harness: HarnessClaude, err: errors.New("permission denied")},
		fakeSource{harness: HarnessCodex},
	}
	_, err := Find(sources, "", "missing-id")
	var notFound *NotFoundError
	if !errors.As(err, &notFound) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want a NotFoundError", err)
	}
	if notFound.UnreadableStore(HarnessClaude) == nil || notFound.UnreadableStore(HarnessCodex) != nil {
		t.Fatalf("unreadable = %+v, want only claude_code", notFound.Unreadable)
	}
	if _, err := Find([]Source{fakeSource{harness: HarnessCodex}}, "", "missing-id"); err.Error() != "session not found: missing-id" {
		t.Fatalf("plain not-found err = %q", err)
	}
}
