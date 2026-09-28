package gitlink

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// txRefPrefix holds a notes transaction's working ref. It sits under refs/notes/ because `git
// notes --ref` only operates on refs there, and it is deleted when the transaction ends.
const txRefPrefix = "refs/notes/beacon-tx/"

// noteAttempts is how many times a note update retries after losing the ref to a concurrent
// writer (another worktree's hook committing at the same moment).
const noteAttempts = 3

// ReadNote returns the Beacon note on commit, "" when it has none.
func ReadNote(ctx context.Context, g Git, commit string) (string, error) {
	return readNote(ctx, g, NotesRef, commit)
}

func readNote(ctx context.Context, g Git, ref, commit string) (string, error) {
	out, err := g.Run(ctx, nil, "notes", "--ref="+ref, "show", commit)
	if err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) && strings.Contains(strings.ToLower(gitErr.Message), "no note found") {
			return "", nil
		}
		// A repository whose notes ref does not exist yet says the same thing on every git version
		// Beacon supports, but older ones word it differently; an absent ref is the empty note.
		if exists, existsErr := refExists(ctx, g, ref); existsErr == nil && !exists {
			return "", nil
		}
		return "", err
	}
	return out, nil
}

// AddLinks merges links into commit's note and reports whether the note changed.
func AddLinks(ctx context.Context, g Git, commit string, links []Link) (bool, error) {
	var valid []Link
	for _, link := range links {
		if link.Valid() {
			valid = append(valid, link)
		}
	}
	if len(valid) == 0 {
		return false, nil
	}
	return UpdateNote(ctx, g, commit, func(note string) string { return MergeNote(note, valid) })
}

// NormalizeNote rewrites commit's note in MergeNote's normal form -- no blank lines, no repeated
// link -- and reports whether it changed. `git commit --amend` leaves the amended commit's note
// concatenated onto the one the post-commit hook just wrote; this folds the two back into one.
func NormalizeNote(ctx context.Context, g Git, commit string) (bool, error) {
	return UpdateNote(ctx, g, commit, normalizeNote)
}

// UpdateNote replaces commit's note with update(note) and reports whether it changed. An update
// returning an empty note removes it.
//
// The update is a compare-and-swap on NotesRef: the new notes commit is built on a private working
// ref and then moved into place only if NotesRef still points where it did when the transaction
// began. Two hooks committing at once in two worktrees of one repository therefore never lose each
// other's links; the loser rebuilds on the winner and tries again.
func UpdateNote(ctx context.Context, g Git, commit string, update func(string) string) (bool, error) {
	token, err := txToken()
	if err != nil {
		return false, err
	}
	var lastErr error
	for attempt := 1; attempt <= noteAttempts; attempt++ {
		changed, err := updateNoteOnce(ctx, g, commit, update, txRefPrefix+token+"-"+strconv.Itoa(attempt))
		if err == nil {
			return changed, nil
		}
		if !errors.Is(err, errRefMoved) {
			return false, err
		}
		lastErr = err
	}
	return false, fmt.Errorf("update %s: %w", NotesRef, lastErr)
}

var errRefMoved = errors.New("notes ref moved during update")

func updateNoteOnce(ctx context.Context, g Git, commit string, update func(string) string, txRef string) (changed bool, err error) {
	base, err := resolveRef(ctx, g, NotesRef)
	if err != nil {
		return false, err
	}
	if base != "" {
		if _, err := g.Run(ctx, nil, "update-ref", txRef, base); err != nil {
			return false, err
		}
	}
	defer func() {
		// Best effort, and on a fresh context: a transaction that timed out must still not leave
		// its working ref behind.
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = g.Run(cleanup, nil, "update-ref", "-d", txRef)
	}()

	current, err := readNote(ctx, g, txRef, commit)
	if err != nil {
		return false, err
	}
	next := update(current)
	// git stores a note with exactly one trailing newline and shows it without; compare that way.
	if strings.TrimRight(next, "\n") == strings.TrimRight(current, "\n") {
		return false, nil
	}
	if strings.TrimSpace(next) == "" {
		if _, err := g.Run(ctx, nil, "notes", "--ref="+txRef, "remove", "--ignore-missing", commit); err != nil {
			return false, err
		}
	} else if _, err := g.Run(ctx, []byte(next), "notes", "--ref="+txRef, "add", "--force", "--file=-", commit); err != nil {
		return false, err
	}
	next, err = resolveRef(ctx, g, txRef)
	if err != nil {
		return false, err
	}
	// update-ref with an old value is the swap: it fails if NotesRef moved since base was read. An
	// empty old value asserts the ref does not exist yet.
	if _, err := g.Run(ctx, nil, "update-ref", "-m", "beacon: link sessions", NotesRef, next, base); err != nil {
		if moved, checkErr := refMoved(ctx, g, base); checkErr == nil && moved {
			return false, errRefMoved
		}
		return false, err
	}
	return true, nil
}

