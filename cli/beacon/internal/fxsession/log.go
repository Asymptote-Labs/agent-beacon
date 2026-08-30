package fxsession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxFrameBytes is the largest events.jsonl line this decoder will hold in memory.
//
// It is fx's own `event_frame_max_bytes` (8 MiB, src/core/session/session_event.zig): fx refuses to
// write a frame larger than this, so a longer line is not a big event but a corrupt one -- two
// frames with a lost newline between them, or a partially overwritten file. Matching fx's limit
// rather than picking a Beacon-specific number keeps the decoder from rejecting a record fx
// considers valid, which is the failure that would silently drop a real turn.
const MaxFrameBytes = 8 * 1024 * 1024

// Stats records what a decoding pass saw. It is returned alongside the events rather than logged,
// because "Beacon collected nothing from this session" and "Beacon could not read this session"
// look identical from the outside and the collector reports the difference.
type Stats struct {
	Lines     int // complete lines read
	Decoded   int // envelopes decoded, including kinds with no mapped payload
	Skipped   int // envelopes skipped: unknown kind or unsupported schema version
	Malformed int // lines that were not a decodable envelope, or exceeded MaxFrameBytes
	// PartialTail is set when the file ended without a newline, which means fx was mid-append.
	// The incomplete bytes are not decoded and not counted as malformed: they are a frame that
	// has not been written yet, and the next sweep will read it whole.
	PartialTail bool
	// FirstError is the first malformed line's error, kept so a caller can say what went wrong
	// without accumulating one error per line of a corrupt file.
	FirstError error
}

// Decoder reads fx event envelopes from an events.jsonl stream.
//
// Streaming rather than slurping: a long-running fx session's log grows without bound, and the
// collector re-reads it on every sweep. Decoding one line at a time keeps that cost proportional
// to the line rather than to the file.
//
// Malformed lines do not stop the pass. An append-only log written by another process can be torn
// by a crash mid-line, and one unreadable line is not a reason to stop collecting the hundreds of
// good ones after it -- but it is a reason to say so, which is what Stats.Malformed is for.
type Decoder struct {
	reader *bufio.Reader
	limit  int64 // bytes to read, 0 for "until EOF"
	read   int64
	stats  Stats
	done   bool
}

// NewDecoder reads from r. When limit is positive the decoder stops after that many bytes, which is
// how a caller restricts a read to the committed prefix fx's manifest reports; see Store.Read.
func NewDecoder(r io.Reader, limit int64) *Decoder {
	return &Decoder{reader: bufio.NewReaderSize(r, 64*1024), limit: limit}
}

// Stats reports what the decoder has seen so far.
func (d *Decoder) Stats() Stats { return d.stats }

// Next returns the next decoded event, or io.EOF when the stream is exhausted.
//
// A line that is skipped (unknown kind, unsupported schema) or malformed is counted and stepped
// over rather than returned, so a caller's loop only ever sees events it can act on.
func (d *Decoder) Next() (*Event, error) {
	for {
		if d.done {
			return nil, io.EOF
		}
		line, err := d.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				d.done = true
				return nil, io.EOF
			}
			return nil, err
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		d.stats.Lines++
		event, err := DecodeEnvelope(line)
		switch {
		case errors.Is(err, ErrUnsupportedSchema):
			d.stats.Skipped++
			continue
		case err != nil:
			d.stats.Malformed++
			if d.stats.FirstError == nil {
				d.stats.FirstError = err
			}
			continue
		}
		d.stats.Decoded++
		if !mapsToPayload(event.Kind) {
			d.stats.Skipped++
			continue
		}
		return event, nil
	}
}

