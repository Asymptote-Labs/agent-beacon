package asymptoteobserve

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ToolCallIDKeys are the names a runtime uses for the identifier it assigns to
// one tool invocation, in the order Beacon prefers them.
//
// Every supported runtime already emits this value -- Claude Code as
// tool_use_id, Codex and OpenCode as call_id, Cline as callId -- and until this
// list existed Beacon dropped all of them into raw, because the promotion list
// held only the canonical OTel name that no runtime actually writes. It is the
// one field that links a tool call to its result, an approval to the execution
// it approved, and a hook event to the OTLP event for the same action, so it is
// promoted to gen_ai.tool.call.id on every capture path from this single list.
//
// Order matters: the canonical names come first so an explicitly mapped value
// always beats a runtime-native one, and the runtime-native names follow in no
// significant order because no payload carries two of them.
var ToolCallIDKeys = []string{
	"gen_ai.tool.call.id",
	"beacon.gen_ai.tool.call.id",
	"tool_use_id",
	"toolUseId",
	"tool_call_id",
	"toolCallId",
	"call_id",
	"callId",
	"callID",
}

// eventIDNamespace is the fixed UUID namespace every Beacon event ID is derived
// under, so the same event derives the same UUID in any process and any
// release. It is an arbitrary constant, not a secret, and must never change:
// changing it renames every event ID Beacon has ever written.
var eventIDNamespace = [16]byte{
	0x1f, 0x4b, 0x2c, 0x8e, 0x6d, 0x3a, 0x5a, 0x7c,
	0x9b, 0x61, 0x3f, 0x0d, 0x5e, 0x8c, 0x2a, 0x14,
}

// EventIDForLine derives the deterministic identity of one serialized endpoint
// event whose event.id has not been set yet.
//
// Deterministic means the same event derives the same ID every time, in either
// capture path and in any later run. That is what makes an event addressable:
// re-reading a log, merging two reports of one action, or uploading a trace
// twice can all name the same event rather than guessing at it by position.
//
// Two identities are possible and the event decides which it gets:
//
//   - When the runtime named the action itself -- a tool call ID plus a
//     resolvable target -- the ID is derived from session, action, target and
//     call ID alone. Timestamps and content are deliberately excluded, so the
//     hook report and the OTLP report of one real action derive one ID even
//     though they are written seconds apart with different fields.
//   - Otherwise the ID is a digest of the event itself, which is unique to the
//     extent the event is. Two events that agree on every recorded byte,
//     including the timestamp, are indistinguishable in the log and get one ID;
//     a per-session sequence number would separate them, and when events carry
//     one this derivation picks it up for free.
//
// The writer does not suppress duplicates on this field. Equality under the
// content digest means "nothing recorded tells these apart", which is a weaker
// claim than "this is the same action" and not one to drop an event over. The
// call ID carries that claim, and duplicate suppression keys on it directly.
//
// line must be the event's JSON with event.id absent; passing a line that
// already carries one derives an ID for a different event than the caller
// meant, so callers marshal with the field empty and fill it afterwards.
func EventIDForLine(line []byte) string {
	var event map[string]interface{}
	if err := json.Unmarshal(line, &event); err != nil {
		// An event Beacon cannot parse is still an event Beacon is about to
		// write, so it gets a content identity rather than no identity.
		return eventUUID("content", string(line))
	}
	action := nestedString(event, "event", "action")
	callID := nestedString(event, "gen_ai", "tool", "call", "id")
	target := dedupeTarget(action, event)
	if callID != "" && target != "" {
		return eventUUID("call", strings.Join([]string{
			nestedString(event, "session", "id"),
			strings.ToLower(strings.TrimSpace(action)),
			target,
			callID,
		}, "\x00"))
	}
	return eventUUID("content", string(line))
}

// eventUUID renders a name under eventIDNamespace as an RFC 4122 version 5
// UUID. The kind prefix keeps the two identity modes in separate value spaces,
// so a content digest can never collide with a call identity.
//
// SHA-1 is what version 5 specifies. It is used here as a namespaced identifier
// function over data Beacon just wrote, never as a security boundary.
//
// The parts are streamed into the hash rather than concatenated into a sized
// buffer first. name is a whole serialized event, so sizing an allocation from
// its length is a computation on a value the event's own content decides -- and
// there is no reason to hold a second copy of the event in memory to hash it.
func eventUUID(kind, name string) string {
	digest := sha1.New()
	// hash.Hash.Write is documented never to return an error.
	digest.Write(eventIDNamespace[:])
	io.WriteString(digest, kind)
	io.WriteString(digest, name)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50 // version 5
	id[8] = (id[8] & 0x3f) | 0x80 // RFC 4122 variant
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}
