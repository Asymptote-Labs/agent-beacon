package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestTraceUTF8LabelsRebuildPreviousDerivation(t *testing.T) {
	prompt := testSchemaEvent("2026-10-13T12:00:00Z", "claude_code", "prompt.submitted", "prompt", "")
	prompt.Session = &schema.SessionInfo{ID: "utf8"}
	prompt.Prompt = &schema.PromptInfo{Text: strings.Repeat("界", 60)}
	prompt.Content = &schema.ContentInfo{Retention: "full", Included: true}
	path := traceStoreIndexedLog(t, marshalEvents(t, prompt))
	want := strings.Repeat("界", 26)
	got, ok, err := ShowTrace(path, "session:claude_code:utf8", TraceQuery{Limit: 10})
	if err != nil || !ok {
		t.Fatalf("show: %v %v", ok, err)
	}
	if !utf8.ValidString(got.Trace.Title) || got.Trace.Title != want {
		t.Fatalf("title: %q", got.Trace.Title)
	}
	store, err := openHistoryStore(false)
	if err != nil {
		t.Fatal(err)
	}
	// Version13 persisted byte-sliced labels as replacement characters. Force that
	// exact previous derivation, without changing the runtime log or fingerprint.
	if _, err = store.db.Exec(`UPDATE traces SET aggregate = replace(aggregate, ?, ?)`, `"title":"`+want+`"`, `"title":"stale replacement�"`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE meta SET value = '13' WHERE key = 'derivation_version'`); err != nil {
		t.Fatal(err)
	}
	store.close()
	rebuilt, ok, err := ShowTrace(path, "session:claude_code:utf8", TraceQuery{Limit: 10})
	if err != nil || !ok {
		t.Fatalf("rebuild: %v %v", ok, err)
	}
	if rebuilt.Trace.Title != want {
		t.Fatalf("served stale label %q", rebuilt.Trace.Title)
	}
	payload, err := json.Marshal(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(payload) || strings.Contains(string(payload), "stale replacement") {
		t.Fatalf("invalid/stale serialized trace %s", payload)
	}
}

func TestTraceSearchSnippetPreservesUTF8ByteCap(t *testing.T) {
	text := strings.Repeat("a", 239) + "😀" + " tail"
	event := testSchemaEvent("2026-10-13T12:00:00Z", "claude_code", "prompt.submitted", "prompt", "")
	event.Session = &schema.SessionInfo{ID: "utf8-search"}
	event.Prompt = &schema.PromptInfo{Text: text}
	event.Content = &schema.ContentInfo{Retention: "full", Included: true}
	path := traceStoreIndexedLog(t, marshalEvents(t, event))
	result, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "tail"}, ResultLevel: "event", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("search: %+v", result)
	}
	if got := result.Events[0].Snippet; !utf8.ValidString(got) || got != strings.Repeat("a", 239) {
		t.Fatalf("snippet: %q", got)
	}
}
