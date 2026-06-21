package writer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func appendTestEvent(t *testing.T, path, action string) {
	t.Helper()
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:  action,
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "m",
	})
	if _, err := AppendEvent(ev, Options{Path: path}); err != nil {
		t.Fatalf("AppendEvent(%q): %v", action, err)
	}
}

func TestReadEventsRoundTripInAppendOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	for _, action := range []string{"a.one", "a.two", "a.three"} {
		appendTestEvent(t, path, action)
	}
	events, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].Event.Action != "a.one" || events[2].Event.Action != "a.three" {
		t.Fatalf("unexpected order/actions: %+v", events)
	}
}

func TestReadEventsMissingFileReturnsNil(t *testing.T) {
	events, err := ReadEvents(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if events != nil {
		t.Fatalf("expected nil events, got %v", events)
	}
}

func TestScanEventsSkipsBlankAndMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendTestEvent(t, path, "a.one")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("\n   \nnot json\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var raws [][]byte
	if err := ScanEvents(path, func(raw []byte, _ schema.Event) error {
		raws = append(raws, raw)
		return nil
	}); err != nil {
		t.Fatalf("ScanEvents: %v", err)
	}
	if len(raws) != 1 {
		t.Fatalf("expected 1 well-formed event, got %d", len(raws))
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raws[0], &decoded); err != nil {
		t.Fatalf("raw line is not valid JSON: %v", err)
	}
}

func TestScanEventsPropagatesCallbackError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	appendTestEvent(t, path, "a.one")

	sentinel := errors.New("stop")
	err := ScanEvents(path, func([]byte, schema.Event) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}
