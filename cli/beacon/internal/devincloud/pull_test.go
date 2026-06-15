package devincloud

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeUploader struct {
	objects map[string][]byte
	calls   int
}

func (f *fakeUploader) Upload(_ context.Context, objectName string, data []byte) error {
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	f.objects[objectName] = data
	f.calls++
	return nil
}

func TestUploadEnabledLaterStillUploads(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "state.json")
	client := pullTestServer(t)

	// First sweep with upload disabled: local sync only.
	if _, err := PullOnce(context.Background(), client, PullOptions{Write: true, LogPath: logPath, StatePath: statePath}); err != nil {
		t.Fatalf("first (no upload): %v", err)
	}

	// Second sweep, session unchanged, upload now enabled: must NOT be skipped.
	up := &fakeUploader{}
	if _, err := PullOnce(context.Background(), client, PullOptions{
		Write: true, LogPath: logPath, StatePath: statePath, Upload: up, UploadPrefix: "agent-traces",
	}); err != nil {
		t.Fatalf("second (upload on): %v", err)
	}
	if up.calls != 1 {
		t.Fatalf("expected unchanged session to upload once upload was enabled, got %d uploads", up.calls)
	}

	// Third sweep, still unchanged and already uploaded: now skipped (no re-upload).
	up2 := &fakeUploader{}
	if _, err := PullOnce(context.Background(), client, PullOptions{
		Write: true, LogPath: logPath, StatePath: statePath, Upload: up2, UploadPrefix: "agent-traces",
	}); err != nil {
		t.Fatalf("third: %v", err)
	}
	if up2.calls != 0 {
		t.Fatalf("expected no re-upload once UploadedAt matches updated_at, got %d", up2.calls)
	}
}

type flakyUploader struct {
	failFirst int
	calls     int
}

func (f *flakyUploader) Upload(_ context.Context, _ string, _ []byte) error {
	f.calls++
	if f.calls <= f.failFirst {
		return fmt.Errorf("transient upload failure")
	}
	return nil
}

func TestPartialFailureRetriedThenSkipped(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "state.json")
	client := pullTestServer(t) // one suspended session, one user message
	up := &flakyUploader{failFirst: 1}
	opts := PullOptions{Write: true, LogPath: logPath, StatePath: statePath, Upload: up, UploadPrefix: "agent-traces"}

	// Sweep 1: local emit succeeds, upload fails -> session left unsynced.
	if _, err := PullOnce(context.Background(), client, opts); err == nil {
		t.Fatal("expected upload error on first sweep")
	}
	lines1 := len(nonEmptyLines(t, logPath))

	// Sweep 2: not skipped (SyncedAt never advanced); reprocess + upload succeeds,
	// with no duplicate local lines.
	sum2, err := PullOnce(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if sum2.SessionsChanged != 1 || sum2.Uploaded != 1 {
		t.Fatalf("partial-failed session should retry+upload, got %+v", sum2)
	}
	if got := len(nonEmptyLines(t, logPath)); got != lines1 {
		t.Fatalf("retry duplicated local lines: %d -> %d", lines1, got)
	}

	// Sweep 3: now fully synced and uploaded -> skipped.
	sum3, err := PullOnce(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	if sum3.SessionsChanged != 0 {
		t.Fatalf("fully synced session should be skipped, got %+v", sum3)
	}
}

func pullTestServer(t *testing.T) *Client {
	srv := fakeDevinAPI(t, `{"items":[
		{"session_id":"s1","status":"suspended","user_id":"user-1","created_at":1000,"updated_at":1600,"acus_consumed":1}
	],"has_next_page":false,"total":1}`, map[string]string{
		"s1": `{"items":[
			{"event_id":"e1","source":"user","message":"prompt one","created_at":1000},
			{"event_id":"e2","source":"devin","message":"reply one","created_at":1100}
		],"has_next_page":false}`,
	})
	t.Cleanup(srv.Close)
	return testClient(t, srv)
}

func TestPullOnceWritesAndUploads(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "state.json")
	up := &fakeUploader{}

	client := pullTestServer(t)
	sum, err := PullOnce(context.Background(), client, PullOptions{
		Write:        true,
		LogPath:      logPath,
		StatePath:    statePath,
		Upload:       up,
		UploadPrefix: "agent-traces",
	})
	if err != nil {
		t.Fatalf("PullOnce: %v", err)
	}
	// started + prompt + agent + ended = 4
	if sum.EventsEmitted != 4 || sum.SessionsChanged != 1 || sum.Uploaded != 1 {
		t.Fatalf("summary = %+v, want 4 events / 1 changed / 1 uploaded", sum)
	}

	lines := nonEmptyLines(t, logPath)
	if len(lines) != 4 {
		t.Fatalf("log has %d lines, want 4", len(lines))
	}

	wantObj := "agent-traces/provider=devin_cloud/user_id=user-1/run_id=s1/runtime.jsonl"
	body, ok := up.objects[wantObj]
	if !ok {
		t.Fatalf("uploaded object %q missing; have %v", wantObj, keys(up.objects))
	}
	if got := len(nonEmptyLinesBytes(body)); got != 4 {
		t.Fatalf("uploaded object has %d lines, want 4", got)
	}
}

