package git

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxGitMetadataBytes caps reads of repository metadata files so hostile or
// corrupt .git contents cannot balloon hook memory or telemetry.
const maxGitMetadataBytes = 4096

// CurrentBranch returns the branch checked out in the repository containing
// cwd, reading HEAD directly so no git binary is required. It returns "" when
// cwd is not inside a git repository, HEAD is detached, or HEAD does not
// parse as a local branch ref.
func CurrentBranch(cwd string) string {
	headPath := findHeadFile(cwd)
	if headPath == "" {
		return ""
	}
	ref, ok := strings.CutPrefix(firstNonEmptyLine(readCapped(headPath)), "ref:")
	if !ok {
		return ""
	}
	branch, ok := strings.CutPrefix(strings.TrimSpace(ref), "refs/heads/")
	if !ok || !isValidBranchName(branch) {
		return ""
	}
	return branch
}

// findHeadFile walks up from cwd to the HEAD file of the innermost checkout.
// A worktree ".git" pointer file resolves to the per-worktree gitdir, which
// holds this checkout's HEAD; the shared commondir HEAD would report another
// worktree's branch.
func findHeadFile(cwd string) string {
	cwd = physicalDir(cwd)
	if cwd == "" {
		return ""
	}

	dir := cwd
	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			if info.IsDir() {
				return filepath.Join(gitPath, "HEAD")
			}
			if gitdir := parseGitDirPointer(gitPath); gitdir != "" {
				// A parsed pointer marks this directory as its own
				// checkout. A missing HEAD means the checkout is broken;
				// walking on would misattribute an enclosing repo's branch.
				head := filepath.Join(gitdir, "HEAD")
				if _, err := os.Stat(head); err == nil {
					return head
				}
				return ""
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// physicalDir returns the directory Git would start discovery from: cwd (or the
// process working directory when empty) with symlinks resolved. A directory alias can
// point inside a different repository, and Git walks the physical path's ancestors, not
// the alias's. An unresolvable cwd is returned as given.
func physicalDir(cwd string) string {
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return ""
		}
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return cwd
}

// parseGitDirPointer reads a ".git" pointer file ("gitdir: <path>") and
// returns the referenced gitdir as an absolute cleaned path, or "".
func parseGitDirPointer(dotGitFile string) string {
	gitdir, ok := strings.CutPrefix(firstNonEmptyLine(readCapped(dotGitFile)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if gitdir == "" {
		return ""
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(dotGitFile), gitdir)
	}
	return filepath.Clean(gitdir)
}

func readCapped(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxGitMetadataBytes))
	if err != nil {
		return ""
	}
	return string(data)
}

func firstNonEmptyLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// isValidBranchName rejects ref names git itself would refuse, so malformed
// or hostile HEAD content never reaches telemetry.
func isValidBranchName(branch string) bool {
	if branch == "" || len(branch) > 255 {
		return false
	}
	for _, r := range branch {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return false
		}
	}
	return true
}
