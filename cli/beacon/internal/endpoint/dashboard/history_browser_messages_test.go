package dashboard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestHistoryRebuildsVersion15LegacyBrowserMessages(t *testing.T) {
	for _, harness := range []string{"claude_web", "chatgpt_web"} {
		for _, removeLog := range []bool{false, true} {
			name := harness + "/raw-present"
			if removeLog {
				name = harness + "/retained-only"
			}
			t.Run(name, func(t *testing.T) {
				const answer = "A uniquely retained legacy browser answer."
				line := []byte(`{"timestamp":"2026-10-01T10:00:02Z","event":{"id":"legacy-browser-answer","action":"agent.response.completed","category":"agent"},"harness":{"name":"` + harness + `","collection_method":"otlp"},"session":{"id":"legacy-browser-session"},"message":"browser assistant response completed","model":"browser-model","gen_ai":{"output":{"messages":[{"role":"assistant","text":"` + answer + `"}]},"usage":{"input_tokens":10,"output_tokens":4}}}`)
				var event schema.Event
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatal(err)
				}
				path := traceStoreIndexedLog(t, [][]byte{line})
				traceID := asymptoteobserve.TraceProjectionID(event)
				store, err := openHistoryStore(false)
				if err != nil {
					t.Fatal(err)
				}
				var originalLine, originalRecordID, originalEventID string
				var originalRowID int64
				if err := store.db.QueryRow(`SELECT id, record_id, event_id, hex(line) FROM events`).Scan(&originalRowID, &originalRecordID, &originalEventID, &originalLine); err != nil {
					t.Fatal(err)
				}
				// Version 15 classified the completion as an agent message, but its
				// reader ignored role/text, leaving no retained content or search text.
				// Seed that actual prior derivation, not an arbitrary old version.
				prior := asymptoteobserve.TraceEventFromEvent(event, originalRecordID, 1)
				prior.Content = nil
				fold := newTraceFold(traceID, event)
				fold.fold(event, prior)
				if _, err := store.db.Exec(`UPDATE events SET hay = ?`, strings.ToLower(traceEventHaystack(prior))); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`UPDATE traces SET aggregate = ?, hay = ?`, mustJSON(t, fold.state()), strings.ToLower(traceSummaryHaystack(fold.summary()))); err != nil {
					t.Fatal(err)
				}
				for _, stmt := range []string{
					`INSERT INTO event_fts (event_fts) VALUES ('rebuild')`,
					`INSERT INTO trace_fts (trace_fts) VALUES ('rebuild')`,
					`UPDATE meta SET value = '15' WHERE key = 'derivation_version'`,
				} {
					if _, err := store.db.Exec(stmt); err != nil {
						t.Fatal(err)
					}
				}
				var oldMatches int
				if err := store.db.QueryRow(`SELECT count(*) FROM event_fts WHERE event_fts MATCH '"uniquely retained legacy"'`).Scan(&oldMatches); err != nil || oldMatches != 0 {
					t.Fatalf("prior search matches=%d err=%v", oldMatches, err)
				}
				store.close()
				if removeLog {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}

				// An ordinary public read must rebuild v15 content, summaries and FTS.
				got, ok, err := ShowTrace(path, traceID, TraceQuery{Limit: 100})
				if err != nil || !ok || len(got.Events) != 1 {
					t.Fatalf("upgraded ShowTrace ok=%v err=%v result=%+v", ok, err, got)
				}
				if got.Events[0].Content == nil || got.Events[0].Content.Text != answer || got.Trace.Title != answer || got.Trace.LocalMessageCount != 1 {
					t.Fatalf("legacy browser content was not rebuilt: %+v", got)
				}
				messages := asymptoteobserve.TraceMessages(TraceBundleV1{Events: got.Events})
				if len(messages) != 1 || messages[0].Role != "assistant" || messages[0].Text != answer {
					t.Fatalf("legacy answer missing from chat: %+v", messages)
				}
				for _, level := range []string{"event", "trace"} {
					search, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "uniquely retained legacy"}, ResultLevel: level, Limit: 10})
					if err != nil || search.TotalMatched != 1 {
						t.Fatalf("%s search = %+v err=%v", level, search, err)
					}
				}
				store, err = openHistoryStore(false)
				if err != nil {
					t.Fatal(err)
				}
				defer store.close()
				if version := store.metaInt("derivation_version", 0); version != 16 {
					t.Fatalf("derivation version=%d, want 16 (actual predecessor 15)", version)
				}
				var rowID int64
				var count int
				var recordID, eventID, storedLine string
				if err := store.db.QueryRow(`SELECT count(*), id, record_id, event_id, hex(line) FROM events`).Scan(&count, &rowID, &recordID, &eventID, &storedLine); err != nil {
					t.Fatal(err)
				}
				if count != 1 || rowID != originalRowID || recordID != originalRecordID || eventID != originalEventID || storedLine != originalLine {
					t.Fatal("upgrade changed stored raw bytes or event identities")
				}
				if !removeLog {
					raw, err := os.ReadFile(path)
					if err != nil || strings.TrimSpace(string(raw)) != string(line) {
						t.Fatalf("upgrade changed raw JSONL: err=%v raw=%q", err, raw)
					}
				}
				if again, ok, err := ShowTrace(path, traceID, TraceQuery{Limit: 100}); err != nil || !ok || mustJSON(t, again) != mustJSON(t, got) {
					t.Fatalf("second read changed projection: ok=%v err=%v", ok, err)
				}
			})
		}
	}
}
