package git

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

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
	data, err := os.ReadFile(dotGitFile)
	if err != nil {
		return ""
	}

	// Parse "gitdir: <path>" from the .git file
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if gitdir == "" {
		return ""
	}

	// Resolve relative paths against the .git file's parent directory
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(dotGitFile), gitdir)
	}
	gitdir = filepath.Clean(gitdir)

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
