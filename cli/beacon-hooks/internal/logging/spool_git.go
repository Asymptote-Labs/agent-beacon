package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// ensureSpoolGitExclude hides a freshly created spool directory from `git status` by adding
// its workspace-relative path to the repository's .git/info/exclude.
//
// The spool is telemetry that exists only while a sandboxed session is being captured, so an
// untracked `.beacon/dsh-spool/` showing up in an operator's status output for the duration
// is exactly the repository pollution the spool design has to avoid. .git/info/exclude (not
// .gitignore) is the mechanism git reserves for local, unshared ignores, and it is the same
// one Beacon's cloud setup script already uses for its repo-local files -- add-only, never
// removed, because an entry naming a path that no longer exists is inert.
//
// Everything here is best-effort: a missing or unwritable exclude file means the directory
// stays visible in git status, which costs a line of noise and no correctness. A linked
// worktree (.git is a file, not a directory) is skipped rather than resolved -- writing into
// a worktree's shared git dir from a telemetry hook is a step too far for a hint.
func ensureSpoolGitExclude(spoolDir string) {
	root := gitRootAbove(spoolDir)
	if root == "" {
		return
	}
	rel, err := filepath.Rel(root, spoolDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	entry := filepath.ToSlash(rel) + "/"
	exclude := filepath.Join(root, ".git", "info", "exclude")
	data, err := os.ReadFile(exclude)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	if excludeHasEntry(data, entry) {
		return
	}
	var out bytes.Buffer
	// A file that does not end in a newline would otherwise have the entry glued onto its
	// last line, silently ignoring it.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		out.WriteByte('\n')
	}
	out.WriteString(entry)
	out.WriteByte('\n')
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(out.Bytes())
}

// excludeHasEntry reports whether data already lists entry as one of its pattern lines.
// Comments and blank lines never match because the entry always ends in a slash.
func excludeHasEntry(data []byte, entry string) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if strings.TrimSpace(string(line)) == entry {
			return true
		}
	}
	return false
}

// gitRootAbove walks up from dir looking for the working tree that contains it, and returns
// the tree root, or "" when there is none above a directory whose .git is a file (a linked
// worktree: the shared git dir lives elsewhere and is not ours to edit).
func gitRootAbove(dir string) string {
	for {
		info, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			if info.IsDir() {
				return dir
			}
			return ""
		}
		if !os.IsNotExist(err) {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
