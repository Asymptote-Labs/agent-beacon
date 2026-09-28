package gitlink

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// RemoteNotesPrefix holds a read-only copy of each remote's links: refs/notes/beacon-remotes/origin
// is origin's refs/notes/beacon as of the last fetch. Readers union it with NotesRef, so a
// teammate's links show up without ever being merged into -- or overwriting -- the local ref.
const RemoteNotesPrefix = "refs/notes/beacon-remotes/"

// incomingRefPrefix holds a one-off fetch from a remote that has no name (a push to a URL).
const incomingRefPrefix = "refs/notes/beacon-incoming/"

// TrackingRef is where remote's links are fetched to.
func TrackingRef(remote string) string { return RemoteNotesPrefix + remote }

// FetchRefspec is the refspec that makes a plain `git fetch <remote>` bring its links along. The
// destination is the tracking ref, never NotesRef: a forced fetch into NotesRef would replace
// links written here and not yet pushed.
func FetchRefspec(remote string) string { return "+" + NotesRef + ":" + TrackingRef(remote) }

// Remotes lists the repository's configured remotes.
func Remotes(ctx context.Context, repo Repo) ([]string, error) {
	out, err := repo.Git.Run(ctx, nil, "remote")
	if err != nil {
		return nil, err
	}
	var remotes []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			remotes = append(remotes, name)
		}
	}
	return remotes, nil
}

func isRemote(ctx context.Context, repo Repo, name string) bool {
	remotes, err := Remotes(ctx, repo)
	if err != nil {
		return false
	}
	for _, r := range remotes {
		if r == name {
			return true
		}
	}
	return false
}

// ConfigureFetch adds FetchRefspec to each remote that lacks it and returns the remotes changed.
func ConfigureFetch(ctx context.Context, repo Repo, remotes []string) ([]string, error) {
	var added []string
	for _, remote := range remotes {
		has, err := hasFetchRefspec(ctx, repo, remote)
		if err != nil {
			return added, err
		}
		if has {
			continue
		}
		if _, err := repo.Git.Run(ctx, nil, "config", "--local", "--add", "remote."+remote+".fetch", FetchRefspec(remote)); err != nil {
			return added, err
		}
		added = append(added, remote)
	}
	return added, nil
}

// UnconfigureFetch removes FetchRefspec from every remote and returns the remotes changed. The
// tracking refs stay: they are copies of what the remote holds, and the next fetch refreshes them.
func UnconfigureFetch(ctx context.Context, repo Repo) ([]string, error) {
	remotes, err := Remotes(ctx, repo)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, remote := range remotes {
		has, err := hasFetchRefspec(ctx, repo, remote)
		if err != nil {
			return removed, err
		}
		if !has {
			continue
		}
		pattern := "^" + regexp.QuoteMeta(FetchRefspec(remote)) + "$"
		if _, err := repo.Git.Run(ctx, nil, "config", "--local", "--unset-all", "remote."+remote+".fetch", pattern); err != nil {
			return removed, err
		}
		removed = append(removed, remote)
	}
	return removed, nil
}

func hasFetchRefspec(ctx context.Context, repo Repo, remote string) (bool, error) {
	out, err := repo.Git.Run(ctx, nil, "config", "--local", "--get-all", "remote."+remote+".fetch")
	if err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) {
			return false, nil
		}
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == FetchRefspec(remote) {
			return true, nil
		}
	}
	return false, nil
}

// FetchNotes copies remote's links into its tracking ref and reports whether the remote had any.
// remote is a remote's name; see fetchInto for a URL.
func FetchNotes(ctx context.Context, repo Repo, remote string) (bool, error) {
	return fetchInto(ctx, repo, remote, TrackingRef(remote))
}

