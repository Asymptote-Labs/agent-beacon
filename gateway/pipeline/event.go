package pipeline

import (
	"encoding/json"
	"sync"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	obs "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// KindEvent is the record kind of a Beacon event: a line in runtime.jsonl's schema.
const KindEvent = "beacon.event"

// Event is one Beacon event built from a call. The events analyzer builds a call's events once,
// sanitized as Beacon's writers sanitize them, and puts them on the Turn; analyzers that run after
// it read them there rather than building or sanitizing their own. Events are shared: nobody
// modifies one once it is on the Turn.
type Event struct {
	// Beacon is the event as analyzers read it, without an event.id (ID derives it) and without
	// what only its written record needs (Complete adds that).
	Beacon obs.Event
	// Recorded reports that the event is written. Content a session already reported, which a
	// request sends again, is never recorded again; its events exist only for analyzers that ask
	// for them (Turn.Resent).
	Recorded bool
	// MetadataOnly writes the event as Beacon's Metadata-only forwarding would
	// (asymptoteobserve.StripToMetadata). Analyzers still read it whole, and its id is that of the
	// whole event, as the endpoint stamps its id before forwarding strips anything.
	MetadataOnly bool
	// Complete, when set, adds to a copy of Beacon the fields only the written record needs and
	// no rule reads, such as a content marker, whose redaction pass and hash would otherwise be
	// paid on the call path. Written applies it once.
	Complete func(*obs.Event)

	writtenOnce sync.Once
	written     obs.Event
	idOnce      sync.Once
	id          string
}

// Written is the event as it is written, without its id: Beacon completed. It is derived on first
// use, when the event is written or cited, or when an analyzer asks.
func (e *Event) Written() *obs.Event {
	e.writtenOnce.Do(func() {
		e.written = e.Beacon
		if e.Complete != nil {
			e.Complete(&e.written)
		}
	})
	return &e.written
}

// ID is the event's Beacon identity (asymptoteobserve.EventID of the written event), derived on
// first use: when the event is written, or when an analyzer cites it as evidence.
func (e *Event) ID() string {
	e.idOnce.Do(func() { e.id = obs.EventID(*e.Written()) })
	return e.id
}

// MarshalJSON writes the event as a runtime.jsonl line, with its id.
func (e *Event) MarshalJSON() ([]byte, error) {
	ev := *e.Written()
	ev.Event.ID = e.ID()
	if e.MetadataOnly {
		obs.StripToMetadata(&ev)
	}
	return json.Marshal(ev)
}

// Resent is content a session already reported that a request sends again: a system message or
// a tool definition, which every request of a session repeats. The model reads it again, so an
// analyzer may need to judge it again, but it is not recorded again, and its event is built only
// when an analyzer asks for it (Event). An analyzer that keeps what it concluded about such
// content, keyed by the content, need never ask.
type Resent struct {
	// Action is the event the content becomes: session.context for a system message,
	// mcp.tool_listed for an MCP tool definition.
	Action string
	// System is the system message, for session.context; Tool the definition, for
	// mcp.tool_listed.
	System *llm.Message
	Tool   *llm.Tool

	build func() *Event
	event *Event
}

// NewResent lists content for Turn.Resent; build makes its (unrecorded) event.
func NewResent(action string, system *llm.Message, tool *llm.Tool, build func() *Event) *Resent {
	return &Resent{Action: action, System: system, Tool: tool, build: build}
}

// Event builds the content's event, once.
func (r *Resent) Event() *Event {
	if r.event == nil {
		r.event = r.build()
	}
	return r.event
}
