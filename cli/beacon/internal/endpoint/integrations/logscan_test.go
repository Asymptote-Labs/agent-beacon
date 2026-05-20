package integrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLastHarnessEventSkipsMalformedLinesAndUsesLatestTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	data := strings.Join([]string{
		`not json`,
		`{"timestamp":"2026-05-13T16:18:40Z","harness":{"name":"openclaw_gateway"}}`,
		`{"harness":{"name":"openclaw_gateway"}}`,
		`{"timestamp":"2026-05-13T16:19:40Z","harness":{"name":"cursor"}}`,
		`{"timestamp":"2026-05-13T16:20:40Z","harness":{"name":"OpenClaw_Gateway"}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	last, ok := LastHarnessEvent(path, "openclaw_gateway")
	if !ok {
		t.Fatal("expected openclaw event")
	}
	if got, want := last.UTC().Format(time.RFC3339), "2026-05-13T16:20:40Z"; got != want {
		t.Fatalf("LastHarnessEvent = %s, want %s", got, want)
	}
}

func TestHasHarnessEventSinceRequiresTimestampAfterSince(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	data := `{"timestamp":"2026-05-13T16:20:40Z","harness":{"name":"openclaw_gateway"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	if !HasHarnessEventSince(path, "openclaw_gateway", time.Date(2026, 5, 13, 16, 20, 0, 0, time.UTC)) {
		t.Fatal("expected event after since timestamp")
	}
	if HasHarnessEventSince(path, "openclaw_gateway", time.Date(2026, 5, 13, 16, 21, 0, 0, time.UTC)) {
		t.Fatal("did not expect event after later since timestamp")
	}
}

func TestHasHarnessEventSinceFalseWithoutParsedTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	data := `{"harness":{"name":"openclaw_gateway"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	if !HasRecentHarnessEvent(path, "openclaw_gateway") {
		t.Fatal("expected untimestamped event to count as observed")
	}
	if HasHarnessEventSince(path, "openclaw_gateway", time.Now()) {
		t.Fatal("untimestamped event should not satisfy since filter")
	}
}
