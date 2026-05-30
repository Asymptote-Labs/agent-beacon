package asymptotetrace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// ErrSinkPathRequired is returned when a JSONL sink has no output path.
var ErrSinkPathRequired = errors.New("sink path is required")

// JSONLSink writes canonical Beacon events to a local JSONL file.
type JSONLSink struct {
	path string
	mu   sync.Mutex
}

// NewJSONLSink creates a local, non-rotating JSONL sink for canonical
// Beacon events. It performs no network I/O.
func NewJSONLSink(path string) *JSONLSink {
	return &JSONLSink{path: path}
}

func (s *JSONLSink) WriteBatch(ctx context.Context, events []Event) (err error) {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s.path == "" {
		return ErrSinkPathRequired
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
	defer func() {
		if cerr := f.Close(); cerr != nil {
			if err != nil {
				err = errors.Join(err, cerr)
			} else {
				err = cerr
			}
		}
	}()

	encoder := json.NewEncoder(f)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
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
