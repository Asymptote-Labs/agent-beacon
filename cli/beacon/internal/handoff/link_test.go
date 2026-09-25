package handoff

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// writeHandoffPrompts makes every fixture session's first prompt the one `beacon handoff resume`
// writes for a new session continuing a Claude Code session.
func (f storeFixture) writeHandoffPrompts(t *testing.T, prompt string) {
	t.Helper()
	claudeMain := filepath.Join(f.dirs.ClaudeProjects, "-work-api", "claude-sess-1.jsonl")
	writeFixture(t, claudeMain, jsonLine(t, map[string]interface{}{
		"type": "user", "uuid": "u1", "sessionId": "claude-sess-1", "cwd": "/work/api", "timestamp": "2026-09-23T08:59:00.000Z",
		"message": map[string]interface{}{"role": "user", "content": prompt},
	}))
	setModTime(t, claudeMain, claudeUpdated)

	newer := filepath.Join(f.dirs.Codex, "sessions", "2026", "09", "21", "rollout-2026-09-21T09-00-00-codex-thread-1.jsonl")
	writeFixture(t, newer, jsonLine(t, map[string]interface{}{
		"timestamp": "2026-09-21T09:00:00.000Z", "type": "session_meta",
		"payload": map[string]interface{}{"id": "codex-thread-1", "session_id": "codex-thread-1", "cwd": "/work/web"},
	})+jsonLine(t, map[string]interface{}{
		"timestamp": "2026-09-21T09:00:01.000Z", "type": "response_item",
		"payload": map[string]interface{}{"type": "message", "role": "user", "content": []interface{}{map[string]interface{}{"type": "input_text", "text": prompt}}},
	}))
	setModTime(t, newer, codexUpdated)

	db, err := sql.Open("sqlite", filepath.Join(f.dirs.OpenCode, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	created := openCodeUpdated.UnixMilli() - 900
	if _, err := db.Exec(`INSERT INTO message VALUES ('msg_h', 'ses_parent', ?, ?)`, created, mustJSON(t, map[string]interface{}{
		"id": "msg_h", "role": "user", "time": map[string]interface{}{"created": created},
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO part VALUES ('part_h', 'msg_h', 'ses_parent', ?, ?)`, created+10, mustJSON(t, map[string]interface{}{
		"id": "part_h", "type": "text", "text": prompt,
	})); err != nil {
		t.Fatal(err)
	}

	messages := filepath.Join(f.dirs.Cline, "data", "sessions", "cline-task-1", "cline-task-1.messages.json")
	writeFixture(t, messages, mustJSON(t, map[string]interface{}{"messages": []interface{}{
		map[string]interface{}{"role": "user", "content": prompt, "ts": clineUpdated.UnixMilli()},
	}}))
	setModTime(t, messages, clineUpdated)
}

// Codex prompts reach Beacon through its session store rather than a prompt hook, so the store
// mappers are where a session continued in Codex gets linked. The other three are linked here too,
// for sessions whose hooks were not installed when the prompt was sent.
func TestSessionStoreMappersLinkAHandedOffSession(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	f := newStoreFixture(t)
	marker := asymptoteobserve.HandoffMarker(HarnessClaude, "src-session-1")
	f.writeHandoffPrompts(t, "Continue the work from an earlier Claude Code session.\n\n"+marker)
	sources := DefaultSources(f.dirs)

	for _, tc := range []struct{ harness, id string }{
		{HarnessClaude, "claude-sess-1"},
		{HarnessCodex, "codex-thread-1"},
		{HarnessOpenCode, "ses_parent"},
		{HarnessCline, "cline-task-1"},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			session, err := Find(sources, tc.harness, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			events, err := Events(sources, session)
			if err != nil {
				t.Fatal(err)
			}
			var prompt, link *schema.Event
			for i := range events {
				switch events[i].Event.Action {
				case "prompt.submitted":
					if prompt == nil {
						prompt = &events[i]
					}
				case "session.handoff":
					if link != nil {
						t.Fatalf("two session.handoff events for one prompt: %s", actionList(events))
					}
					link = &events[i]
				}
			}
			if prompt == nil || link == nil {
				t.Fatalf("events = %s, want a prompt and its session.handoff link", actionList(events))
			}
			if link.Handoff == nil || link.Handoff.SourceHarness != HarnessClaude || link.Handoff.SourceSessionID != "src-session-1" {
				t.Fatalf("handoff = %+v", link.Handoff)
			}
			if link.Session == nil || link.Session.ID != tc.id {
				t.Fatalf("link session = %+v, want %s", link.Session, tc.id)
			}
			if link.Harness.Name != tc.harness || link.Harness.CollectionMethod != schema.CollectionMethodPoll || link.Event.Fidelity != schema.FidelityObserved {
				t.Fatalf("link provenance = %+v / %q", link.Harness, link.Event.Fidelity)
			}
			if link.Prompt != nil || link.Content != nil {
				t.Fatal("the link must not repeat the prompt text")
			}
			if link.Event.ID == "" || link.Event.ID == prompt.Event.ID {
				t.Fatalf("link id %q must be its own and deterministic (prompt %q)", link.Event.ID, prompt.Event.ID)
			}
			again, _ := Events(sources, session)
			for _, e := range again {
				if e.Event.Action == "session.handoff" && e.Event.ID != link.Event.ID {
					t.Fatalf("re-reading the store renamed the link: %q then %q", link.Event.ID, e.Event.ID)
				}
			}
		})
	}
}

func TestSessionStoreMappersDoNotLinkWithoutAMarker(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	f := newStoreFixture(t)
	f.writeHandoffPrompts(t, "an ordinary prompt that mentions beacon-handoff in passing")
	sources := DefaultSources(f.dirs)
	for _, h := range Harnesses {
		sessions, _ := List(sources, Filter{Harness: h})
		for _, s := range sessions {
			events, err := Events(sources, s)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(actionList(events), "session.handoff") {
				t.Fatalf("%s linked a session with no marker: %s", h, actionList(events))
			}
		}
	}
}
