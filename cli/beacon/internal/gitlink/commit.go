package gitlink

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Commit is the part of a commit attribution reads.
type Commit struct {
	SHA        string    `json:"sha"`
	Parents    []string  `json:"parents,omitempty"`
	CommitTime time.Time `json:"commit_time"`
	Subject    string    `json:"subject"`
	// ParentTime is the first parent's commit time; zero for a root commit.
	ParentTime time.Time `json:"parent_time,omitempty"`
}

// ResolveCommit reads rev (HEAD when empty) as a commit.
func ResolveCommit(ctx context.Context, g Git, rev string) (Commit, error) {
	if rev == "" {
		rev = "HEAD"
	}
	out, err := g.Run(ctx, nil, "show", "-s", "--no-notes", "--format=%H%x00%P%x00%ct%x00%s", rev+"^{commit}", "--")
	if err != nil {
		return Commit{}, fmt.Errorf("resolve commit %s: %w", rev, err)
	}
	fields := strings.SplitN(out, "\x00", 4)
	if len(fields) != 4 {
		return Commit{}, fmt.Errorf("resolve commit %s: unexpected output", rev)
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
	if err != nil {
		return Commit{}, fmt.Errorf("resolve commit %s: commit time %q", rev, fields[2])
	}
	commit := Commit{
		SHA:        strings.TrimSpace(fields[0]),
		Parents:    strings.Fields(fields[1]),
		CommitTime: time.Unix(seconds, 0).UTC(),
		Subject:    fields[3],
	}
	if len(commit.Parents) > 0 {
		parent, err := g.Run(ctx, nil, "show", "-s", "--no-notes", "--format=%ct", commit.Parents[0], "--")
		if err == nil {
			if seconds, err := strconv.ParseInt(strings.TrimSpace(parent), 10, 64); err == nil {
				commit.ParentTime = time.Unix(seconds, 0).UTC()
			}
		}
	}
	return commit, nil
}

// ChangedFiles returns the repository-relative paths commit changes, compared with its first
// parent. A rename or copy contributes both its source and its destination: an agent that moved a
// file edited the path it moved from as much as the one it moved to.
func ChangedFiles(ctx context.Context, g Git, commit string) ([]string, error) {
	out, err := g.RunRaw(ctx, "diff-tree", "--no-commit-id", "--name-status", "-r", "-z", "-M", "--root",
		"--diff-merges=first-parent", commit, "--")
	if err != nil {
		return nil, fmt.Errorf("list files changed by %s: %w", commit, err)
	}
	return parseNameStatusZ(out), nil
}

// parseNameStatusZ reads `--name-status -z` output: a status field and then one path, or two for a
// rename (R) or copy (C).
func parseNameStatusZ(out []byte) []string {
	fields := strings.Split(string(out), "\x00")
	var paths []string
	seen := map[string]bool{}
	add := func(path string) {
		if path != "" && !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	for i := 0; i < len(fields); {
		status := fields[i]
		if status == "" {
			i++
			continue
		}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if i+2 < len(fields) {
				add(fields[i+1])
				add(fields[i+2])
			}
			i += 3
			continue
		}
		if i+1 < len(fields) {
			add(fields[i+1])
		}
		i += 2
	}
	return paths
}
