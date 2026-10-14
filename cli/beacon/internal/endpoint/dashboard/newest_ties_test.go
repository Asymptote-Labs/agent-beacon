package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestReadEventsNewestTiesUseNumericLineBeforeLimit(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	lines := make([][]byte, 11)
	for i := range lines {
		lines[i] = testEvent("2026-05-13T01:00:00Z", "cursor", "tool.completed", "tool", "repo")
	}
	writeTestLog(t, path, lines...)
	handler, err := Handler(Options{UserMode: true, LogPath: path})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/events?limit=3", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
	}
	var result EventResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.TotalMatched != 11 || result.Returned != 3 || !result.Truncated {
		t.Fatalf("wrong result metadata: %+v", result)
	}
	for i, want := range []string{"line-11", "line-10", "line-9"} {
		if result.Events[i].ID != want {
			t.Fatalf("event %d=%s, want %s", i, result.Events[i].ID, want)
		}
	}
}

func TestReadEventsNewestTiesUseEmissionSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	newest := testSchemaEvent("2026-05-13T01:00:00Z", "cursor", "tool.completed", "tool", "repo")
	newest.Sequence = 12
	older := newest
	older.Sequence = 3
	writeTestLog(t, path, marshalEvents(t, newest, older)...)
	for _, noLimit := range []bool{false, true} {
		result, err := ReadEvents(path, EventQuery{Limit: 1, NoLimit: noLimit})
		if err != nil {
			t.Fatal(err)
		}
		if result.Events[0].Event.Sequence != 12 {
			t.Fatalf("NoLimit=%t first sequence=%d, want 12", noLimit, result.Events[0].Event.Sequence)
		}
	}
}

func TestReadEventsNewestTiesKeepNewerRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	for _, suffix := range []string{".1", ".2"} {
		writeTestLog(t, path+suffix, testEvent("2026-05-13T01:00:00Z", "cursor", "tool.completed", "tool", "repo"))
	}
	result, err := ReadEvents(path, EventQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Events[0].ID != "archive-1-line-1" {
		t.Fatalf("first event=%s, want newest archive", result.Events[0].ID)
	}
}