func TestUploadSnapshotIsRedacted(t *testing.T) {
	dir := t.TempDir()
	up := &fakeUploader{}
	srv := fakeDevinAPI(t, `{"items":[
		{"session_id":"s1","status":"suspended","user_id":"user-1","created_at":1000,"updated_at":1600}
	],"has_next_page":false,"total":1}`, map[string]string{
		"s1": `{"items":[
			{"event_id":"e1","source":"user","message":"deploy with token=supersecretvalue123456","created_at":1000}
		],"has_next_page":false}`,
	})
	defer srv.Close()

	_, err := PullOnce(context.Background(), testClient(t, srv), PullOptions{
		Write:        true,
		LogPath:      filepath.Join(dir, "runtime.jsonl"),
		StatePath:    filepath.Join(dir, "state.json"),
		Upload:       up,
		UploadPrefix: "agent-traces",
	})
	if err != nil {
		t.Fatalf("PullOnce: %v", err)
	}
	for obj, body := range up.objects {
		if strings.Contains(string(body), "supersecretvalue123456") {
			t.Fatalf("uploaded object %q leaked an unredacted secret:\n%s", obj, body)
		}
	}
}

func TestPullOnceDedupsAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "state.json")
	client := pullTestServer(t)

	opts := PullOptions{Write: true, LogPath: logPath, StatePath: statePath}
	if _, err := PullOnce(context.Background(), client, opts); err != nil {
		t.Fatalf("first PullOnce: %v", err)
	}
	sum, err := PullOnce(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("second PullOnce: %v", err)
	}
	// Unchanged session → skipped entirely on the second sweep.
	if sum.EventsEmitted != 0 || sum.SessionsChanged != 0 {
		t.Fatalf("second sweep emitted %+v, want zero (dedup)", sum)
	}
	if len(nonEmptyLines(t, logPath)) != 4 {
		t.Fatalf("log grew on re-run; dedup failed")
	}
}

func TestPullOncePersistsStateOnError(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "state.json")

	var s2Calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[
			{"session_id":"s1","status":"suspended","user_id":"u","created_at":10,"updated_at":20},
			{"session_id":"s2","status":"suspended","user_id":"u","created_at":10,"updated_at":20}
		],"has_next_page":false,"total":2}`))
	})
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions/s1/messages", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"event_id":"e1","source":"user","message":"hi","created_at":10}],"has_next_page":false}`))
	})
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions/s2/messages", func(w http.ResponseWriter, _ *http.Request) {
		s2Calls++
		if s2Calls == 1 {
			http.Error(w, `{"detail":"boom"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"event_id":"e2","source":"user","message":"yo","created_at":10}],"has_next_page":false}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := New(testOrg, "cog_test", WithBaseURL(srv.URL), WithRetry(0, 0))

	opts := PullOptions{Write: true, LogPath: logPath, StatePath: statePath}

	// First sweep: s1 succeeds and is written; s2 errors -> PullOnce returns error.
	if _, err := PullOnce(context.Background(), client, opts); err == nil {
		t.Fatal("expected error from failing session")
	}
	afterFirst := len(nonEmptyLines(t, logPath)) // s1: started + prompt + ended = 3

	// Second sweep: s1 must be skipped (state persisted), s2 now succeeds.
	if _, err := PullOnce(context.Background(), client, opts); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	total := len(nonEmptyLines(t, logPath))
	added := total - afterFirst
	if added != 3 { // only s2's started + prompt + ended; s1 not re-emitted
		t.Fatalf("second sweep added %d lines, want 3 (s1 not duplicated)", added)
	}
}

func TestOneSessionErrorDoesNotBlockOthers(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")

	mux := http.NewServeMux()
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions", func(w http.ResponseWriter, _ *http.Request) {
		// "bad" sorts before "good"; bad fails, good must still be processed.
		_, _ = w.Write([]byte(`{"items":[
			{"session_id":"bad","status":"suspended","user_id":"u","created_at":10,"updated_at":20},
			{"session_id":"good","status":"suspended","user_id":"u","created_at":10,"updated_at":20}
		],"has_next_page":false,"total":2}`))
	})
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions/bad/messages", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"always fails"}`, http.StatusInternalServerError)
	})
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions/good/messages", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"event_id":"g1","source":"user","message":"hi","created_at":10}],"has_next_page":false}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := New(testOrg, "cog_test", WithBaseURL(srv.URL), WithRetry(0, 0))

	sum, err := PullOnce(context.Background(), client, PullOptions{
		Write: true, LogPath: logPath, StatePath: filepath.Join(dir, "state.json"),
	})
	if err == nil {
		t.Fatal("expected an aggregated error from the failing session")
	}
	if sum.Errors != 1 {
		t.Fatalf("Errors = %d, want 1", sum.Errors)
	}
	// The healthy session must still have been ingested despite the bad one.
	lines := nonEmptyLines(t, logPath)
	if len(lines) != 3 { // good: started + prompt + ended
		t.Fatalf("healthy session not ingested: %d lines, want 3", len(lines))
	}
	for _, l := range lines {
		if !strings.Contains(l, `"run_id":"good"`) {
			t.Fatalf("unexpected event from non-good session: %s", l)
		}
	}
}

