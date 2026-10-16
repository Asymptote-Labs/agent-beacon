package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/gitlink"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Share URLs in commit notes.
//
// A note line may carry the linked session's shared-trace URL after the link: `beacon:<harness>/<id>
// <url>`. The URL is an identifier like the session id -- it names a page, it carries no content --
// so it belongs in a note that may be pushed. It is only ever an https URL on beacon.sh or on the
// Beacon service this CLI is configured for (gitlink.ValidShareURL); a note can come from anyone
// who pushes to the remote, so any other URL is neither written nor shown.
//
// The post-commit hook takes URLs only from local session.shared events (it never touches the
// network). The pre-push hook, with `beacon git setup --share-sessions`, shares linked sessions
// that have no URL yet during a push the person started, then records the URLs.

const (
	// gitShareMaxSessions bounds how many sessions one push shares.
	gitShareMaxSessions = 10
	// gitShareMaxCommits bounds how many pushed commits are read for links.
	gitShareMaxCommits = 100
	// gitShareNotesReserve is the part of the pre-push budget kept for pushing the notes ref.
	gitShareNotesReserve = 3 * time.Second
	// gitShareNoteWriteBudget is the part kept, after the uploads, for writing the URLs they
	// returned into the local notes. It is local work, so a second is plenty.
	gitShareNoteWriteBudget = time.Second
)

var (
	// gitShareUploadBudget bounds finding and uploading the sessions to share during a push: the
	// pre-push budget less what writing their URLs into the notes and pushing the notes ref need.
	gitShareUploadBudget = gitShareBudget - gitShareNotesReserve - gitShareNoteWriteBudget
	// gitShareUpload is shareUpload; tests replace it.
	gitShareUpload = shareUpload
	// gitShareLogSessions lists the sessions in the runtime log; tests replace it to count reads.
	gitShareLogSessions = func(logPath string) ([]handoff.Session, error) {
		return handoff.LogSessions(logPath, handoff.Filter{})
	}
	// gitShareListStore lists one runtime's stored sessions; tests replace it to count listings.
	gitShareListStore = func(sources []handoff.Source, harness string) ([]handoff.Session, error) {
		return handoff.List(sources, handoff.Filter{Harness: harness, IncludeSubagents: true})
	}
)

// gitShareMaxResolve bounds how many linked sessions without a URL one push looks up, counting the
// ones that are not on this machine (teammates' sessions). Each runtime's store is listed at most
// once per push and the runtime log read at most once, so the cap bounds the per-link work that
// follows (matching, and reading a log session's events), not the number of store listings.
const gitShareMaxResolve = 50

// gitShareStoreIndex finds linked sessions in the runtimes' stores, listing each runtime's store at
// most once per push, on the first link of that runtime. A link names a session by its full id, so
// only an exact, unique id match counts: a prefix match (which handoff.Find would also accept) is
// another session.
type gitShareStoreIndex struct {
	sources []handoff.Source
	byID    map[string]map[string][]handoff.Session // harness -> id -> sessions
}

func newGitShareStoreIndex(sources []handoff.Source) *gitShareStoreIndex {
	return &gitShareStoreIndex{sources: sources, byID: map[string]map[string][]handoff.Session{}}
}

// find returns the stored session of harness with exactly id. ctx is checked before a listing.
func (x *gitShareStoreIndex) find(ctx context.Context, harness, id string) (handoff.Session, bool) {
	index, listed := x.byID[harness]
	if !listed {
		if ctx.Err() != nil {
			return handoff.Session{}, false
		}
		index = map[string][]handoff.Session{}
		// A store that cannot be read lists what it can; the runtime log still answers for it.
		sessions, _ := gitShareListStore(x.sources, harness)
		for _, s := range sessions {
			index[s.ID] = append(index[s.ID], s)
		}
		x.byID[harness] = index
	}
	if matches := index[id]; len(matches) == 1 {
		return matches[0], true
	}
	return handoff.Session{}, false
}

