package gitlink

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// testRepo is a real repository in a temp dir, driven with an environment that ignores the
// developer's git configuration.
type testRepo struct {
	t    *testing.T
	root string
	env  []string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(base, "repo")
	for _, dir := range []string{home, root} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r := &testRepo{t: t, root: root, env: IsolatedEnv(home)}
	r.git("init", "-q", "-b", "main")
	r.git("config", "commit.gpgsign", "false")
	return r
}

func (r *testRepo) gitClient() Git { return Git{Dir: r.root, Env: r.env} }

func (r *testRepo) open() Repo {
	r.t.Helper()
	repo, err := OpenRepo(context.Background(), r.gitClient())
	if err != nil {
		r.t.Fatal(err)
	}
	return repo
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	out, err := r.gitClient().Run(context.Background(), nil, args...)
	if err != nil {
		r.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func (r *testRepo) write(rel, content string) {
	r.t.Helper()
	path := filepath.Join(r.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// commitAt stages everything and commits with both dates pinned, returning the new sha.
func (r *testRepo) commitAt(message string, at time.Time) string {
	r.t.Helper()
	r.git("add", "-A")
	stamp := at.UTC().Format(time.RFC3339)
	cmd := exec.Command("git", "-C", r.root, "commit", "-q", "--allow-empty", "-m", message)
	cmd.Env = append(append([]string{}, r.env...), "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("commit: %v\n%s", err, out)
	}
	return r.git("rev-parse", "HEAD")
}

// logEvent is the part of an event the tests vary.
type logEvent struct {
	at        time.Time
	harness   string
	session   string
	cwd       string
	action    string
	filePath  string
	operation string
	command   string
	args      interface{}
}

func (e logEvent) event() schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{Action: e.action, Harness: schema.HarnessInfo{Name: e.harness}})
	ev.Timestamp = schema.FormatTimestamp(e.at)
	if e.session != "" || e.cwd != "" {
		ev.Session = &schema.SessionInfo{ID: e.session, WorkingDirectory: e.cwd}
	}
	if e.filePath != "" {
		ev.File = &schema.FileInfo{Path: e.filePath, Operation: e.operation}
	}
	if e.command != "" {
		ev.Command = &schema.CommandInfo{Command: e.command}
	}
	if e.args != nil {
		ev.GenAI = &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{Call: &schema.GenAIToolCallInfo{Arguments: e.args}}}
	}
	return ev
}

func writeLog(t *testing.T, path string, events ...logEvent) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range events {
		data, err := json.Marshal(e.event())
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeLogAppend(t *testing.T, path string, events ...logEvent) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range events {
		data, err := json.Marshal(e.event())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func keys(links []Link) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, l.Key())
	}
	return out
}
