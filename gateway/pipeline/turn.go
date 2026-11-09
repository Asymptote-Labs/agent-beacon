package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
	"unsafe"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/harness"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/store"
)

// Turn is one call as analyzers see it.
//
// A turn carries no per-session memory. Clients resend the whole conversation on every call, so
// the request itself says what is new (the tail after the model's last turn) and which tool call
// each result answers. What a session has reported before, and anything else that must outlive
// the call, lives in Store, so any replica can serve any call.
type Turn struct {
	Call      *gateway.Call
	SessionID string
	Harness   harness.Harness
	// New is what this call adds to what the session has reported. Verdicts should still weigh
	// the whole request: every call puts all of it in front of the model.
	New Delta
	// Store is the shared state store. Analyzers namespace their keys ("<analyzer>/...").
	Store store.Store
	// Events are the Beacon events the events analyzer built for this call, in order. Analyzers
	// after it read them here.
	Events []*Event
	// Resent lists the content of this request the session already reported (system messages,
	// MCP tool definitions), when an analyzer is a ResentReader; nil otherwise.
	Resent []*Resent

	wantsResent bool
	calls       map[string]llm.ToolCall
}

// WantsResent reports whether an analyzer reads Resent, so whoever builds the turn's events
// lists it.
func (t *Turn) WantsResent() bool { return t.wantsResent }

// Delta is the part of a turn that is new.
type Delta struct {
	// System holds system messages whose text the session has not reported before.
	System []llm.Message
	// Tools holds tool definitions the session has not reported before.
	Tools []llm.Tool
	// Tail is the conversation after the model's last turn, system messages excluded: the user
	// turns and tool results this request adds. It needs no memory to compute.
	Tail []llm.Message
	// Output is the model's reply, on the response phase.
	Output *llm.Response
}

// ToolCall returns the call a tool result answers. The conversation is resent with every request,
// so the call is in the same request as its result; a request that does not carry its history
// (a Responses call continuing previous_response_id) has no calls to find.
func (t *Turn) ToolCall(id string) (llm.ToolCall, bool) {
	if t.calls == nil {
		t.calls = map[string]llm.ToolCall{}
		if r := t.Call.Request; r != nil {
			for _, m := range r.Messages {
				for _, c := range llm.ToolCalls(m.Parts) {
					t.calls[c.ID] = c
				}
			}
		}
	}
	c, ok := t.calls[id]
	return c, ok
}

// requestDelta computes what a request adds. The tail is stateless; system text and tool
// definitions are resent on every call, so whether the session reported them before is asked of
// the store, in one batch, unless the memo says this process already confirmed the same set. When
// the store fails, everything counts as new: a record reported twice is better than one never
// reported.
func requestDelta(ctx context.Context, s store.Store, memo *seenMemo, session string, r *llm.Request, ttl time.Duration) (Delta, error) {
	var d Delta
	var err error
	now := time.Now()
	fp := memo.fingerprint(r)
	if !memo.known(session, fp, now) {
		var systems []llm.Message
		var entries []store.Entry
		for _, m := range r.Messages {
			if m.Role == llm.RoleSystem {
				systems = append(systems, m)
				entries = append(entries, seenEntry(session, ttl, "system", m.Text()))
			}
		}
		for _, t := range r.Tools {
			entries = append(entries, seenEntry(session, ttl, "tool", t.Name, t.Description, unsafe.String(unsafe.SliceData(t.Parameters), len(t.Parameters))))
		}
		var added []bool
		added, err = s.Add(ctx, entries...)
		for i, m := range systems {
			if err != nil || added[i] {
				d.System = append(d.System, m)
			}
		}
		for i, t := range r.Tools {
			if err != nil || added[len(systems)+i] {
				d.Tools = append(d.Tools, t)
			}
		}
		if err == nil {
			memo.confirm(session, fp, now.Add(ttl))
		}
	}
	for _, m := range r.Messages[llm.TailStart(r.Messages):] {
		if m.Role != llm.RoleSystem {
			d.Tail = append(d.Tail, m)
		}
	}
	return d, err
}

// seenEntry is the store entry marking content as reported in a session: a digest of its parts,
// hashed in place (copying a 12 KB system prompt or a tool schema to hash it was a sixth of the
// call path's allocation).
func seenEntry(session string, ttl time.Duration, parts ...string) store.Entry {
	h := sha256.New()
	for _, p := range parts {
		h.Write(unsafe.Slice(unsafe.StringData(p), len(p))) // Write neither keeps nor modifies it
		h.Write(sep)
	}
	var sum [sha256.Size]byte
	var hexed [32]byte
	hex.Encode(hexed[:], h.Sum(sum[:0])[:16])
	return store.Entry{Key: "seen/" + session + "/" + string(hexed[:]), Value: seenValue, TTL: ttl}
}

// seenValue is the value of every seen entry; stores copy what they keep.
var seenValue = []byte{1}

var sep = []byte{0}