func TestForceRefreshReFetchesWithoutDuplicating(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "state.json")

	var msgCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"session_id":"s1","status":"suspended","user_id":"u","created_at":10,"updated_at":20}],"has_next_page":false,"total":1}`))
	})
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions/s1/messages", func(w http.ResponseWriter, _ *http.Request) {
		msgCalls++
		_, _ = w.Write([]byte(`{"items":[{"event_id":"e1","source":"user","message":"hi","created_at":10}],"has_next_page":false}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := New(testOrg, "cog_test", WithBaseURL(srv.URL), WithRetry(0, 0))

	base := PullOptions{Write: true, LogPath: logPath, StatePath: statePath}
	if _, err := PullOnce(context.Background(), client, base); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Without force, an unchanged session is skipped (no second message fetch).
	if _, err := PullOnce(context.Background(), client, base); err != nil {
		t.Fatalf("second: %v", err)
	}
	if msgCalls != 1 {
		t.Fatalf("expected 1 message fetch (skip on unchanged), got %d", msgCalls)
	}

	// With force, the unchanged session is re-fetched, but dedup prevents new log lines.
	before := len(nonEmptyLines(t, logPath))
	forced := base
	forced.ForceRefresh = true
	if _, err := PullOnce(context.Background(), client, forced); err != nil {
		t.Fatalf("forced: %v", err)
	}
	if msgCalls != 2 {
		t.Fatalf("expected re-fetch with ForceRefresh, msgCalls=%d", msgCalls)
	}
	if got := len(nonEmptyLines(t, logPath)); got != before {
		t.Fatalf("force-resync duplicated log lines: %d -> %d", before, got)
	}
}

func TestEndedEmittedOncePerObservedEpisode(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")

	current := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(current))
	})
	mux.HandleFunc("/v3/organizations/"+testOrg+"/sessions/s1/messages", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"event_id":"e1","source":"user","message":"hi","created_at":10}],"has_next_page":false}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := New(testOrg, "cog_test", WithBaseURL(srv.URL), WithRetry(0, 0))
	opts := PullOptions{Write: true, LogPath: logPath, StatePath: filepath.Join(dir, "state.json")}

	sess := func(status string, updated int) string {
		return fmt.Sprintf(`{"items":[{"session_id":"s1","status":%q,"user_id":"u","created_at":10,"updated_at":%d}],"has_next_page":false,"total":1}`, status, updated)
	}
	sweep := func() {
		if _, err := PullOnce(context.Background(), client, opts); err != nil {
			t.Fatalf("sweep: %v", err)
		}
	}

	current = sess("suspended", 20)
	sweep() // end #1
	current = sess("suspended", 30)
	sweep() // updated_at bump, no observed resume -> NO new end
	current = sess("working", 40)
	sweep() // resumed -> reset
	current = sess("suspended", 50)
	sweep() // end #2

	ends := 0
	for _, l := range nonEmptyLines(t, logPath) {
		if strings.Contains(l, `"action":"session.ended"`) {
			ends++
		}
	}
	if ends != 2 {
		t.Fatalf("session.ended count = %d, want 2 (one per observed terminal episode)", ends)
	}
}

func nonEmptyLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return nonEmptyLinesBytes(data)
}

func nonEmptyLinesBytes(data []byte) []string {
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
