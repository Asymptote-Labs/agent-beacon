package gitlink

import (
	"regexp"
	"sort"
	"strings"
)

// NotesRef is the notes ref Beacon's links live under. It is a contract: `beacon git notes`, the
// hooks, and anyone who fetched the ref from a teammate read it by this name.
const NotesRef = "refs/notes/beacon"

// linePrefix starts every line Beacon writes. Lines without it are someone else's -- a person who
// ran `git notes append` by hand, or a tool that shares the ref -- and are kept as they are.
const linePrefix = "beacon:"

// MaxSessionIDLength bounds a session id in a note. Runtime session ids are UUIDs or short
// tokens; anything longer is not one, and a note line has no business carrying it.
const MaxSessionIDLength = 256

var harnessPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// Link names one session a commit is linked to.
type Link struct {
	Harness   string `json:"harness"`
	SessionID string `json:"session_id"`
}

// Key is the link's identity, harness and session together: two runtimes may reuse an id.
func (l Link) Key() string { return l.Harness + "/" + l.SessionID }

// Valid reports whether the link can be written as a note line and read back unchanged.
func (l Link) Valid() bool {
	if !harnessPattern.MatchString(l.Harness) {
		return false
	}
	if l.SessionID == "" || len(l.SessionID) > MaxSessionIDLength {
		return false
	}
	for _, r := range l.SessionID {
		// No whitespace, since a line ends at it, and no control or non-ASCII characters, since a
		// note is displayed by `git log` and must not be able to move the terminal's cursor.
		if r <= ' ' || r >= 0x7f {
			return false
		}
	}
	return true
}

// Line is the note line for the link.
func (l Link) Line() string { return linePrefix + l.Key() }

// ParseLine reads one note line, returning false for a line that is not a Beacon link.
func ParseLine(line string) (Link, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), linePrefix)
	if !ok {
		return Link{}, false
	}
	// Anything after whitespace is reserved for later fields; readers today ignore it.
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		rest = rest[:i]
	}
	harness, session, ok := strings.Cut(rest, "/")
	if !ok {
		return Link{}, false
	}
	link := Link{Harness: harness, SessionID: session}
	if !link.Valid() {
		return Link{}, false
	}
	return link, true
}

// ParseNote returns the Beacon links in a note, in the order they first appear, without repeats.
func ParseNote(note string) []Link {
	var links []Link
	seen := map[string]bool{}
	for _, line := range strings.Split(note, "\n") {
		link, ok := ParseLine(line)
		if !ok || seen[link.Key()] {
			continue
		}
		seen[link.Key()] = true
		links = append(links, link)
	}
	return links
}

// MergeNote returns note with links added. Every existing line is kept verbatim and in place --
// lines that are not Beacon links belong to a person or another tool, and a Beacon line carrying
// fields this version does not know was written by a newer one -- except that blank lines and a
// repeat of a link already seen are dropped. New links are appended in the order given. Merging
// links a note already has returns it unchanged apart from that normalization.
//
// Links are never removed here. A note is shared history, and a teammate's link on the same commit
// is as true as ours.
func MergeNote(note string, links []Link) string {
	var lines []string
	seen := map[string]bool{}
	for _, line := range strings.Split(note, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if link, ok := ParseLine(line); ok {
			if seen[link.Key()] {
				continue
			}
			seen[link.Key()] = true
		}
		lines = append(lines, line)
	}
	for _, link := range links {
		if !link.Valid() || seen[link.Key()] {
			continue
		}
		seen[link.Key()] = true
		lines = append(lines, link.Line())
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// SortLinks orders links by key, for output that must not depend on note order.
func SortLinks(links []Link) {
	sort.Slice(links, func(i, j int) bool { return links[i].Key() < links[j].Key() })
}
