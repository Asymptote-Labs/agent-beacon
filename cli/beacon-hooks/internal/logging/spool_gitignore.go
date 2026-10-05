package logging

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// ensureSpoolGitignore keeps a spool directory out of git by writing a `*` ignore file inside
// it the first time it is used.
//
// A spool holds retained content -- prompts, command output, diffs -- inside what is usually
// a repository, and agents commit with `git add -A`. An ignore file in the spool directory
// itself ignores every file there, itself included, so neither `git status` nor `git add -A`
// sees the spool. Unlike an entry in the repository's .gitignore or .git/info/exclude it
// needs no repository at all, so it behaves the same in a plain checkout, a linked worktree,
// a submodule, and a directory that becomes a repository later, and it never edits a file
// Beacon does not own.
//
// It is best-effort: a write that fails leaves the spool visible in git status, which costs a
// line of noise and no correctness. An existing file is left alone, whatever it holds.
func ensureSpoolGitignore(spoolDir string) {
	path := filepath.Join(spoolDir, asymptoteobserve.DSHSpoolGitignore)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString("# Written by Beacon: staged DeepSeek Harness telemetry, drained by `beacon endpoint dsh sync`.\n*\n")
}