// gitAllowShareHosts admits the configured Beacon service's host besides beacon.sh.
func gitAllowShareHosts() {
	for _, raw := range []string{account.ResolveBaseURL("")} {
		if u, err := url.Parse(raw); err == nil && u.Scheme == "https" {
			gitlink.AllowShareHost(u.Host)
		}
	}
	if session, err := shareAccountLoad(); err == nil && session != nil {
		if u, err := url.Parse(session.BaseURL); err == nil && u.Scheme == "https" {
			gitlink.AllowShareHost(u.Host)
		}
	}
}

func gitShareKey(harness, sessionID string) string {
	name := asymptoteobserve.NormalizeHarnessName(harness)
	if name == "" {
		name = harness
	}
	return name + "/" + sessionID
}

// localShareURLs reads the share URL of every session the runtime log records as shared, from its
// session.shared events, keyed by gitShareKey. Only the current log file is read, and only lines
// that mention the action are decoded, so the post-commit hook stays fast.
func localShareURLs(logPath string) map[string]string {
	urls := map[string]string{}
	f, err := os.Open(logPath)
	if err != nil {
		return urls
	}
	defer f.Close()
	marker := []byte(`"` + asymptoteobserve.ActionSessionShared + `"`)
	reader := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, marker) {
			var event schema.Event
			if json.Unmarshal(line, &event) == nil && event.Event.Action == asymptoteobserve.ActionSessionShared && event.Session != nil && event.Session.ID != "" {
				if u, _ := event.Raw["shared_url"].(string); gitlink.ValidShareURL(u) {
					urls[gitShareKey(event.Harness.Name, event.Session.ID)] = u
				}
			}
		}
		if err != nil {
			return urls
		}
	}
}

// gitShareURLLookup answers gitlink.Options.ShareURL from the runtime log, read once and only when
// a link is about to be written.
func gitShareURLLookup(logPath string) func(gitlink.Link) string {
	var urls map[string]string
	return func(link gitlink.Link) string {
		if urls == nil {
			gitAllowShareHosts()
			urls = localShareURLs(logPath)
		}
		return urls[gitShareKey(link.Harness, link.SessionID)]
	}
}

// gitPushShareReport is what sharing sessions during one push did.
type gitPushShareReport struct {
	Shared  []string `json:"shared,omitempty"`
	Noted   int      `json:"noted"`
	Skipped string   `json:"skipped,omitempty"`
	// OutOfTime is true when the uploads or the note writes ran past their deadline (not when the
	// hook was cancelled).
	OutOfTime bool `json:"out_of_time,omitempty"`
}

// gitShareCollector receives what the share phase of a push finds and uploads, as it happens, so
// the hook can take whatever is there when the upload deadline passes and move on. Once closed it
// ignores further deliveries: an upload the hook abandoned can no longer reach the notes.
type gitShareCollector struct {
	mu       sync.Mutex
	closed   bool
	notes    map[string]string // commit -> note, read before anything is uploaded
	urls     map[string]string // link key -> share URL
	shared   []string
	subjects int
	skipped  string
	err      error
}

func (c *gitShareCollector) update(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		f()
	}
}

func (c *gitShareCollector) fail(err error) {
	c.update(func() {
		if c.err == nil {
			c.err = err
		}
	})
}

// close stops deliveries and returns what was collected.
func (c *gitShareCollector) close() gitShareCollector {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	urls := make(map[string]string, len(c.urls))
	for k, v := range c.urls {
		urls[k] = v
	}
	return gitShareCollector{notes: c.notes, urls: urls, shared: append([]string(nil), c.shared...), subjects: c.subjects, skipped: c.skipped, err: c.err}
}

