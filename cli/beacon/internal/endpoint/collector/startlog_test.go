package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appendLog(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

// The #447 line, as the collector wrote it, must reach the install error -- and a failure left in
// the same appended-to file by an earlier run must not be quoted in its place.
func TestLogErrorSinceQuotesOnlyWhatThisStartWrote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.err")
	appendLog(t, path, "Error: stale failure from an earlier run\n")
	offset := LogSize(path)
	bind := `Failed to start extension	health_check	failed to bind to address 127.0.0.1:13133: listen tcp 127.0.0.1:13133: bind: address already in use`
	appendLog(t, path, "2026-09-04T17:00:00Z info service starting\n"+bind+"\n2026-09-04T17:00:01Z info shutdown complete\n")

	got := LogErrorSince(path, offset)

	if got != strings.TrimSpace(bind) {
		t.Fatalf("LogErrorSince = %q, want the bind failure", got)
	}
}

func TestLogErrorSinceFallsBackToTheLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.out")
	appendLog(t, path, "first\n\nsomething odd happened\n\n")
	if got := LogErrorSince(path, 0); got != "something odd happened" {
		t.Fatalf("LogErrorSince = %q, want the last non-empty line", got)
	}
}

func TestLogErrorSinceIsEmptyWhenNothingNewWasWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.err")
	appendLog(t, path, "Error: old\n")
	if got := LogErrorSince(path, LogSize(path)); got != "" {
		t.Fatalf("LogErrorSince = %q, want empty", got)
	}
	if got := LogErrorSince(filepath.Join(t.TempDir(), "missing"), 0); got != "" {
		t.Fatalf("LogErrorSince(missing) = %q, want empty", got)
	}
	if got := LogErrorSince("", 0); got != "" {
		t.Fatalf("LogErrorSince(\"\") = %q, want empty", got)
	}
}

// A log that shrank since the offset was taken was rotated or truncated, so all of it is new.
func TestLogErrorSinceReadsAFileThatShrankFromTheStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.err")
	appendLog(t, path, "Error: bind failed\n")
	if got := LogErrorSince(path, 1<<20); got != "Error: bind failed" {
		t.Fatalf("LogErrorSince = %q, want the line in the truncated file", got)
	}
}

func TestLogErrorSinceBoundsALongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.err")
	appendLog(t, path, "Error: "+strings.Repeat("x", 5000)+"\n")
	got := LogErrorSince(path, 0)
	if len([]rune(got)) > startLogLineLimit+3 || !strings.HasPrefix(got, "Error: ") || !strings.HasSuffix(got, "...") {
		t.Fatalf("LogErrorSince returned %d runes, want at most %d with an ellipsis", len([]rune(got)), startLogLineLimit+3)
	}
}
