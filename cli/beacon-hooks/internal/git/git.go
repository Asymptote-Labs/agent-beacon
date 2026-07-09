package git

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
// Unlike findGitDir, a worktree ".git" pointer file resolves to the
// per-worktree gitdir, which holds this checkout's HEAD; the shared commondir
// HEAD would report another worktree's branch.
func findHeadFile(cwd string) string {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ""
		}
	}

	dir := cwd
	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			if info.IsDir() {
				return filepath.Join(gitPath, "HEAD")
			}
			if gitdir := parseGitDirPointer(gitPath); gitdir != "" {
				head := filepath.Join(gitdir, "HEAD")
				if _, err := os.Stat(head); err == nil {
					return head
				}
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
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

// GetRemoteInfo returns the owner and repository name from the git remote origin.
// Returns empty strings if not in a git repo or no origin remote is configured.
// Supports both SSH (git@github.com:owner/name.git) and HTTPS (https://github.com/owner/name.git) URLs.
// If cwd is provided, it uses that directory as the starting point; otherwise uses os.Getwd().
func GetRemoteInfo(cwd string) (owner, name string) {
	// Find .git directory by walking up from cwd
	gitDir := findGitDir(cwd)
	if gitDir == "" {
		return "", ""
	}

	// Read .git/config
	configPath := filepath.Join(gitDir, "config")
	url := parseRemoteOriginURL(configPath)
	if url == "" {
		return "", ""
	}

	// Parse owner/name from URL
	return parseGitURL(url)
}

// findGitDir walks up the directory tree to find the .git directory.
// If cwd is provided, it uses that directory as the starting point; otherwise uses os.Getwd().
// Handles both normal repositories (.git is a directory) and worktrees (.git is a file
// containing a "gitdir:" reference).
func findGitDir(cwd string) string {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ""
		}
	}

	dir := cwd
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				// Normal repository: .git is a directory
				return gitPath
			}
			// Worktree: .git is a file with "gitdir: <path>"
			if resolved := resolveWorktreeGitDir(gitPath); resolved != "" {
				return resolved
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root
			return ""
		}
		dir = parent
	}
}

// resolveWorktreeGitDir reads a .git file (as found in worktrees),
// extracts the gitdir path, and resolves the common git directory
// that contains the config with remote URLs.
func resolveWorktreeGitDir(dotGitFile string) string {
	gitdir := parseGitDirPointer(dotGitFile)
	if gitdir == "" {
		return ""
	}

	// In a worktree, gitdir points to e.g. /repo/.git/worktrees/foo
	// which contains a "commondir" file with a relative path to the shared .git dir
	commondirPath := filepath.Join(gitdir, "commondir")
	if commondirData, err := os.ReadFile(commondirPath); err == nil {
		commondir := strings.TrimSpace(string(commondirData))
		if commondir != "" {
			if !filepath.IsAbs(commondir) {
				commondir = filepath.Join(gitdir, commondir)
			}
			commondir = filepath.Clean(commondir)
			// Verify it looks like a git dir (has a config file)
			if _, err := os.Stat(filepath.Join(commondir, "config")); err == nil {
				return commondir
			}
		}
	}

	// No commondir means this is not a worktree (likely a submodule).
	// Return "" so findGitDir continues walking up to the parent repo.
	return ""
}

// parseRemoteOriginURL reads .git/config and extracts the origin remote URL
func parseRemoteOriginURL(configPath string) string {
	file, err := os.Open(configPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inOriginSection := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Check for section headers
		if strings.HasPrefix(line, "[") {
			inOriginSection = line == `[remote "origin"]`
			continue
		}

		// Look for url in origin section
		if inOriginSection && strings.HasPrefix(line, "url") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}

	return ""
}

// parseGitURL extracts owner and repository name from a git URL
// Supports:
// - git@github.com:owner/name.git
// - git@github.com:owner/name
// - https://github.com/owner/name.git
// - https://github.com/owner/name
// - ssh://git@github.com/owner/name.git
func parseGitURL(url string) (owner, name string) {
	// SSH format: git@github.com:owner/name.git
	sshRegex := regexp.MustCompile(`^git@[^:]+:([^/]+)/([^/]+?)(?:\.git)?$`)
	if matches := sshRegex.FindStringSubmatch(url); len(matches) == 3 {
		return matches[1], matches[2]
	}

	// HTTPS format: https://github.com/owner/name.git
	httpsRegex := regexp.MustCompile(`^https?://[^/]+/([^/]+)/([^/]+?)(?:\.git)?$`)
	if matches := httpsRegex.FindStringSubmatch(url); len(matches) == 3 {
		return matches[1], matches[2]
	}

	// SSH with ssh:// prefix: ssh://git@github.com/owner/name.git
	sshPrefixRegex := regexp.MustCompile(`^ssh://[^/]+/([^/]+)/([^/]+?)(?:\.git)?$`)
	if matches := sshPrefixRegex.FindStringSubmatch(url); len(matches) == 3 {
		return matches[1], matches[2]
	}

	return "", ""
}