// gitShareSessionsOnPush shares the sessions linked to the commits being pushed that have no share
// URL in their note yet, and writes the URLs into the notes, before the notes ref is pushed. A
// session already shared from this machine only has its URL written. Without a credential it does
// nothing and says nothing. Every error is returned for one line on stderr, never to fail the push.
//
// The budget is enforced by the clock, not by cooperation: reading the stores and the runtime log,
// building a payload and recording the share do not all watch a context. The share phase runs in a
// goroutine that delivers each URL as its upload finishes; the hook waits for it at most
// gitShareUploadBudget, takes the URLs collected by then and moves on, abandoning the goroutine
// (the hook process exits soon after). Only this function writes notes, and only from what was
// collected, so an abandoned upload never touches them; it may still append its session.shared
// record to the runtime log, which is harmless: that upload did happen. The note writes then get a
// fresh gitShareNoteWriteBudget, and the caller pushes the notes ref on a fresh
// gitShareNotesReserve, so neither is starved by the uploads.
func gitShareSessionsOnPush(parent context.Context, repo gitlink.Repo, remote, stdin, visibility string) (gitPushShareReport, error) {
	var report gitPushShareReport
	gitAllowShareHosts()
	// The uploads end gitShareUploadBudget from now, or earlier when the hook's own deadline leaves
	// less than the note writes and the notes push need after them.
	uploadDeadline := time.Now().Add(gitShareUploadBudget)
	if d, ok := parent.Deadline(); ok {
		if latest := d.Add(-gitShareNoteWriteBudget - gitShareNotesReserve); latest.Before(uploadDeadline) {
			uploadDeadline = latest
		}
	}
	ctx, cancelUploads := context.WithDeadline(parent, uploadDeadline)
	defer cancelUploads()
	collector := &gitShareCollector{urls: map[string]string{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		gitShareFindAndUpload(ctx, collector, repo, remote, stdin, visibility)
	}()
	timedOut := false
	select {
	case <-done:
	case <-ctx.Done():
		timedOut = true
	}
	got := collector.close()
	cancelUploads()
	report.Shared = got.shared
	report.Skipped = got.skipped
	report.OutOfTime = timedOut && errors.Is(ctx.Err(), context.DeadlineExceeded)
	shareErr := got.err
	if timedOut && shareErr == nil && (got.notes == nil || len(got.shared) < got.subjects) {
		shareErr = errors.New("ran out of time before every linked session was shared; the next push shares the rest")
	}
	if len(got.notes) == 0 {
		return report, shareErr
	}
	// Whatever the hook has left, short of what pushing the notes ref keeps, with the note-write
	// budget as a floor.
	noteCtx, cancelNotes := gitShareAfterContext(parent, gitShareNotesReserve, gitShareNoteWriteBudget)
	defer cancelNotes()
	for sha := range got.notes {
		changed, err := gitlink.UpdateNote(noteCtx, repo.Git, sha, func(note string) string { return gitlink.SetLinkURLs(note, got.urls) })
		if err != nil {
			if errors.Is(noteCtx.Err(), context.DeadlineExceeded) {
				report.OutOfTime = true
			}
			if shareErr == nil {
				shareErr = err
			}
			continue
		}
		if changed {
			report.Noted++
		}
	}
	return report, shareErr
}

// gitShareFindAndUpload is the share phase of a push: it reads the pushed commits' notes, finds the
// linked sessions without a URL, and uploads them, delivering each result to c as it comes.
func gitShareFindAndUpload(ctx context.Context, c *gitShareCollector, repo gitlink.Repo, remote, stdin, visibility string) {
	commits, err := gitlink.PushedCommits(ctx, repo, remote, stdin, gitShareMaxCommits)
	if err != nil {
		c.fail(err)
		return
	}
	var order []gitlink.Link
	wanted := map[string]bool{}
	notes := map[string]string{}
	for _, sha := range commits {
		note, err := gitlink.ReadNote(ctx, repo.Git, sha)
		if err != nil || note == "" {
			continue
		}
		notes[sha] = note
		for _, link := range gitlink.ParseNote(note) {
			if link.URL == "" && !wanted[link.Key()] {
				wanted[link.Key()] = true
				order = append(order, link)
			}
		}
	}
	if len(order) == 0 {
		return
	}
	c.update(func() { c.notes = notes })
	known := localShareURLs(shareLogPath())
	var toShare []gitlink.Link
	for _, link := range order {
		if u := known[gitShareKey(link.Harness, link.SessionID)]; u != "" {
			key := link.Key()
			c.update(func() { c.urls[key] = u })
		} else {
			toShare = append(toShare, link)
		}
	}
	type linkedSubject struct {
		link    gitlink.Link
		subject shareSubject
	}
	var subjects []linkedSubject
	if len(toShare) > 0 {
		// Resolve first, cap after: a teammate's session, or one this machine no longer has,
		// must not use up a slot that one of this person's sessions needs.
		stores := newGitShareStoreIndex(handoffSources(handoff.StoreDirs{}))
		logPath := shareLogPath()
		var inLog map[string]bool // read once, on the first link the stores do not have
		for i, link := range toShare {
			if ctx.Err() != nil || len(subjects) >= gitShareMaxSessions || i >= gitShareMaxResolve {
				break
			}
			harness, err := handoff.ParseHarness(link.Harness)
			if err != nil {
				continue
			}
			if session, ok := stores.find(ctx, harness, link.SessionID); ok {
				subjects = append(subjects, linkedSubject{link: link, subject: shareSubject{session: session}})
				continue
			}
			if ctx.Err() != nil {
				break
			}
			if inLog == nil {
				inLog = map[string]bool{}
				if sessions, err := gitShareLogSessions(logPath); err == nil {
					for _, s := range sessions {
						inLog[s.Harness+"\x00"+s.ID] = true
					}
				}
			}
			if !inLog[harness+"\x00"+link.SessionID] {
				continue // a teammate's session, or one this machine no longer has
			}
			session, events, found, err := handoff.LogSession(logPath, link.SessionID, harness)
			if err != nil || !found || session.ID != link.SessionID || session.Harness != harness {
				continue
			}
			subjects = append(subjects, linkedSubject{link: link, subject: shareSubject{session: session, fromLog: true, logEvents: events}})
		}
	}
	if len(subjects) == 0 {
		return
	}
	c.update(func() { c.subjects = len(subjects) })
	client, err := newShareClient()
	if err != nil {
		c.update(func() { c.skipped = "no Beacon credential" })
		return
	}
	for _, ls := range subjects {
		if ctx.Err() != nil {
			return
		}
		key := ls.link.Key()
		result, _, err := gitShareUpload(ctx, client, ls.subject, shareUploadOptions{Visibility: visibility, Record: true})
		if err != nil && result.SharedURL == "" {
			c.fail(fmt.Errorf("%s: %w", key, err))
			continue
		}
		if gitlink.ValidShareURL(result.SharedURL) {
			c.update(func() {
				c.urls[key] = result.SharedURL
				c.shared = append(c.shared, key)
			})
		}
	}
}

// gitShareAfterContext is a fresh context for work after the share phase: it gets what is left of
// parent's deadline less keep (time a later step needs), or floor when that is less, so an upload
// that overran cannot starve it and a share phase that returned at once leaves it the full budget.
// It does not inherit parent's cancellation, only the time and parent's values (among them the
// hook's git spawn counter).
func gitShareAfterContext(parent context.Context, keep, floor time.Duration) (context.Context, context.CancelFunc) {
	budget := floor
	if deadline, ok := parent.Deadline(); ok {
		if remaining := time.Until(deadline) - keep; remaining > budget {
			budget = remaining
		}
	}
	return context.WithTimeout(context.WithoutCancel(parent), budget)
}

// readHookStdin reads what git passes a pre-push hook on stdin, bounded.
func readHookStdin(r io.Reader) string {
	if r == nil {
		return ""
	}
	data, _ := io.ReadAll(io.LimitReader(r, 1<<20))
	return strings.TrimSpace(string(data))
}
