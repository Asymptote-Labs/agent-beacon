package gitlink

import (
	"context"
	"sort"
	"time"
)

const (
	// DefaultMinLookback is how far before a commit attribution always looks. It is the floor
	// under the parent-commit bound below, so a commit made right after its parent still sees the
	// edits that led up to both.
	DefaultMinLookback = 2 * time.Hour
	// DefaultMaxLookback caps how far back attribution looks, however old the parent commit is.
	// It bounds the scan on a branch that sat untouched for a month.
	DefaultMaxLookback = 24 * time.Hour
	// commitTimeSlack admits events stamped just after the commit: commit times have one-second
	// resolution and a hook's event can reach the log a moment after git wrote the commit.
	commitTimeSlack = time.Minute
	// MaxLinks bounds how many sessions one commit is linked to.
	MaxLinks = 32
)

// Options configures Attribute.
type Options struct {
	// Rev is the commit to attribute, HEAD when empty.
	Rev string
	// LogPath is the runtime JSONL log to read sessions from.
	LogPath     string
	MinLookback time.Duration
	MaxLookback time.Duration
	// DryRun computes the links without writing the note.
	DryRun bool
}

// Candidate is a session that wrote at least one file the commit changes.
type Candidate struct {
	Link
	// Files are the commit's files the session wrote, sorted.
	Files       []string  `json:"files"`
	LastWriteAt time.Time `json:"last_write_at"`
	// AlreadyLinked is true when the commit's note already named the session.
	AlreadyLinked bool `json:"already_linked,omitempty"`
}

// Result is the outcome of attributing one commit.
type Result struct {
	Commit       Commit      `json:"commit"`
	RepoRoot     string      `json:"repo_root"`
	Since        time.Time   `json:"since"`
	Until        time.Time   `json:"until"`
	ChangedFiles int         `json:"changed_files"`
	Candidates   []Candidate `json:"candidates"`
	// Added are the links this run wrote (or, on a dry run, would write).
	Added []Link `json:"added"`
	// NoteUpdated is true when the note on the commit changed.
	NoteUpdated bool `json:"note_updated"`
	DryRun      bool `json:"dry_run,omitempty"`
}

// Window returns the span of runtime-log time attribution reads for commit: from the later of the
// parent commit and maxLookback before the commit -- but never less than minLookback before it --
// up to the commit plus a small slack.
//
// The parent is the natural lower bound: edits made since the previous commit are the ones this
// commit can contain. The floor keeps work that straddled the parent, and the cap keeps a stale
// parent from turning a hook into a scan of the endpoint's whole history.
func Window(commit Commit, minLookback, maxLookback time.Duration) (since, until time.Time) {
	if minLookback <= 0 {
		minLookback = DefaultMinLookback
	}
	if maxLookback < minLookback {
		maxLookback = minLookback
	}
	until = commit.CommitTime.Add(commitTimeSlack)
	since = commit.CommitTime.Add(-maxLookback)
	if !commit.ParentTime.IsZero() && commit.ParentTime.After(since) {
		since = commit.ParentTime
	}
	if floor := commit.CommitTime.Add(-minLookback); since.After(floor) {
		since = floor
	}
	return since, until
}

// Attribute links the commit to the sessions in the runtime log that wrote the files it changes.
//
// A session is a candidate when, inside Window, it wrote at least one file the commit changes.
// Candidates are ranked by how many of the commit's files they wrote, then by how recently, and
// the top MaxLinks are merged into the commit's note. The link is inferred from overlap and says
// so everywhere it is reported: a session that edited a file a person then rewrote by hand is
// still linked, which is the right default for "what produced this commit", and a session that
// wrote nothing the commit kept is not.
func Attribute(ctx context.Context, repo Repo, opts Options) (Result, error) {
	commit, err := ResolveCommit(ctx, repo.Git, opts.Rev)
	if err != nil {
		return Result{}, err
	}
	since, until := Window(commit, opts.MinLookback, opts.MaxLookback)
	result := Result{Commit: commit, RepoRoot: repo.Root, Since: since, Until: until, DryRun: opts.DryRun}

	changed, err := ChangedFiles(ctx, repo.Git, commit.SHA)
	if err != nil {
		return Result{}, err
	}
	result.ChangedFiles = len(changed)
	if len(changed) == 0 {
		return result, nil
	}
	evidence, err := ReadEvidence(ctx, repo, opts.LogPath, since, until)
	if err != nil {
		return Result{}, err
	}
	note, err := ReadNote(ctx, repo.Git, commit.SHA)
	if err != nil {
		return Result{}, err
	}
	existing := map[string]bool{}
	for _, link := range ParseNote(note) {
		existing[link.Key()] = true
	}

	result.Candidates = rankCandidates(evidence, changed, existing)
	for _, c := range result.Candidates {
		if !c.AlreadyLinked && len(result.Added) < MaxLinks {
			result.Added = append(result.Added, c.Link)
		}
	}
	if len(result.Added) == 0 || opts.DryRun {
		return result, nil
	}
	updated, err := AddLinks(ctx, repo.Git, commit.SHA, result.Added)
	if err != nil {
		return Result{}, err
	}
	result.NoteUpdated = updated
	return result, nil
}

func rankCandidates(evidence map[string]*SessionEvidence, changed []string, existing map[string]bool) []Candidate {
	var candidates []Candidate
	for key, session := range evidence {
		var files []string
		for _, path := range changed {
			if _, ok := session.Files[path]; ok {
				files = append(files, path)
			}
		}
		if len(files) == 0 {
			continue
		}
		sort.Strings(files)
		candidates = append(candidates, Candidate{
			Link:          session.Link,
			Files:         files,
			LastWriteAt:   session.LastWrite(files),
			AlreadyLinked: existing[key],
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if len(a.Files) != len(b.Files) {
			return len(a.Files) > len(b.Files)
		}
		if !a.LastWriteAt.Equal(b.LastWriteAt) {
			return a.LastWriteAt.After(b.LastWriteAt)
		}
		return a.Key() < b.Key()
	})
	return candidates
}
