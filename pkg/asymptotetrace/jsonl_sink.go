package asymptotetrace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type JSONLSink struct {
	path string
	mu   sync.Mutex
}

// NewJSONLSink creates a local, non-rotating NDJSON sink.
// It intentionally performs no network I/O.
func NewJSONLSink(path string) *JSONLSink {
	return &JSONLSink{path: path}
}

func (s *JSONLSink) WriteBatch(ctx context.Context, envelopes []Envelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	for _, envelope := range envelopes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := encoder.Encode(envelope); err != nil {
			return err
		}
	}
	return nil
}

func (s *JSONLSink) Flush(context.Context) error {
	return nil
}

func (s *JSONLSink) Close() error {
	return nil
}