func fetchInto(ctx context.Context, repo Repo, remote, dest string) (bool, error) {
	// --no-write-fetch-head keeps FETCH_HEAD for the person's own fetches; the empty --refmap stops
	// git also updating the remote's configured branch tracking refs.
	_, err := repo.Git.Run(ctx, nil, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--refmap=", remote, "+"+NotesRef+":"+dest)
	if err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) && strings.Contains(strings.ToLower(gitErr.Message), "couldn't find remote ref") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// MergeNotesFrom merges the links in ref into NotesRef and reports whether NotesRef changed. It
// uses git's cat_sort_uniq strategy, which keeps every line of both sides, and the same
// compare-and-swap as UpdateNote, so a hook linking a commit meanwhile is not lost.
func MergeNotesFrom(ctx context.Context, repo Repo, ref string) (bool, error) {
	g := repo.Git
	token, err := txToken()
	if err != nil {
		return false, err
	}
	for attempt := 1; attempt <= noteAttempts; attempt++ {
		incoming, err := resolveRef(ctx, g, ref)
		if err != nil || incoming == "" {
			return false, err
		}
		base, err := resolveRef(ctx, g, NotesRef)
		if err != nil {
			return false, err
		}
		next := incoming
		if base != "" {
			if base == incoming {
				return false, nil
			}
			next, err = mergeOnTx(ctx, g, base, ref, txRefPrefix+token+"-m"+strconv.Itoa(attempt))
			if err != nil {
				return false, err
			}
			if next == base {
				return false, nil
			}
		}
		if _, err := g.Run(ctx, nil, "update-ref", "-m", "beacon: merge shared links", NotesRef, next, base); err != nil {
			if moved, checkErr := refMoved(ctx, g, base); checkErr == nil && moved {
				continue
			}
			return false, err
		}
		return true, nil
	}
	return false, fmt.Errorf("merge into %s: %w", NotesRef, errRefMoved)
}

func mergeOnTx(ctx context.Context, g Git, base, ref, txRef string) (string, error) {
	if _, err := g.Run(ctx, nil, "update-ref", txRef, base); err != nil {
		return "", err
	}
	defer func() { _, _ = g.Run(context.Background(), nil, "update-ref", "-d", txRef) }()
	if _, err := g.Run(ctx, nil, "notes", "--ref="+txRef, "merge", "--quiet", "--strategy=cat_sort_uniq", ref); err != nil {
		return "", err
	}
	return resolveRef(ctx, g, txRef)
}

// PushResult is the outcome of PushNotes.
type PushResult struct {
	Remote string `json:"remote"`
	// Merged is true when the remote had links this repository lacked, now merged in.
	Merged bool `json:"merged"`
	// Pushed is false only when there was nothing local to push.
	Pushed bool `json:"pushed"`
}

// PushNotes shares this repository's links with remote: it fetches the remote's links, merges them
// in, and pushes NotesRef. A push rejected because someone pushed links in between is retried once
// after merging again. remote may be a remote's name or a URL.
//
// The push runs with --no-verify, so it never triggers the repository's own pre-push hooks --
// Beacon's included, which is what calls this during a person's push.
func PushNotes(ctx context.Context, repo Repo, remote string) (PushResult, error) {
	result := PushResult{Remote: remote}
	named := isRemote(ctx, repo, remote)
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		merged, err := fetchAndMerge(ctx, repo, remote, named)
		if err != nil {
			return result, err
		}
		result.Merged = result.Merged || merged
		if exists, err := refExists(ctx, repo.Git, NotesRef); err != nil || !exists {
			return result, err
		}
		_, err = repo.Git.Run(ctx, nil, "push", "--quiet", "--no-verify", remote, NotesRef+":"+NotesRef)
		if err == nil {
			result.Pushed = true
			if named {
				// The remote now holds exactly what was pushed; say so without another round trip.
				if local, err := resolveRef(ctx, repo.Git, NotesRef); err == nil && local != "" {
					_, _ = repo.Git.Run(ctx, nil, "update-ref", TrackingRef(remote), local)
				}
			}
			return result, nil
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		lastErr = err
	}
	return result, lastErr
}

func fetchAndMerge(ctx context.Context, repo Repo, remote string, named bool) (bool, error) {
	dest := TrackingRef(remote)
	if !named {
		token, err := txToken()
		if err != nil {
			return false, err
		}
		dest = incomingRefPrefix + token
		defer func() { _, _ = repo.Git.Run(context.Background(), nil, "update-ref", "-d", dest) }()
	}
	found, err := fetchInto(ctx, repo, remote, dest)
	if err != nil || !found {
		return false, err
	}
	return MergeNotesFrom(ctx, repo, dest)
}

// TrackingRefs lists the refs holding remotes' links.
func TrackingRefs(ctx context.Context, g Git) ([]string, error) {
	out, err := g.Run(ctx, nil, "for-each-ref", "--format=%(refname)", RemoteNotesPrefix)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		if ref := strings.TrimSpace(line); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}
