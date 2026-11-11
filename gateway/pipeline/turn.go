package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
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
	// PreviousResponse is the provider response the call's request continues (OpenAI Responses'
	// previous_response_id): on a request that names one, and on its reply when the reply's
	// session was named from the request's body. Such a call is its own session unless something
	// else names its conversation, so this, with each reply's response id, is what links its turns.
	PreviousResponse string
	Harness          harness.Harness
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
	// TailReported reports that the session already reported this tail: the request is a
	// client's retry of one it sent before. Its tail is still analyzed, but not recorded again.
	TailReported bool
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
// the store, unless the memo says this process already confirmed the same set; and so is whether
// it reported this very tail, which a client's retry sends again, when retries is set (a session
// the client or the gateway named, a request that resends its history). Both are asked in one
// batch. When the store fails, everything counts as new: a record reported twice is better than
// one never reported.
func requestDelta(ctx context.Context, s store.Store, memo *seenMemo, session string, r *llm.Request, ttl time.Duration, retries bool) (Delta, error) {
	var d Delta
	now := time.Now()
	start := llm.TailStart(r.Messages)
	for _, m := range r.Messages[start:] {
		if m.Role != llm.RoleSystem {
			d.Tail = append(d.Tail, m)
		}
	}
	fp := memo.fingerprint(r)
	known := memo.known(session, fp, now)
	var systems []llm.Message
	var entries []store.Entry
	if !known {
		for _, m := range r.Messages {
			if m.Role == llm.RoleSystem {
				systems = append(systems, m)
				entries = append(entries, seenEntry(session, ttl, "system", m.Text()))
			}
		}
		for _, t := range r.Tools {
			entries = append(entries, seenEntry(session, ttl, "tool", t.Name, t.Description, unsafe.String(unsafe.SliceData(t.Parameters), len(t.Parameters))))
		}
	}
	tail := -1
	if len(d.Tail) > 0 && retries {
		tail = len(entries)
		entries = append(entries, tailEntry(session, ttl, r.Messages, start, d.Tail))
	}
	if len(entries) == 0 {
		return d, nil
	}
	added, err := s.Add(ctx, entries...)
	if !known {
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
	if tail >= 0 && err == nil && !added[tail] {
		d.TailReported = true
	}
	return d, err
}

// tailEntry is the store entry marking a request's tail as reported in a session: a digest of
// where the tail starts, the model turn it answers and its content, so a retry of the request
// finds it and a later turn with the same words (another "continue") does not.
func tailEntry(session string, ttl time.Duration, msgs []llm.Message, start int, tail []llm.Message) store.Entry {
	d := newDigest()
	d.int(start)
	if start > 0 {
		d.message(&msgs[start-1])
	}
	for i := range tail {
		d.message(&tail[i])
	}
	return store.Entry{Key: "tail/" + session + "/" + d.hex(), Value: seenValue, TTL: ttl}
}

// digest hashes content stably across processes and replicas (SHA-256, truncated to 128 bits when
// rendered), each field length-prefixed so no two sequences of fields hash alike.
type digest struct {
	h   hash.Hash
	buf [sha256.Size]byte
}

func newDigest() *digest { return &digest{h: sha256.New()} }

func (d *digest) int(n int) {
	binary.BigEndian.PutUint64(d.buf[:8], uint64(n))
	d.h.Write(d.buf[:8])
}

// field writes s after its length, hashed in place.
func (d *digest) field(s string) {
	d.bytes(unsafe.Slice(unsafe.StringData(s), len(s))) // Write neither keeps nor modifies it
}

// bytes writes b after its length.
func (d *digest) bytes(b []byte) {
	d.int(len(b))
	d.h.Write(b)
}

// message writes a message's role and parts.
func (d *digest) message(m *llm.Message) {
	d.field(string(m.Role))
	for i := range m.Parts {
		d.part(&m.Parts[i])
	}
}

func (d *digest) part(p *llm.Part) {
	d.int(int(p.Kind))
	switch {
	case p.Call != nil:
		d.field(p.Call.ID)
		d.field(p.Call.Name)
		d.field(p.Call.Arguments)
	case p.Result != nil:
		d.field(p.Result.CallID)
		isError := 0
		if p.Result.IsError {
			isError = 1
		}
		d.int(isError)
		for i := range p.Result.Parts {
			d.part(&p.Result.Parts[i])
		}
	case p.Media != nil:
		d.field(p.Media.Type)
		d.field(p.Media.MIME)
		d.field(p.Media.URL)
		d.field(p.Media.Data)
	default:
		d.field(p.Text)
	}
}

// hex renders the digest's first 128 bits in hex.
func (d *digest) hex() string {
	var hexed [32]byte
	hex.Encode(hexed[:], d.h.Sum(d.buf[:0])[:16])
	return string(hexed[:])
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