// readLine returns one complete line without its newline, or io.EOF.
//
// Three endings are distinguished on purpose. A complete line is returned. A trailing fragment with
// no newline is a frame fx is still writing, so it is reported through Stats.PartialTail and the
// stream ends. A line longer than MaxFrameBytes is consumed and discarded as malformed rather than
// buffered, so a corrupt file cannot make the collector allocate without bound.
func (d *Decoder) readLine() ([]byte, error) {
	for {
		var buf []byte
		oversized := false
		for {
			if d.limit > 0 && d.read >= d.limit {
				// The committed prefix ends on a frame boundary, so anything buffered here is a
				// frame fx wrote past the watermark and has not committed yet.
				d.notePartial(buf, oversized)
				return nil, io.EOF
			}
			chunk, err := d.reader.ReadSlice('\n')
			if d.limit > 0 && int64(len(chunk)) > d.limit-d.read {
				chunk = chunk[:d.limit-d.read]
				err = io.EOF
			}
			d.read += int64(len(chunk))
			if oversized || len(buf)+len(chunk) > MaxFrameBytes {
				// Discard rather than buffer: a line this long is corruption, and holding it
				// would let a damaged file dictate the collector's memory use.
				oversized = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) {
				d.notePartial(buf, oversized)
				return nil, io.EOF
			}
			if err != nil {
				return nil, err
			}
			break
		}
		if oversized {
			d.noteMalformed(fmt.Errorf("fx event frame exceeds %d bytes", MaxFrameBytes))
			continue
		}
		return bytes.TrimSuffix(bytes.TrimSuffix(buf, []byte("\n")), []byte("\r")), nil
	}
}

// notePartial records an unterminated trailing frame. An oversized fragment is corruption rather
// than a frame in flight, so it is counted as malformed instead.
func (d *Decoder) notePartial(buf []byte, oversized bool) {
	switch {
	case oversized:
		d.noteMalformed(fmt.Errorf("fx event frame exceeds %d bytes", MaxFrameBytes))
	case len(buf) > 0:
		d.stats.PartialTail = true
	}
}

func (d *Decoder) noteMalformed(err error) {
	d.stats.Malformed++
	if d.stats.FirstError == nil {
		d.stats.FirstError = err
	}
}

// mapsToPayload reports whether Beacon decodes a payload for this kind.
//
// The kinds that return false are fx's internal storage machinery: recovery checkpoints and the
// chunked state replacement it writes when it compacts a log. They describe how fx keeps its own
// records, not anything the agent did, and turning them into endpoint events would fill the log
// with rows no reader could act on.
func mapsToPayload(kind string) bool {
	switch kind {
	case KindSessionStarted, KindPreferencesChanged, KindWorkspaceRebound,
		KindHistoryTurnCommitted, KindUsageCheckpointed:
		return true
	default:
		return false
	}
}

// DecodeEnvelope decodes one events.jsonl line, including the payload of the kinds Beacon maps.
//
// Returns ErrUnsupportedSchema for a version this decoder does not understand, so the caller can
// skip that line rather than treat the file as corrupt. fx makes the same distinction: it rejects
// an unknown schema version outright instead of parsing it as the version it knows.
func DecodeEnvelope(line []byte) (*Event, error) {
	var envelope Envelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return nil, fmt.Errorf("fx event envelope: %w", err)
	}
	if envelope.SchemaVersion != SupportedSchemaVersion {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedSchema, envelope.SchemaVersion)
	}
	if envelope.Kind == "" {
		return nil, errors.New("fx event envelope: missing kind")
	}
	event := &Event{Envelope: envelope}
	if err := decodePayload(event); err != nil {
		return nil, err
	}
	return event, nil
}

func decodePayload(event *Event) error {
	if len(event.Payload) == 0 {
		if mapsToPayload(event.Kind) {
			return fmt.Errorf("fx event %q: missing payload", event.Kind)
		}
		return nil
	}
	switch event.Kind {
	case KindSessionStarted:
		return unmarshalPayload(event.Payload, event.Kind, &event.SessionStarted)
	case KindPreferencesChanged:
		return unmarshalPayload(event.Payload, event.Kind, &event.PreferencesChanged)
	case KindWorkspaceRebound:
		return unmarshalPayload(event.Payload, event.Kind, &event.WorkspaceRebound)
	case KindHistoryTurnCommitted:
		return unmarshalPayload(event.Payload, event.Kind, &event.TurnCommitted)
	case KindUsageCheckpointed:
		var wrapper struct {
			Usage *UsageSnapshot `json:"usage"`
		}
		if err := json.Unmarshal(event.Payload, &wrapper); err != nil {
			return fmt.Errorf("fx event %q: %w", event.Kind, err)
		}
		event.UsageCheckpointed = wrapper.Usage
		return nil
	default:
		return nil
	}
}

func unmarshalPayload[T any](payload json.RawMessage, kind string, out **T) error {
	value := new(T)
	if err := json.Unmarshal(payload, value); err != nil {
		return fmt.Errorf("fx event %q: %w", kind, err)
	}
	*out = value
	return nil
}
