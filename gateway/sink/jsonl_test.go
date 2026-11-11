package sink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
)

// failing is an *os.File whose writes fail while fail is set: it writes the first half of what it
// is given, then reports the disk full.
type failing struct {
	*os.File
	fail bool
}

func (f *failing) Write(b []byte) (int, error) {
	if f.fail {
		n, _ := f.File.Write(b[:len(b)/2])
		return n, syscall.ENOSPC
	}
	return f.File.Write(b)
}

func records(values ...string) []pipeline.Record {
	out := make([]pipeline.Record, len(values))
	for i, v := range values {
		out[i] = pipeline.Record{Kind: "test", Value: v}
	}
	return out
}

func content(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A batch that fails to write (the disk filling up partway through) leaves no half line in the
// file, is reported dropped in full, and the next batch writes normally once the disk has room:
// the buffered writer's error does not stick.
func TestWriteRecoversFromFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ff := &failing{File: f}
	s, err := newJSONL(ff)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Write(ctx, records("a", "b")); err != nil {
		t.Fatal(err)
	}
	ff.fail = true
	err = s.Write(ctx, records("c", strings.Repeat("d", 1000)))
	var we *pipeline.WriteError
	if !errors.As(err, &we) || we.Dropped != 2 || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("failed write returned %v, want a WriteError dropping 2 records", err)
	}
	if got := content(t, path); got != "\"a\"\n\"b\"\n" {
		t.Fatalf("after the failed batch the file holds %q", got)
	}
	ff.fail = false
	if err := s.Write(ctx, records("e")); err != nil {
		t.Fatalf("write after recovery: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := content(t, path); got != "\"a\"\n\"b\"\n\"e\"\n" {
		t.Errorf("file holds %q", got)
	}
}

// The file rotated by copytruncate between batches, and another writer's line appended to it,
// then a failed batch: the batch is cut back to the file's size when it started, read from the
// file, so the file is neither padded with NUL bytes to its old size nor stripped of the other
// line, and the next batch follows it.
func TestFailureAfterExternalTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ff := &failing{File: f}
	s, err := newJSONL(ff)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Write(ctx, records(strings.Repeat("a", 5000), "b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 0); err != nil { // logrotate copytruncate
		t.Fatal(err)
	}
	other, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	other.WriteString("{\"other\":1}\n")
	other.Close()
	ff.fail = true
	if err := s.Write(ctx, records("c", strings.Repeat("d", 1000))); err == nil {
		t.Fatal("the failing write succeeded")
	}
	if got := content(t, path); got != "{\"other\":1}\n" {
		t.Fatalf("after the failed batch the file holds %q", got)
	}
	ff.fail = false
	if err := s.Write(ctx, records("e")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := content(t, path); got != "{\"other\":1}\n\"e\"\n" {
		t.Errorf("file holds %q", got)
	}
}

// A record that does not marshal is skipped and counted; the rest of its batch is written.
func TestWriteSkipsUnmarshalable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	s, err := OpenJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(context.Background(), []pipeline.Record{{Kind: "test", Value: "a"}, {Kind: "test", Value: func() {}}})
	var we *pipeline.WriteError
	if !errors.As(err, &we) || we.Dropped != 1 {
		t.Errorf("returned %v, want a WriteError dropping 1 record", err)
	}
	s.Close()
	if got := content(t, path); got != "\"a\"\n" {
		t.Errorf("file holds %q", got)
	}
}

// A line a crash left unfinished is cut off when the file is opened again, so the next record is
// not joined to it.
func TestOpenCutsUnfinishedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	if err := os.WriteFile(path, []byte("\"a\"\n{\"half\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(context.Background(), records("b")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := content(t, path); got != "\"a\"\n\"b\"\n" {
		t.Errorf("file holds %q", got)
	}
	// A file with no complete line at all is emptied.
	if err := os.WriteFile(path, []byte("{\"half\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = OpenJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := content(t, path); got != "" {
		t.Errorf("file holds %q", got)
	}
	// The last complete line can sit further back than one read.
	long := "\"" + strings.Repeat("x", 70000) + "\"\n"
	if err := os.WriteFile(path, []byte(long+strings.Repeat("y", 50000)), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = OpenJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := content(t, path); got != long {
		t.Errorf("file holds %d bytes, want the %d of its complete line", len(got), len(long))
	}
}
