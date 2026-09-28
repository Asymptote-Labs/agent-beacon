// Package gitlink links git commits to the agent sessions that produced them.
//
// A link is a line in a git note on the commit, under NotesRef. The note names a session by its
// harness and session id and carries nothing else: notes can travel to a shared remote, so the
// commit says which session wrote it and the session's content stays in the local runtime log.
//
// Which sessions to link is an inference from the runtime log, never an observation. A session is
// linked to a commit when it edited, within the lookback window, a file the commit changes. See
// Attribute.
package gitlink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git runs git against one repository. Dir is any directory inside the work tree.
type Git struct {
	Dir string
	// Env, when set, replaces the process environment for every git invocation. Tests use it to
	// keep the caller's global git configuration out of the repository under test.
	Env []string
	// ExtraEnv is added to the environment (Env, or the process's own) for every invocation.
	ExtraEnv []string
}

// WithExtraEnv returns g with vars added to every invocation's environment.
func (g Git) WithExtraEnv(vars ...string) Git {
	g.ExtraEnv = append(append([]string{}, g.ExtraEnv...), vars...)
	return g
}

// ErrNotRepository is returned when Dir is not inside a git work tree.
var ErrNotRepository = errors.New("not a git repository")

// Run runs git with args and returns its trimmed stdout. stdin, when non-nil, is written to git's
// standard input.
func (g Git) Run(ctx context.Context, stdin []byte, args ...string) (string, error) {
	out, err := g.run(ctx, stdin, args...)
	return strings.TrimRight(string(out), "\r\n"), err
}

// RunRaw is Run without trimming, for output whose trailing bytes are data (NUL-separated lists).
func (g Git) RunRaw(ctx context.Context, args ...string) ([]byte, error) {
	return g.run(ctx, nil, args...)
}

func (g Git) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	full := append([]string{"-C", g.dir()}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if g.Env != nil {
		cmd.Env = g.Env
	}
	if len(g.ExtraEnv) > 0 {
		base := cmd.Env
		if base == nil {
			base = os.Environ()
		}
		cmd.Env = append(append([]string{}, base...), g.ExtraEnv...)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, &GitError{Args: args, Message: msg, Err: err}
	}
	return stdout.Bytes(), nil
}

func (g Git) dir() string {
	if g.Dir == "" {
		return "."
	}
	return g.Dir
}

// GitError is a git invocation that exited unsuccessfully.
type GitError struct {
	Args    []string
	Message string
	Err     error
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), e.Message)
}

func (e *GitError) Unwrap() error { return e.Err }

// ErrorSummary is err in one line for a person: for a failed git invocation, git's own first
// complaint without its "fatal:"/"error:" prefix, rather than the command line and every hint.
func ErrorSummary(err error) string {
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		return err.Error()
	}
	for _, line := range strings.Split(gitErr.Message, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"fatal:", "error:", "remote:"} {
			line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
		if line != "" && !strings.HasPrefix(line, "hint:") && !strings.HasPrefix(line, "To ") {
			return line
		}
	}
	return gitErr.Err.Error()
}

// Repo is a resolved repository: the top of the work tree, the per-worktree git directory, and the
// common directory worktrees share (where hooks and refs live).
type Repo struct {
	Git       Git
	Root      string
	GitDir    string
	CommonDir string
}

// OpenRepo resolves the repository containing g.Dir.
func OpenRepo(ctx context.Context, g Git) (Repo, error) {
	out, err := g.Run(ctx, nil, "rev-parse", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) {
			return Repo{}, fmt.Errorf("%w: %s", ErrNotRepository, g.dir())
		}
		return Repo{}, err
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 || strings.TrimSpace(lines[0]) == "" {
		// A bare repository has no top level, and nothing in one was written by an agent.
		return Repo{}, fmt.Errorf("%w (no work tree): %s", ErrNotRepository, g.dir())
	}
	root := filepath.Clean(strings.TrimSpace(lines[0]))
	gitDir := filepath.Clean(strings.TrimSpace(lines[1]))
	common := strings.TrimSpace(lines[2])
	if !filepath.IsAbs(common) {
		// --git-common-dir is relative to the directory git ran in, not to the top level.
		base, err := filepath.Abs(g.dir())
		if err != nil {
			return Repo{}, err
		}
		common = filepath.Join(base, common)
	}
	return Repo{
		Git:       Git{Dir: root, Env: g.Env, ExtraEnv: g.ExtraEnv},
		Root:      root,
		GitDir:    gitDir,
		CommonDir: filepath.Clean(common),
	}, nil
}

// roots returns the spellings of the work tree root a recorded path may use: the one git reports
// and, when it differs, the one with symlinks resolved (macOS reports /var for /private/var).
func (r Repo) roots() []string {
	roots := []string{r.Root}
	if resolved, err := filepath.EvalSymlinks(r.Root); err == nil && resolved != r.Root {
		roots = append(roots, resolved)
	}
	return roots
}

// RelPath returns path as a slash-separated path relative to the work tree root, and false when
// path is not inside the work tree. A relative path is taken relative to base, which is where the
// session that recorded it was running.
func (r Repo) RelPath(path, base string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	path = strings.TrimPrefix(path, "file://")
	if !filepath.IsAbs(path) {
		if base == "" || !filepath.IsAbs(base) {
			return "", false
		}
		path = filepath.Join(base, path)
	}
	path = filepath.Clean(path)
	candidates := []string{path}
	if resolved, ok := resolveExisting(path); ok && resolved != path {
		candidates = append(candidates, resolved)
	}
	for _, root := range r.roots() {
		for _, candidate := range candidates {
			rel, err := filepath.Rel(root, candidate)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				continue
			}
			rel = filepath.ToSlash(rel)
			if rel == ".git" || strings.HasPrefix(rel, ".git/") {
				return "", false
			}
			return rel, true
		}
	}
	return "", false
}

// resolveExisting resolves symlinks in the longest prefix of path that exists, so a deleted file
// under a symlinked directory still resolves.
func resolveExisting(path string) (string, bool) {
	suffix := ""
	current := path
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			if suffix == "" {
				return resolved, true
			}
			return filepath.Join(resolved, suffix), true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		if suffix == "" {
			suffix = filepath.Base(current)
		} else {
			suffix = filepath.Join(filepath.Base(current), suffix)
		}
		current = parent
	}
}

// IsolatedEnv is an environment for git that ignores the user's global and system configuration.
// Tests use it; nothing in the shipping path does, because a user's hooks run with their config.
func IsolatedEnv(home string) []string {
	env := []string{
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig"),
		"GIT_AUTHOR_NAME=Beacon Test",
		"GIT_AUTHOR_EMAIL=beacon@example.invalid",
		"GIT_COMMITTER_NAME=Beacon Test",
		"GIT_COMMITTER_EMAIL=beacon@example.invalid",
		"GIT_TERMINAL_PROMPT=0",
	}
	for _, key := range []string{"PATH", "SYSTEMROOT", "TMPDIR", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
