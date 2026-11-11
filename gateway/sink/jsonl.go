// Package sink holds pipeline sinks.
package sink

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
)

// file is what JSONL writes to: an *os.File opened for appending.
type file interface {
	io.Writer
	io.ReaderAt
	Stat() (os.FileInfo, error)
	Truncate(size int64) error
	Close() error
}

// JSONL appends each record's value as one JSON line. A record that does not marshal is skipped
// and reported; the rest of the batch is still written.
//
// A batch is written whole or not at all. When writing it fails (a full disk, a quota, an I/O
// error), the file is cut back to the end of its last complete line, so it never holds half a
// line, and the next batch starts with a fresh buffer: bufio.Writer keeps its first error forever,
// so reusing it would fail every later batch even once the disk has room again.
type JSONL struct {
	f file
	w *bufio.Writer
	// end is the offset just past the last complete line.
	end int64
	// cut reports that the file may hold bytes past end, from a write whose cutting back failed.
	cut bool
}

// OpenJSONL opens path for appending, creating it with owner-only permissions: records carry
// prompts and tool output.
func OpenJSONL(path string) (*JSONL, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s, err := newJSONL(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return s, nil
}

// newJSONL writes to f after its last complete line: a line a crash left unfinished is cut off,
// so the first record written is not joined to it.
func newJSONL(f file) (*JSONL, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	end, err := lastLineEnd(f, info.Size())
	if err != nil {
		return nil, err
	}
	s := &JSONL{f: f, w: bufio.NewWriterSize(f, 64<<10), end: end}
	if end != info.Size() {
		if err := f.Truncate(end); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// lastLineEnd is the offset just past the last newline among f's first size bytes, read backwards
// in chunks; 0 when there is none.
func lastLineEnd(f io.ReaderAt, size int64) (int64, error) {
	var buf [32 << 10]byte
	for hi := size; hi > 0; {
		lo := max(hi-int64(len(buf)), 0)
		chunk := buf[:hi-lo]
		if _, err := f.ReadAt(chunk, lo); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if i := bytes.LastIndexByte(chunk, '\n'); i >= 0 {
			return lo + int64(i) + 1, nil
		}
		hi = lo
	}
	return 0, nil
}

// Write implements pipeline.Sink. A batch is flushed before Write returns, so a crash loses at
// most the batch in flight. A batch that cannot be written is cut back out of the file and
// reported as a pipeline.WriteError counting its records.
func (s *JSONL) Write(_ context.Context, batch []pipeline.Record) error {
	if s.cut {
		if err := s.f.Truncate(s.end); err != nil {
			return &pipeline.WriteError{Dropped: len(batch), Err: fmt.Errorf("cut back a failed write: %w", err)}
		}
		s.cut = false
	}
	var skipped error
	var n int64
	written := 0
	for _, r := range batch {
		line, err := json.Marshal(r.Value)
		if err != nil {
			skipped = fmt.Errorf("marshal %s record: %w", r.Kind, err)
			continue
		}
		s.w.Write(line)
		s.w.WriteByte('\n')
		n += int64(len(line)) + 1
		written++
	}
	if err := s.w.Flush(); err != nil {
		s.w.Reset(s.f)
		if terr := s.f.Truncate(s.end); terr != nil {
			s.cut = true
		}
		return &pipeline.WriteError{Dropped: len(batch), Err: err}
	}
	s.end += n
	if skipped != nil {
		return &pipeline.WriteError{Dropped: len(batch) - written, Err: skipped}
	}
	return nil
}

// Close implements pipeline.Sink.
func (s *JSONL) Close() error {
	if err := s.w.Flush(); err != nil {
		s.f.Close()
		return err
	}
	return s.f.Close()
}
