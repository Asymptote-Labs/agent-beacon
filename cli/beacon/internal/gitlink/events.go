package gitlink

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// LinkAction is the event action recorded in the runtime log when a commit is linked to a session.
const LinkAction = "session.commit_linked"

// AttributionFileOverlap is the only attribution method today: the session wrote files the commit
// changes.
const AttributionFileOverlap = "file_overlap"

// CommitContext is where a commit was made: the branch it landed on and the remote that branch
// tracks. Both are best effort and empty when git cannot say.
type CommitContext struct {
	Branch    string
	RemoteURL string
}

// ResolveCommitContext reads the branch and remote for commit. The branch is HEAD's, and only when
// HEAD is the commit: linking an older commit by hand cannot know which branch it was made on.
func ResolveCommitContext(ctx context.Context, repo Repo, commit Commit) CommitContext {
	var cc CommitContext
	head, err := repo.Git.Run(ctx, nil, "rev-parse", "--verify", "--quiet", "HEAD")
	if err == nil && strings.TrimSpace(head) == commit.SHA {
		if branch, err := repo.Git.Run(ctx, nil, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
			cc.Branch = strings.TrimSpace(branch)
		}
	}
	remote := "origin"
	if cc.Branch != "" {
		if r, err := repo.Git.Run(ctx, nil, "config", "--get", "branch."+cc.Branch+".remote"); err == nil && strings.TrimSpace(r) != "" && strings.TrimSpace(r) != "." {
			remote = strings.TrimSpace(r)
		}
	}
	if u, err := repo.Git.Run(ctx, nil, "config", "--get", "remote."+remote+".url"); err == nil {
		cc.RemoteURL = SanitizeRemoteURL(u)
	}
	return cc
}

// SanitizeRemoteURL removes credentials from a remote URL. An https remote can carry a token as
// its user info (https://x-access-token:ghp_...@github.com/org/repo), and the URL is about to be
// written to a log that may be forwarded. The user of an scp-style remote (git@host:org/repo) is a
// login, not a secret, but it is dropped too so every spelling of a remote reads the same.
func SanitizeRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			// Unparseable: drop anything that could be user info rather than risk keeping it.
			if at := strings.LastIndex(raw, "@"); at >= 0 {
				if scheme := strings.Index(raw, "://"); scheme >= 0 && scheme < at {
					return raw[:scheme+3] + raw[at+1:]
				}
			}
			return raw
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	// scp-like syntax: [user@]host:path. A local path has no colon before its first slash.
	if at := strings.Index(raw, "@"); at >= 0 {
		colon := strings.Index(raw, ":")
		slash := strings.Index(raw, "/")
		if colon > at && (slash < 0 || colon < slash) {
			return raw[at+1:]
		}
	}
	return raw
}

// LinkEvents returns one runtime-log event per link result added, in the session's own timeline:
// harness and session are the linked session's, so the dashboard, traces and handoff briefs show
// the commit alongside the work that produced it.
//
// The events carry no file path, command, or tool arguments. That keeps them metadata only, and it
// keeps them from ever counting as attribution evidence themselves.
func LinkEvents(result Result, cc CommitContext, agentVersion string) []schema.Event {
	candidates := map[string]Candidate{}
	for _, c := range result.Candidates {
		candidates[c.Key()] = c
	}
	var events []schema.Event
	for _, link := range result.Added {
		c := candidates[link.Key()]
		event := schema.NewEvent(schema.NewEventOptions{
			Action:       LinkAction,
			Category:     "session",
			Severity:     schema.SeverityInfo,
			AgentVersion: agentVersion,
			Harness:      schema.HarnessInfo{Name: link.Harness},
			Fidelity:     asymptoteobserve.FidelityInferred,
			Message: fmt.Sprintf("commit %s linked to this session: it wrote %d of the %d file(s) the commit changes",
				shortRevision(result.Commit.SHA), len(c.Files), result.ChangedFiles),
		})
		event.Session = &schema.SessionInfo{ID: link.SessionID, WorkingDirectory: result.RepoRoot}
		event.Repository = result.RepoRoot
		event.Branch = cc.Branch
		head := &schema.VCSRefHeadInfo{Revision: result.Commit.SHA, Name: cc.Branch}
		if cc.Branch != "" {
			head.Type = "branch"
		}
		event.VCS = &schema.VCSInfo{
			Ref: &schema.VCSRefInfo{Head: head},
			Attribution: &schema.VCSAttributionInfo{
				Method:       AttributionFileOverlap,
				ChangedFiles: result.ChangedFiles,
				MatchedFiles: len(c.Files),
			},
		}
		if cc.RemoteURL != "" {
			event.VCS.Repository = &schema.VCSRepositoryInfo{URL: &schema.VCSRepositoryURLInfo{Full: cc.RemoteURL}}
		}
		events = append(events, event)
	}
	return events
}

func shortRevision(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
