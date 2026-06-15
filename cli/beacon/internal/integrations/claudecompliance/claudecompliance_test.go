package claudecompliance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestPullActivitiesPaginatesWritesAndAdvancesCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Fatalf("missing API key header")
		}
		query := r.URL.Query()
		if query.Get("order") != "asc" || query.Get("limit") != "2" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		if got := query["activity_types[]"]; len(got) != 1 || got[0] != "claude_chat_created" {
			t.Fatalf("activity type filter = %#v", got)
		}
		switch query.Get("after_id") {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"activity_1","created_at":"2026-04-10T08:09:10Z","type":"claude_chat_created","actor":{"type":"user_actor","email_address":"user@example.com","user_id":"user_1"}}],"has_more":true,"first_id":"activity_1","last_id":"activity_1"}`))
		case "activity_1":
			_, _ = w.Write([]byte(`{"data":[{"id":"activity_2","created_at":"2026-04-10T08:10:10Z","type":"claude_file_uploaded","actor":{"type":"user_actor","email_address":"user@example.com","user_id":"user_1"}}],"has_more":false,"first_id":"activity_2","last_id":"activity_2"}`))
		default:
			t.Fatalf("unexpected after_id %q", query.Get("after_id"))
		}
	}))
	defer server.Close()

	var written []schema.Event
	statePath := filepath.Join(t.TempDir(), "state.json")
	summary, err := PullActivities(context.Background(), SyncOptions{
		Client: &Client{
			BaseURL:    server.URL,
			APIKey:     "test-key",
			HTTPClient: server.Client(),
			MaxRetries: -1,
		},
		StatePath: statePath,
		Query: Query{
			Limit:         2,
			CreatedAtGTE:  "2026-04-10T00:00:00Z",
			ActivityTypes: []string{"claude_chat_created"},
		},
		MaxPages: 2,
		Now:      fixedNow,
		WriteEvent: func(event schema.Event) error {
			written = append(written, event)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("PullActivities returned error: %v", err)
	}
	if summary.Fetched != 2 || summary.Written != 2 || summary.Pages != 2 {
		t.Fatalf("summary = %#v, want fetched/written/pages 2", summary)
	}
	if len(written) != 2 {
		t.Fatalf("written events = %d, want 2", len(written))
	}
	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.LastID != "activity_2" || len(state.RecentIDs) != 2 {
		t.Fatalf("state = %#v, want last activity_2 and 2 recent IDs", state)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Fatalf("state permissions = %o, want %o", got, want)
	}
}

func TestPullActivitiesUsesOverlapAndDedupesRecentIDs(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := SaveState(statePath, State{
		LastID:       "activity_old",
		LastSyncedAt: "2026-04-10T08:20:00Z",
		RecentIDs:    []SeenID{{ID: "activity_old", SeenAt: "2026-04-10T08:20:00Z"}},
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("created_at.gte") != "" {
			if got, want := query.Get("created_at.gte"), "2026-04-10T08:10:00Z"; got != want {
				t.Fatalf("overlap created_at.gte = %q, want %q", got, want)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"activity_late","created_at":"2026-04-10T08:15:00Z","type":"claude_chat_created"}],"has_more":false,"first_id":"activity_late","last_id":"activity_late"}`))
			return
		}
		if got := query.Get("after_id"); got != "activity_old" {
			t.Fatalf("incremental after_id = %q, want activity_old", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"activity_late","created_at":"2026-04-10T08:15:00Z","type":"claude_chat_created"},{"id":"activity_new","created_at":"2026-04-10T08:21:00Z","type":"claude_chat_created"}],"has_more":false,"first_id":"activity_late","last_id":"activity_new"}`))
	}))
	defer server.Close()

	var written []schema.Event
	summary, err := PullActivities(context.Background(), SyncOptions{
		Client:    &Client{BaseURL: server.URL, APIKey: "test-key", HTTPClient: server.Client(), MaxRetries: -1},
		StatePath: statePath,
		Query:     Query{Limit: 100},
		MaxPages:  1,
		Overlap:   10 * time.Minute,
		Now:       fixedNow,
		WriteEvent: func(event schema.Event) error {
			written = append(written, event)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("PullActivities returned error: %v", err)
	}
	if summary.Fetched != 3 || summary.Written != 2 || summary.Skipped != 1 || len(written) != 2 {
		t.Fatalf("summary=%#v written=%d, want fetched 3 written 2 skipped 1", summary, len(written))
	}
	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.LastID != "activity_new" || !state.HasSeen("activity_late") || !state.HasSeen("activity_new") {
		t.Fatalf("state after overlap = %#v", state)
	}
}

func TestPullActivitiesRequiresSinceForFirstPull(t *testing.T) {
	_, err := PullActivities(context.Background(), SyncOptions{
		Client:    &Client{APIKey: "test-key"},
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Query:     Query{Limit: 1},
	})
	if err != ErrSinceRequired {
		t.Fatalf("PullActivities error = %v, want ErrSinceRequired", err)
	}
}

func TestPullActivitiesDryRunDoesNotWriteStateOrEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"activity_1","created_at":"2026-04-10T08:09:10Z","type":"claude_chat_created"}],"has_more":false,"first_id":"activity_1","last_id":"activity_1"}`))
	}))
	defer server.Close()
	statePath := filepath.Join(t.TempDir(), "state.json")
	summary, err := PullActivities(context.Background(), SyncOptions{
		Client:    &Client{BaseURL: server.URL, APIKey: "test-key", HTTPClient: server.Client(), MaxRetries: -1},
		StatePath: statePath,
		Query:     Query{CreatedAtGTE: "2026-04-10T00:00:00Z"},
		DryRun:    true,
		Now:       fixedNow,
		WriteEvent: func(event schema.Event) error {
			t.Fatal("dry-run should not write events")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("PullActivities returned error: %v", err)
	}
	if summary.Written != 1 || !summary.DryRun {
		t.Fatalf("summary = %#v, want dry-run would-write count", summary)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("dry-run state stat error = %v, want not exist", err)
	}
}

func TestEventFromActivityUsesComplianceKind(t *testing.T) {
	activity := Activity{
		ID:        "activity_1",
		CreatedAt: "2026-04-10T08:09:10Z",
		Type:      "claude_chat_created",
		Actor: map[string]interface{}{
			"email_address": "user@example.com",
			"user_id":       "user_1",
		},
		Raw: map[string]interface{}{
			"id":             "activity_1",
			"type":           "claude_chat_created",
			"claude_chat_id": "claude_chat_1",
		},
	}
	event := EventFromActivity(activity, fixedNow())
	if event.Event.Kind == "agent_runtime" {
		t.Fatal("Compliance API event must not be emitted as agent_runtime")
	}
	if event.Event.Kind != "compliance_activity" || event.Event.Action != "compliance.activity.claude.chat.created" {
		t.Fatalf("event info = %#v", event.Event)
	}
	if event.Harness.Name != Name || event.Origin != schema.OriginCloud {
		t.Fatalf("source fields = harness %#v origin %q", event.Harness, event.Origin)
	}
	if event.User.Name != "user@example.com" || event.User.UID != "user_1" {
		t.Fatalf("user = %#v", event.User)
	}
	if event.GenAI == nil || event.GenAI.Conversation == nil || event.GenAI.Conversation.ID != "claude_chat_1" {
		t.Fatalf("gen_ai conversation missing: %#v", event.GenAI)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("event Validate returned error: %v", err)
	}
}

func TestClientRetriesRetryableStatus(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
	}))
	defer server.Close()
	client := &Client{
		BaseURL:    server.URL,
		APIKey:     "test-key",
		HTTPClient: server.Client(),
		MaxRetries: 1,
		Sleep: func(ctx context.Context, delay time.Duration) error {
			if delay != time.Second {
				t.Fatalf("retry delay = %s, want 1s", delay)
			}
			return nil
		},
	}
	if _, err := client.ListActivities(context.Background(), Query{Limit: 1}); err != nil {
		t.Fatalf("ListActivities returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestLastComplianceEventSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	lines := []byte(`not json
{"timestamp":"2026-04-10T08:09:10Z","harness":{"name":"other"}}
{"timestamp":"2026-04-10T08:10:10Z","harness":{"name":"claude_compliance"}}
`)
	if err := os.WriteFile(path, lines, 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	last, ok := LastComplianceEvent(path)
	if !ok {
		t.Fatal("expected compliance event")
	}
	if got, want := last.UTC().Format(time.RFC3339), "2026-04-10T08:10:10Z"; got != want {
		t.Fatalf("LastComplianceEvent = %s, want %s", got, want)
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 4, 10, 8, 30, 0, 0, time.UTC)
}

func TestActivityCapturesRawFields(t *testing.T) {
	var activity Activity
	if err := json.Unmarshal([]byte(`{"id":"activity_1","created_at":"2026-04-10T08:09:10Z","type":"claude_chat_created","extra":"value"}`), &activity); err != nil {
		t.Fatalf("unmarshal activity: %v", err)
	}
	if activity.ID != "activity_1" || activity.Raw["extra"] != "value" {
		t.Fatalf("activity = %#v", activity)
	}
}
