package devincloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testOrg = "org-test123"

// fakeDevinAPI serves canned v3 responses for one org with the given sessions
// and per-session messages.
func fakeDevinAPI(t *testing.T, sessionsJSON string, messagesJSON map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cog_test" {
			http.Error(w, `{"detail":"unauthorized"}`, http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(sessionsJSON))
	})
	for sid, body := range messagesJSON {
		b := body
		mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions/"+sid+"/messages", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(b))
		})
	}
	return httptest.NewServer(mux)
}

func testClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(testOrg, "cog_test", WithBaseURL(srv.URL), WithRetry(1, 0))
}

func TestListSessionsParsesFields(t *testing.T) {
	srv := fakeDevinAPI(t, `{
		"items": [
			{"session_id":"s1","status":"suspended","status_detail":"inactivity","title":"T","user_id":"u1","org_id":"org-test123","created_at":100,"updated_at":200,"acus_consumed":1.5,"pull_requests":[{"url":"https://github.com/acme/widgets/pull/7","number":7}],"child_session_ids":["c1"]}
		],
		"end_cursor": null,
		"has_next_page": false,
		"total": 1
	}`, nil)
	defer srv.Close()

	sessions, err := testClient(t, srv).ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	s := sessions[0]
	if s.SessionID != "s1" || s.Status != "suspended" || s.UserID != "u1" || s.AcusConsumed != 1.5 {
		t.Fatalf("unexpected session: %+v", s)
	}
	if s.CreatedAt != 100 || s.UpdatedAt != 200 {
		t.Fatalf("timestamps = %d/%d, want 100/200", s.CreatedAt, s.UpdatedAt)
	}
	if len(s.PullRequests) != 1 || s.PullRequests[0].Number != 7 {
		t.Fatalf("pull_requests = %+v", s.PullRequests)
	}
}

func TestListSessionsFollowsCursor(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"items":[{"session_id":"s1"}],"end_cursor":"CUR","has_next_page":true,"total":2}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"session_id":"s2"}],"end_cursor":null,"has_next_page":false,"total":2}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sessions, err := New(testOrg, "cog_test", WithBaseURL(srv.URL), WithRetry(0, 0)).ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 || sessions[0].SessionID != "s1" || sessions[1].SessionID != "s2" {
		t.Fatalf("pagination failed: %+v", sessions)
	}
	if calls != 2 {
		t.Fatalf("expected 2 page fetches, got %d", calls)
	}
}

func TestSessionMessagesParses(t *testing.T) {
	srv := fakeDevinAPI(t, `{"items":[],"has_next_page":false}`, map[string]string{
		"s1": `{"items":[
			{"event_id":"e1","source":"user","message":"hi","created_at":100},
			{"event_id":"e2","source":"devin","message":"hello","created_at":101}
		],"end_cursor":null,"has_next_page":false}`,
	})
	defer srv.Close()

	msgs, err := testClient(t, srv).SessionMessages(context.Background(), "s1")
	if err != nil {
		t.Fatalf("SessionMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Source != "user" || msgs[1].Source != "devin" || msgs[0].EventID != "e1" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
}

func TestGetJSONErrorsOnForbidden(t *testing.T) {
	srv := fakeDevinAPI(t, `{"items":[]}`, nil)
	defer srv.Close()
	c := New(testOrg, "wrong-key", WithBaseURL(srv.URL), WithRetry(0, 0))
	_, err := c.ListSessions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
}

func TestGetJSONRequiresOrgAndKey(t *testing.T) {
	if _, err := New("", "k").ListSessions(context.Background()); err == nil {
		t.Fatal("expected error for missing org")
	}
	if _, err := New("org-x", "").ListSessions(context.Background()); err == nil {
		t.Fatal("expected error for missing key")
	}
	_ = time.Second
}
