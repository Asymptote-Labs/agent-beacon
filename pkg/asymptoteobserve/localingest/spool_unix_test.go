//go:build unix

package localingest

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A workspace spool sits where a checked-out repository can write, so its pending.jsonl is not
// trusted to be the spool. A planted link must not let an append write, or a drain read and
// rewrite, the file it points at; a planted FIFO must not hang the hook that opens it.
func TestSpoolRefusesAPlantedLinkOrFIFOAtItsFile(t *testing.T) {
	t.Run("link", func(t *testing.T) {
		s := Spool{Dir: t.TempDir()}
		target := filepath.Join(t.TempDir(), "elsewhere.jsonl")
		if err := os.WriteFile(target, []byte("{\"keep\":true}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, s.FilePath()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append([][]byte{[]byte(`{"a":1}`)}); err == nil {
			t.Fatal("Append wrote through a planted link")
		}
		sent := 0
		if _, err := s.Drain(10, func(lines [][]byte) (int, error) { sent += len(lines); return len(lines), nil }); err == nil || sent != 0 {
			t.Fatalf("Drain read through a planted link: sent %d, err %v", sent, err)
		}
		if data, _ := os.ReadFile(target); string(data) != "{\"keep\":true}\n" {
			t.Fatalf("the link's target changed: %q", data)
		}
	})
	t.Run("FIFO", func(t *testing.T) {
		s := Spool{Dir: t.TempDir()}
		if err := syscall.Mkfifo(s.FilePath(), 0o600); err != nil {
			t.Skipf("no FIFOs here: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := s.Append([][]byte{[]byte(`{"a":1}`)})
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("Append wrote to a FIFO")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Append hung on a planted FIFO")
		}
	})
}
