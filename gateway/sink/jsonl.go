// Package sink holds pipeline sinks.
package sink

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
)

// JSONL appends each record's value as one JSON line. A record that does not marshal is skipped
// and reported; the rest of the batch is still written.
type JSONL struct {
	f *os.File
	w *bufio.Writer
}

// OpenJSONL opens path for appending, creating it with owner-only permissions: records carry
// prompts and tool output.
func OpenJSONL(path string) (*JSONL, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &JSONL{f: f, w: bufio.NewWriterSize(f, 64<<10)}, nil
}

// Write implements pipeline.Sink. A batch is flushed before Write returns, so a crash loses at
// most the batch in flight.
func (s *JSONL) Write(_ context.Context, batch []pipeline.Record) error {
	var skipped error
	for _, r := range batch {
		line, err := json.Marshal(r.Value)
		if err != nil {
			skipped = fmt.Errorf("marshal %s record: %w", r.Kind, err)
			continue
		}
		s.w.Write(line)
		s.w.WriteByte('\n')
	}
	if err := s.w.Flush(); err != nil {
		return err
	}
	return skipped
}

// Close implements pipeline.Sink.
func (s *JSONL) Close() error {
	if err := s.w.Flush(); err != nil {
		s.f.Close()
		return err
	}
	return s.f.Close()
}
