package gitlink

import (
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/shellcmd"
)

// ShellWrittenPaths returns the files a shell command line evidently writes: the operands of rm,
// touch, truncate, mv, cp, install, ln, tee, `git mv`/`git rm`, in-place sed/perl, and the targets
// of output redirections (shellcmd.FileEffects). It is deliberately shallow. Anything it cannot
// read plainly -- variables, command substitutions, globs, a script that writes files itself --
// contributes nothing, because a wrong path would link a session to a commit it had no part in,
// and a missed one only leaves a link out. A command line that does not parse was captured
// incomplete and contributes nothing.
func ShellWrittenPaths(command string) []string {
	commands, err := shellcmd.Parse(command)
	if err != nil {
		return nil
	}
	var paths []string
	for _, c := range commands {
		for _, effect := range c.FileEffects() {
			// After a cd, a relative path is relative to somewhere the parser does not track.
			if c.AfterCD && !filepath.IsAbs(effect.Path) {
				continue
			}
			paths = append(paths, effect.Path)
		}
	}
	return dedupe(paths)
}

// PatchPaths returns the files an apply_patch envelope touches: Add, Update, Delete, and the
// destination of a Move.
func PatchPaths(patch string) []string {
	var paths []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Add File:", "*** Update File:", "*** Delete File:", "*** Move to:"} {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				if p := strings.TrimSpace(rest); p != "" {
					paths = append(paths, p)
				}
			}
		}
	}
	return dedupe(paths)
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
