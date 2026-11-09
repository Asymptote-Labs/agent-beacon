package codec

import (
	"bytes"
)

// isSSE tells a complete event stream from a JSON body by its first bytes. Streaming and
// non-streaming replies share a status and often a content type once a gateway has buffered them,
// so the body is the reliable signal.
func isSSE(body []byte) bool {
	b := bytes.TrimLeft(body, " \t\r\n")
	return bytes.HasPrefix(b, []byte("event:")) ||
		bytes.HasPrefix(b, []byte("data:")) ||
		bytes.HasPrefix(b, []byte(":"))
}

// sseScanner splits a complete stream into events: an event's name (empty when the stream names
// none, as OpenAI Chat's does) and its data lines joined with \n. It accepts CRLF, LF and CR line
// endings, ignores comments and fields other than event and data, and dispatches a final event
// that lacks its terminating blank line, since a buffered body may have been cut there.
//
// It allocates nothing per event: name and data alias the body, except the data of an event with
// several data lines, which is joined in a buffer the scanner reuses. Both are valid until the
// next call to next.
type sseScanner struct {
	body []byte
	pos  int
	// lf and cr are the offsets of the next '\n' and '\r' at or after pos (len(body) if none),
	// found once each, so a stream of CR-only line endings is not rescanned for '\n' per line.
	lf, cr int
	name   []byte
	data   []byte
	buf    []byte
}

func newSSEScanner(body []byte) *sseScanner {
	return &sseScanner{body: body, lf: -1, cr: -1}
}

// line returns the next line and whether there was one.
func (s *sseScanner) line() ([]byte, bool) {
	if s.pos >= len(s.body) {
		return nil, false
	}
	if s.lf < s.pos {
		s.lf = s.nextByte('\n')
	}
	if s.cr < s.pos {
		s.cr = s.nextByte('\r')
	}
	end := min(s.lf, s.cr)
	line := s.body[s.pos:end]
	switch {
	case end == len(s.body):
		s.pos = end
	case s.body[end] == '\r' && end+1 < len(s.body) && s.body[end+1] == '\n':
		s.pos = end + 2
	default:
		s.pos = end + 1
	}
	return line, true
}

func (s *sseScanner) nextByte(c byte) int {
	if i := bytes.IndexByte(s.body[s.pos:], c); i >= 0 {
		return s.pos + i
	}
	return len(s.body)
}

// next advances to the next event, reporting false when the stream has no more.
func (s *sseScanner) next() bool {
	s.name, s.data = nil, nil
	hasData, joined, pending := false, false, false
	for {
		line, ok := s.line()
		if !ok {
			return pending
		}
		if len(line) == 0 {
			if pending {
				return true
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value := sseField(line)
		switch field {
		case sseEventField:
			s.name, pending = value, true
		case sseDataField:
			switch {
			case !hasData:
				s.data = value
			case joined:
				s.buf = append(append(s.buf, '\n'), value...)
				s.data = s.buf
			default:
				s.buf = append(append(append(s.buf[:0], s.data...), '\n'), value...)
				s.data, joined = s.buf, true
			}
			hasData, pending = true, true
		}
	}
}

type sseFieldKind uint8

const (
	sseOtherField sseFieldKind = iota
	sseEventField
	sseDataField
)

// sseField splits a line into its field and value, the value without the one space that may
// follow the colon; a line without a colon is a field with an empty value.
func sseField(line []byte) (sseFieldKind, []byte) {
	var kind sseFieldKind
	var rest []byte
	switch {
	case bytes.HasPrefix(line, []byte("data")):
		kind, rest = sseDataField, line[len("data"):]
	case bytes.HasPrefix(line, []byte("event")):
		kind, rest = sseEventField, line[len("event"):]
	default:
		return sseOtherField, nil
	}
	if len(rest) == 0 {
		return kind, rest
	}
	if rest[0] != ':' {
		return sseOtherField, nil
	}
	if len(rest) > 1 && rest[1] == ' ' {
		return kind, rest[2:]
	}
	return kind, rest[1:]
}
