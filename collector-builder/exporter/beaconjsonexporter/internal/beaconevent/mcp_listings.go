package beaconevent

import (
	"crypto/sha256"
	"sync"
)

// maxMCPListings bounds how many session and tool pairs MCPListings remembers. Past it the oldest is
// forgotten, which costs at most one more listing for that tool if its session is still running.
const maxMCPListings = 4096

// MCPListings remembers which MCP tool descriptions each session has already listed. Claude Code
// sends its whole tools array with every model request, so without it each request would list every
// MCP tool again, and a poisoned description would raise its alert on every one of them.
//
// It is held by the claude_api_body processor, so each destination is sent a tool once, and by each
// exporter, which keeps a pipeline without the processor to the same rule. It lasts as long as the
// collector process: a restart forgets it, which costs one listing per tool on the next request. The
// zero value is ready to use, and a nil one treats every listing as new.
type MCPListings struct {
	mu    sync.Mutex
	seen  map[string][sha256.Size]byte
	order []string
}

// Fresh reports whether a session has not yet listed this tool with this description, and
// remembers that it now has.
//
// The processor uses it as it hands a record on. The batch processor after it exports later and
// keeps export errors to itself, so there is no later point at which the processor could learn that
// a listing was not delivered.
func (l *MCPListings) Fresh(session, tool, description string) bool {
	if l == nil {
		return true
	}
	key, digest := listingKey(session, tool, description)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.listedLocked(key, digest) {
		return false
	}
	l.rememberLocked(key, digest)
	return true
}

// Filter drops the mcp.tool_listed events a session already listed, and repeats of one listing
// within events. Every other event passes through in order.
//
// It does not remember the listings it keeps. An exporter calls Remember for the ones it wrote or
// sent, so a listing whose write or send failed is listed again on the session's next request rather
// than taken for one already delivered.
func (l *MCPListings) Filter(events []Event) []Event {
	if l == nil {
		return events
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := make([]Event, 0, len(events))
	inBatch := map[string][sha256.Size]byte{}
	for _, event := range events {
		if key, digest, ok := eventListingKey(event); ok {
			if previous, repeat := inBatch[key]; (repeat && previous == digest) || l.listedLocked(key, digest) {
				continue
			}
			inBatch[key] = digest
		}
		kept = append(kept, event)
	}
	return kept
}

// Remember records the mcp.tool_listed events among events as listed. Exporters call it once they
// have written or sent them.
func (l *MCPListings) Remember(events ...Event) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, event := range events {
		if key, digest, ok := eventListingKey(event); ok {
			l.rememberLocked(key, digest)
		}
	}
}

func listingKey(session, tool, description string) (string, [sha256.Size]byte) {
	return session + "\x00" + tool, sha256.Sum256([]byte(description))
}

func eventListingKey(event Event) (string, [sha256.Size]byte, bool) {
	if event.Event.Action != "mcp.tool_listed" || event.GenAI == nil || event.GenAI.Tool == nil {
		return "", [sha256.Size]byte{}, false
	}
	session := ""
	if event.Session != nil {
		session = event.Session.ID
	}
	key, digest := listingKey(session, event.GenAI.Tool.Name, event.GenAI.Tool.Description)
	return key, digest, true
}

func (l *MCPListings) listedLocked(key string, digest [sha256.Size]byte) bool {
	previous, ok := l.seen[key]
	return ok && previous == digest
}

func (l *MCPListings) rememberLocked(key string, digest [sha256.Size]byte) {
	if l.seen == nil {
		l.seen = map[string][sha256.Size]byte{}
	}
	if _, ok := l.seen[key]; !ok {
		l.order = append(l.order, key)
	}
	l.seen[key] = digest
	for len(l.seen) > maxMCPListings {
		delete(l.seen, l.order[0])
		l.order = l.order[1:]
	}
}
