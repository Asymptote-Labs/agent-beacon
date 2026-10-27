package beaconevent

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

// Codex runs on one set of app-server internals in the desktop app and, since about CLI 0.158, in the
// terminal too, and both export OTLP as service.name=codex-app-server. The service name alone
// therefore can't tell the Codex desktop app from the CLI. What does is the `originator` attribute
// Codex puts on its semantic log events: "Codex_Desktop" for the desktop app, "codex-tui" for the
// terminal, "codex_exec" for `codex exec`.
//
// Not every record carries it: the per-turn usage span has only the conversation (thread) id, and
// the legacy token metric not even that. So the surface an originator names is remembered per
// conversation, and a conversation's records without one get the same name. A record with neither
// stays codex_cli, the name Codex's hooks use on every surface.
//
// The logs and traces pipelines flush on their own timers, so a short turn's usage span can reach
// the exporter before the prompt log that names its conversation. Sort holds such a record until
// its conversation is named or codexHoldWindow runs out.

const (
	codexCLIHarness     = "codex_cli"
	codexDesktopHarness = "codex_desktop"
)

// maxCodexConversations bounds the conversations remembered, least recently used first out. A
// Codex conversation's records arrive within its own lifetime, so a few thousand recent
// conversations cover any real machine.
const maxCodexConversations = 4096

// codexHoldWindow is how long Sort holds a record for its conversation's logs. Codex batches its
// logs for up to a second and the collector's batch processor waits up to five more.
const codexHoldWindow = 10 * time.Second

// maxHeldCodexEvents bounds the records held at once; past it the oldest goes out as it is.
const maxHeldCodexEvents = 1024

// CodexSurfaces remembers which Codex surface each conversation runs in. It is shared across every
// signal the exporter receives (logs, traces, metrics arrive on separate pipelines), so it is safe
// for concurrent use. The zero value is ready to use; a nil *CodexSurfaces remembers nothing.
type CodexSurfaces struct {
	mu   sync.Mutex
	byID map[string]*list.Element
	// recent orders the conversations most recently used first; each element holds a
	// codexConversation.
	recent list.List
	// held are the records Sort is waiting to name, oldest first.
	held []heldCodexEvent
}

type heldCodexEvent struct {
	event Event
	until time.Time
}

type codexConversation struct {
	id      string
	surface string
}

// The collector builds a separate exporter for each pipeline (logs, traces, metrics) from one
// exporter config, and the originator arrives only on logs. So the exporters built from one config
// share one CodexSurfaces, or the traces and metrics exporters would never learn what the logs
// exporter did.
var sharedCodexSurfaces = struct {
	sync.Mutex
	byConfig map[any]*codexSurfacesUse
}{byConfig: map[any]*codexSurfacesUse{}}

type codexSurfacesUse struct {
	surfaces *CodexSurfaces
	users    int
}

// AcquireCodexSurfaces returns the CodexSurfaces shared by every exporter built from config, and
// a release for the exporter to call when it shuts down; the last release frees it. config is the
// exporter's config pointer, which the collector passes to each signal's create function.
func AcquireCodexSurfaces(config any) (*CodexSurfaces, func()) {
	sharedCodexSurfaces.Lock()
	defer sharedCodexSurfaces.Unlock()
	use := sharedCodexSurfaces.byConfig[config]
	if use == nil {
		use = &codexSurfacesUse{surfaces: &CodexSurfaces{}}
		sharedCodexSurfaces.byConfig[config] = use
	}
	use.users++
	var once sync.Once
	return use.surfaces, func() {
		once.Do(func() {
			sharedCodexSurfaces.Lock()
			defer sharedCodexSurfaces.Unlock()
			if use.users--; use.users == 0 && sharedCodexSurfaces.byConfig[config] == use {
				delete(sharedCodexSurfaces.byConfig, config)
			}
		})
	}
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
		if s != nil {
			event.codexConversation = conversation
		}
	}
	event.Harness.Name = surface
}

// Sort returns the events to write now, in order: the given ones whose surface is settled, then
// any held earlier whose conversation has since been named or whose wait has run out. A Codex
// app-server record whose conversation is unnamed is held back instead. Every exporter sharing the
// memory calls it with each batch, so a log that names a conversation releases that conversation's
// held records through whichever exporter received the log, and with no events on a timer, so a
// record whose wait ran out is written without waiting for the next batch.
func (s *CodexSurfaces) Sort(events []Event, now time.Time) []Event {
	if s == nil {
		return events
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ready := make([]Event, 0, len(events))
	for _, event := range events {
		if event.codexConversation == "" || s.nameLocked(&event) {
			ready = append(ready, event)
			continue
		}
		s.held = append(s.held, heldCodexEvent{event: event, until: now.Add(codexHoldWindow)})
	}
	for len(s.held) > maxHeldCodexEvents {
		ready = append(ready, s.release(0))
	}
	for i := 0; i < len(s.held); {
		held := &s.held[i]
		if s.nameLocked(&held.event) || !now.Before(held.until) {
			ready = append(ready, s.release(i))
			continue
		}
		i++
	}
	return ready
}

// Drain returns every held record, named where its conversation now is, for an exporter shutting
// down.
func (s *CodexSurfaces) Drain() []Event {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for len(s.held) > 0 {
		s.nameLocked(&s.held[0].event)
		out = append(out, s.release(0))
	}
	return out
}

// nameLocked gives a held record its conversation's surface once one is known, and reports
// whether it did. s.mu is held.
func (s *CodexSurfaces) nameLocked(event *Event) bool {
	element, known := s.byID[event.codexConversation]
	if !known {
		return false
	}
	s.recent.MoveToFront(element)
	event.Harness.Name = element.Value.(*codexConversation).surface
	event.codexConversation = ""
	return true
}

// release removes held record i and returns it as it stands, without the hold marker. s.mu is held.
func (s *CodexSurfaces) release(i int) Event {
	event := s.held[i].event
	event.codexConversation = ""
	s.held = append(s.held[:i], s.held[i+1:]...)
	return event
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