func normalizeNote(note string) string { return MergeNote(note, nil) }

func refMoved(ctx context.Context, g Git, base string) (bool, error) {
	now, err := resolveRef(ctx, g, NotesRef)
	if err != nil {
		return false, err
	}
	return now != base, nil
}

// resolveRef returns the object ref points to, "" when it does not exist.
func resolveRef(ctx context.Context, g Git, ref string) (string, error) {
	exists, err := refExists(ctx, g, ref)
	if err != nil || !exists {
		return "", err
	}
	return g.Run(ctx, nil, "rev-parse", "--verify", "--quiet", ref)
}

func refExists(ctx context.Context, g Git, ref string) (bool, error) {
	_, err := g.Run(ctx, nil, "show-ref", "--verify", "--quiet", ref)
	if err == nil {
		return true, nil
	}
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		return false, nil
	}
	return false, err
}

func txToken() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strconv.Itoa(os.Getpid()) + "-" + hex.EncodeToString(b[:]), nil
}

// LinkedCommit is a commit carrying Beacon links.
type LinkedCommit struct {
	SHA        string    `json:"sha"`
	CommitTime time.Time `json:"commit_time"`
	Subject    string    `json:"subject"`
	Links      []Link    `json:"links"`
}

// ListLinked walks up to limit commits reachable from rev (HEAD when empty), newest first, and
// returns those whose Beacon note names at least one session. Links fetched from remotes (see
// RemoteNotesPrefix) are included alongside local ones.
func ListLinked(ctx context.Context, g Git, rev string, limit int) ([]LinkedCommit, error) {
	if rev == "" {
		rev = "HEAD"
	}
	if limit <= 0 {
		limit = 20
	}
	args := []string{"log", "--no-notes"}
	if exists, err := refExists(ctx, g, NotesRef); err != nil {
		return nil, err
	} else if exists {
		args = append(args, "--notes="+NotesRef)
	}
	tracking, err := TrackingRefs(ctx, g)
	if err != nil {
		return nil, err
	}
	if len(tracking) > 0 {
		args = append(args, "--notes="+RemoteNotesPrefix+"*")
	}
	if len(args) == 2 {
		return nil, nil
	}
	// Records end in RS and fields are split by NUL, so a note or subject containing newlines
	// cannot shift fields. --no-notes first clears any notes.displayRef the user configured. With
	// several notes refs, %N holds each one's note in turn; ParseNote folds repeats.
	args = append(args, "-n", strconv.Itoa(limit), "--format=%H%x00%ct%x00%s%x00%N%x1e", rev, "--")
	out, err := g.RunRaw(ctx, args...)
	if err != nil {
		return nil, err
	}
	var commits []LinkedCommit
	for _, record := range strings.Split(string(out), "\x1e") {
		record = strings.TrimLeft(record, "\r\n")
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\x00", 4)
		if len(fields) != 4 {
			continue
		}
		links := ParseNote(fields[3])
		if len(links) == 0 {
			continue
		}
		seconds, _ := strconv.ParseInt(fields[1], 10, 64)
		commits = append(commits, LinkedCommit{
			SHA:        fields[0],
			CommitTime: time.Unix(seconds, 0).UTC(),
			Subject:    fields[2],
			Links:      links,
		})
	}
	return commits, nil
}
