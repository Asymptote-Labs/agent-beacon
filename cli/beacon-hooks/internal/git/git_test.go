package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestCurrentBranchNormalRepo(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")

	if got := CurrentBranch(repo); got != "main" {
		t.Fatalf("CurrentBranch = %q, want main", got)
	}
}

func TestCurrentBranchWithSlashes(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/feature/custom-fields\n")

	if got := CurrentBranch(repo); got != "feature/custom-fields" {
		t.Fatalf("CurrentBranch = %q, want feature/custom-fields", got)
	}
}

func TestCurrentBranchWalksUpFromSubdirectory(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	nested := filepath.Join(repo, "pkg", "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	if got := CurrentBranch(nested); got != "main" {
		t.Fatalf("CurrentBranch = %q, want main", got)
	}
}

func TestCurrentBranchDetachedHead(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, filepath.Join(repo, ".git", "HEAD"), "0123456789abcdef0123456789abcdef01234567\n")

	if got := CurrentBranch(repo); got != "" {
		t.Fatalf("CurrentBranch = %q, want empty for detached HEAD", got)
	}
}

func TestCurrentBranchOutsideRepo(t *testing.T) {
	if got := CurrentBranch(t.TempDir()); got != "" {
		t.Fatalf("CurrentBranch = %q, want empty outside a repo", got)
	}
}

func TestCurrentBranchWorktreeUsesPerWorktreeHead(t *testing.T) {
	base := t.TempDir()
	mainRepo := filepath.Join(base, "main-repo")
	worktree := filepath.Join(base, "wt")

	writeTestFile(t, filepath.Join(mainRepo, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeTestFile(t, filepath.Join(mainRepo, ".git", "config"), "[remote \"origin\"]\n\turl = git@github.com:asymptote-labs/agent-beacon.git\n")
	worktreeGitDir := filepath.Join(mainRepo, ".git", "worktrees", "wt")
	writeTestFile(t, filepath.Join(worktreeGitDir, "HEAD"), "ref: refs/heads/feature/wt-branch\n")
	writeTestFile(t, filepath.Join(worktreeGitDir, "commondir"), "../..\n")
	writeTestFile(t, filepath.Join(worktree, ".git"), "gitdir: "+worktreeGitDir+"\n")

	if got := CurrentBranch(worktree); got != "feature/wt-branch" {
		t.Fatalf("CurrentBranch = %q, want the worktree's own branch", got)
	}
	if got := CurrentBranch(mainRepo); got != "main" {
		t.Fatalf("CurrentBranch = %q, want the main checkout's branch", got)
	}
	// The commondir path used by GetRemoteInfo must keep working after the
	// shared pointer-parsing refactor.
	owner, name := GetRemoteInfo(worktree)
	if owner != "asymptote-labs" || name != "agent-beacon" {
		t.Fatalf("GetRemoteInfo = %q/%q, want asymptote-labs/agent-beacon", owner, name)
	}
}

func TestCurrentBranchWorktreeRelativeGitDirPointer(t *testing.T) {
	base := t.TempDir()
	mainRepo := filepath.Join(base, "main-repo")
	worktree := filepath.Join(base, "wt")

	worktreeGitDir := filepath.Join(mainRepo, ".git", "worktrees", "wt")
	writeTestFile(t, filepath.Join(worktreeGitDir, "HEAD"), "ref: refs/heads/relative-branch\n")
	writeTestFile(t, filepath.Join(worktree, ".git"), "gitdir: ../main-repo/.git/worktrees/wt\n")

	if got := CurrentBranch(worktree); got != "relative-branch" {
		t.Fatalf("CurrentBranch = %q, want relative-branch", got)
	}
}

func TestCurrentBranchRejectsMalformedHead(t *testing.T) {
	tests := []struct {
		name string
		head string
	}{
		{"empty", ""},
		{"non-branch ref", "ref: refs/remotes/origin/main\n"},
		{"missing name", "ref: refs/heads/\n"},
		{"embedded space", "ref: refs/heads/bad name\n"},
		{"control character", "ref: refs/heads/bad\x01name\n"},
		{"overlong name", "ref: refs/heads/" + strings.Repeat("a", 300) + "\n"},
		{"garbage", "not a head file\n"},
		{"oversized garbage", strings.Repeat("x", 2*maxGitMetadataBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			writeTestFile(t, filepath.Join(repo, ".git", "HEAD"), tt.head)
			if got := CurrentBranch(repo); got != "" {
				t.Fatalf("CurrentBranch = %q, want empty for %s HEAD", got, tt.name)
			}
		})
	}
}
