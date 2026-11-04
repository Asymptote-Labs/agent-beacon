package localingest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func spoolLines(prefix string, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = []byte(fmt.Sprintf(`{"n":"%s-%d"}`, prefix, i))
	}
	return out
}

func readSpool(t *testing.T, s Spool) []string {
	t.Helper()
	data, err := os.ReadFile(s.filePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range splitLines(data) {
		out = append(out, string(line))
	}
	return out
}

func acceptAll(sent *[][]byte) func([][]byte) (int, error) {
	return func(lines [][]byte) (int, error) {
		for _, l := range lines {
			*sent = append(*sent, append([]byte(nil), l...))
		}
		return len(lines), nil
	}
}

func TestSpoolAppendThenDrainEverything(t *testing.T) {
	s := Spool{Dir: filepath.Join(t.TempDir(), "spool")}
	if dropped, err := s.Append(spoolLines("a", 3)); err != nil || dropped != 0 {
		t.Fatalf("Append = %d, %v", dropped, err)
	}
	if n, size, err := s.Pending(); err != nil || n != 3 || size == 0 {
		t.Fatalf("Pending = %d, %d, %v", n, size, err)
	}
	var sent [][]byte
	n, err := s.Drain(100, acceptAll(&sent))
	if err != nil || n != 3 {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	for i, line := range sent {
		if string(line) != fmt.Sprintf(`{"n":"a-%d"}`, i) {
			t.Fatalf("sent[%d] = %s, out of order", i, line)
		}
	}
	if _, err := os.Stat(s.filePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spool file left behind after a full drain: %v", err)
	}
	if n, _, _ := s.Pending(); n != 0 {
		t.Fatalf("Pending after drain = %d", n)
	}
}

func TestSpoolDrainKeepsWhatWasNotProcessed(t *testing.T) {
	s := Spool{Dir: t.TempDir()}
	_, _ = s.Append(spoolLines("a", 5))
	n, err := s.Drain(100, func(lines [][]byte) (int, error) {
		return 2, errors.New("collector stopped")
	})
	if n != 2 || err == nil {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	if got := readSpool(t, s); strings.Join(got, ",") != `{"n":"a-2"},{"n":"a-3"},{"n":"a-4"}` {
		t.Fatalf("spool = %v", got)
	}
}

func TestSpoolDrainThatSendsNothingRemovesNothing(t *testing.T) {
	s := Spool{Dir: t.TempDir()}
	_, _ = s.Append(spoolLines("a", 2))
	n, err := s.Drain(100, func([][]byte) (int, error) { return 0, errors.New("down") })
	if n != 0 || err == nil {
		t.Fatalf("Drain = %d, %v", n, err)
	}
	if got := readSpool(t, s); len(got) != 2 {
		t.Fatalf("spool = %v", got)
	}
}

func TestSpoolDrainClampsANonsenseCount(t *testing.T) {
	s := Spool{Dir: t.TempDir()}
	_, _ = s.Append(spoolLines("a", 2))
	n, _ := s.Drain(100, func([][]byte) (int, error) { return 99, nil })
	if n != 2 {
		t.Fatalf("Drain = %d, want it clamped to the batch", n)
	}
	n, _ = s.Drain(100, func([][]byte) (int, error) { return -1, nil })
	if n != 0 {
		t.Fatalf("Drain = %d", n)
	}
}

func TestSpoolDrainHonorsMaxLines(t *testing.T) {
	s := Spool{Dir: t.TempDir()}
	_, _ = s.Append(spoolLines("a", 10))
	var sent [][]byte
	n, err := s.Drain(4, acceptAll(&sent))
	if err != nil || n != 4 || len(sent) != 4 {
		t.Fatalf("Drain = %d, %v, sent %d", n, err, len(sent))
	}
	if got := readSpool(t, s); len(got) != 6 || got[0] != `{"n":"a-4"}` {
		t.Fatalf("spool = %v", got)
	}
	if n, _ := s.Drain(0, acceptAll(&sent)); n != 0 {
		t.Fatal("Drain(0) sent something")
	}
}

func TestSpoolDrainOfNothing(t *testing.T) {
	s := Spool{Dir: filepath.Join(t.TempDir(), "never-created")}
	called := false
	n, err := s.Drain(10, func([][]byte) (int, error) { called = true; return 0, nil })
	if n != 0 || err != nil || called {
		t.Fatalf("Drain = %d, %v, called %v", n, err, called)
	}
	if _, err := os.Stat(s.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Drain created the spool directory")
	}
}

func TestSpoolDropsPastItsCap(t *testing.T) {
	s := Spool{Dir: t.TempDir(), MaxBytes: 30}
	// Each line is 12 bytes plus a newline: two fit in 30, the third does not.
	dropped, err := s.Append(spoolLines("x", 3))
	if err != nil || dropped != 1 {
		t.Fatalf("Append = %d, %v", dropped, err)
	}
	dropped, err = s.Append(spoolLines("y", 1))
	if err != nil || dropped != 1 {
		t.Fatalf("Append into a full spool = %d, %v", dropped, err)
	}
	if got := readSpool(t, s); len(got) != 2 {
		t.Fatalf("spool = %v", got)
	}
}

func TestSpoolDropsLinesThatCouldNeverBeSent(t *testing.T) {
	s := Spool{Dir: t.TempDir()}
	dropped, err := s.Append([][]byte{
		[]byte(""),
		[]byte("{\"a\":1}\n{\"b\":2}"),
		[]byte(strings.Repeat("x", MaxLineBytes+1)),
		[]byte("{\"ok\":1}\n"),
	})
	if err != nil || dropped != 3 {
		t.Fatalf("Append = %d, %v", dropped, err)
	}
	if got := readSpool(t, s); len(got) != 1 || got[0] != `{"ok":1}` {
		t.Fatalf("spool = %v", got)
	}
}

func TestSpoolIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not decide access on Windows")
	}
	parent := t.TempDir()
	s := Spool{Dir: filepath.Join(parent, "spool")}
	if err := os.MkdirAll(s.Dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(spoolLines("a", 2)); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(s.Dir); info.Mode().Perm() != SpoolDirMode {
		t.Fatalf("dir mode = %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(s.filePath()); info.Mode().Perm() != SpoolFileMode {
		t.Fatalf("file mode = %v", info.Mode().Perm())
	}
	_, _ = s.Drain(1, func([][]byte) (int, error) { return 1, nil })
	if info, _ := os.Stat(s.filePath()); info.Mode().Perm() != SpoolFileMode {
		t.Fatalf("rewritten file mode = %v", info.Mode().Perm())
	}
}

func TestSpoolAppendIsNotHeldUpByASlowDrain(t *testing.T) {
	s := Spool{Dir: t.TempDir()}
	_, _ = s.Append(spoolLines("old", 3))
	inSend := make(chan struct{})
	release := make(chan struct{})
	result := make(chan int, 1)
	go func() {
		n, _ := s.Drain(2, func(lines [][]byte) (int, error) {
			close(inSend)
			<-release
			return len(lines), nil
		})
		result <- n
	}()
	<-inSend

	start := time.Now()
	if _, err := s.Append(spoolLines("new", 1)); err != nil {
		t.Fatalf("Append during a drain: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Append waited %v on a drain that was sending", elapsed)
	}
	// A second drain finds one under way and steps aside.
	if n, err := s.Drain(10, func([][]byte) (int, error) { t.Error("a second drain sent"); return 0, nil }); n != 0 || err != nil {
		t.Fatalf("concurrent Drain = %d, %v", n, err)
	}
	close(release)
	if n := <-result; n != 2 {
		t.Fatalf("Drain = %d", n)
	}
	got := strings.Join(readSpool(t, s), ",")
	if got != `{"n":"old-2"},{"n":"new-0"}` {
		t.Fatalf("spool = %s; the event appended mid-drain must survive and the sent ones must go", got)
	}
}

func TestSpoolWithoutADirectory(t *testing.T) {
	if _, err := (Spool{}).Append(spoolLines("a", 1)); err == nil {
		t.Fatal("Append with no directory succeeded")
	}
}
