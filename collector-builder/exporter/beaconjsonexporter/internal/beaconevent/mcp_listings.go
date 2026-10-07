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
func (l *MCPListings) Fresh(session, tool, description string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := session + "\x00" + tool
	digest := sha256.Sum256([]byte(description))
	if previous, ok := l.seen[key]; ok && previous == digest {
		return false
	}
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
	return true
}

// Filter drops the mcp.tool_listed events a session already listed. Every other event passes through
// in order.
func (l *MCPListings) Filter(events []Event) []Event {
	if l == nil {
		return events
	}
	kept := make([]Event, 0, len(events))
	for _, event := range events {
		if event.Event.Action == "mcp.tool_listed" && event.GenAI != nil && event.GenAI.Tool != nil {
			session := ""
			if event.Session != nil {
				session = event.Session.ID
			}
			if !l.Fresh(session, event.GenAI.Tool.Name, event.GenAI.Tool.Description) {
				continue
			}
		}
		kept = append(kept, event)
	}
	return kept
}
