package telemetry

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLogCursorRoundTripsAndIsOffWithTelemetry(t *testing.T) {
	_, home := setup(t, nil)
	Init(options(nil, io.Discard))
	if _, ok := LoadLogCursor(); ok {
		t.Fatal("a fresh home has a cursor")
	}
	want := LogCursor{Log: "l", Head: "h", Offset: 42}
	if !SaveLogCursor(want) {
		t.Fatal("cursor not saved")
	}
	Close()

	// The next run reads it back.
	Init(options(nil, io.Discard))
	if got, ok := LoadLogCursor(); !ok || got != want {
		t.Fatalf("cursor = %+v, %t; want %+v", got, ok, want)
	}
	Close()

	// With telemetry off nothing is read or written.
	off := map[string]string{EnvTelemetry: "0"}
	Init(options(off, io.Discard))
	if _, ok := LoadLogCursor(); ok {
		t.Fatal("a cursor was read with telemetry off")
	}
	if SaveLogCursor(LogCursor{Log: "other"}) {
		t.Fatal("a cursor was saved with telemetry off")
	}
	Close()
	if err := os.Remove(filepath.Join(home, LogCursorPath)); err != nil {
		t.Fatal(err)
	}
	Init(options(off, io.Discard))
	SaveLogCursor(want)
	Close()
	if _, err := os.Stat(filepath.Join(home, LogCursorPath)); !os.IsNotExist(err) {
		t.Fatalf("telemetry off wrote the cursor: %v", err)
	}
}
