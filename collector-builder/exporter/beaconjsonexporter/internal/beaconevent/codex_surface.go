package beaconevent

import (
	"container/list"
	"strings"
	"sync"
)

// Codex runs on one set of app-server internals in the desktop app and, since CLI 0.163, in the
// terminal too, and both export OTLP as service.name=codex-app-server. The service name alone
// therefore can't tell the Codex desktop app from the CLI. What does is the `originator` attribute
// Codex puts on its semantic log events: "Codex_Desktop" for the desktop app, "codex-tui" for the
// terminal, "codex_exec" for `codex exec`.
//
// Not every record carries it: the per-turn usage span and the token metrics have only the
// conversation (thread) id. So the surface an originator names is remembered per conversation, and
// a conversation's records without one get the same name. A record with neither stays codex_cli,
// the name Codex's hooks use on every surface.

const (
	codexCLIHarness     = "codex_cli"
	codexDesktopHarness = "codex_desktop"
)

// maxCodexConversations bounds the conversations remembered, least recently used first out. A
// Codex conversation's records arrive within its own lifetime, so a few thousand recent
// conversations cover any real machine.
const maxCodexConversations = 4096

// CodexSurfaces remembers which Codex surface each conversation runs in. It is shared across every
// signal the exporter receives (logs, traces, metrics arrive on separate pipelines), so it is safe
// for concurrent use. The zero value is ready to use; a nil *CodexSurfaces remembers nothing.
type CodexSurfaces struct {
	mu   sync.Mutex
	byID map[string]*list.Element
	// recent orders the conversations most recently used first; each element holds a
	// codexConversation.
	recent list.List
}

type codexConversation struct {
	id      string
	surface string
}

// codexSurfaceFromOriginator is the harness an originator names, or "" when there is none.
func codexSurfaceFromOriginator(originator string) string {
	normalized := strings.ToLower(strings.TrimSpace(originator))
	switch {
	case normalized == "":
		return ""
	case strings.Contains(normalized, "desktop"):
		return codexDesktopHarness
	default:
		return codexCLIHarness
	}
}

// isCodexAppServerName is true for the service name both the desktop app and the CLI's app-server
// report, the one name that needs an originator to place.
func isCodexAppServerName(name string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(name)))
	return normalized == "codexappserver"
}

// settle sets a Codex event's harness to the surface it ran in. It only touches events whose name
// came from the shared app-server service name; a name stated outright (beacon.harness.name,
// harness.name) or a CLI-only service (codex_cli_rs, codex_exec) is already exact.
func (s *CodexSurfaces) settle(event *Event, attrs map[string]interface{}) {
	if event == nil || !isCodexHarness(event.Harness.Name) {
		return
	}
	if FirstString(attrs, "beacon.harness.name", "harness.name") != "" {
		return
	}
	if !isCodexAppServerName(FirstString(attrs, "service.name")) {
		return
	}
	conversation := ""
	if event.Session != nil {
		conversation = event.Session.ID
	}
	if conversation == "" {
		conversation = FirstString(attrs, "conversation.id", "thread.id", "conversation_id", "thread_id")
	}
	surface := codexSurfaceFromOriginator(FirstString(attrs, "originator"))
	switch {
	case surface != "":
		s.remember(conversation, surface)
	default:
		surface = s.recall(conversation)
	}
	if surface == "" {
		surface = codexCLIHarness
	}
	event.Harness.Name = surface
}

func (s *CodexSurfaces) remember(conversation, surface string) {
	if s == nil || conversation == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if element, known := s.byID[conversation]; known {
		element.Value.(*codexConversation).surface = surface
		s.recent.MoveToFront(element)
		return
	}
	if s.byID == nil {
		s.byID = make(map[string]*list.Element)
	}
	s.byID[conversation] = s.recent.PushFront(&codexConversation{id: conversation, surface: surface})
	for s.recent.Len() > maxCodexConversations {
		oldest := s.recent.Back()
		s.recent.Remove(oldest)
		delete(s.byID, oldest.Value.(*codexConversation).id)
	}
}

func (s *CodexSurfaces) recall(conversation string) string {
	if s == nil || conversation == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	element, known := s.byID[conversation]
	if !known {
		return ""
	}
	s.recent.MoveToFront(element)
	return element.Value.(*codexConversation).surface
}
