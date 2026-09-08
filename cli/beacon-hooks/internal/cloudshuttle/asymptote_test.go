package cloudshuttle

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type ingestCall struct {
	auth, contentType, encoding, userAgent, path string
	lines                                        []string
}

func startFakeIngest(t *testing.T, status int) (*httptest.Server, *[]ingestCall) {
	t.Helper()
	var mu sync.Mutex
	calls := &[]ingestCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("body is not gzip: %v", err)
			w.WriteHeader(400)
			return
		}
		raw, _ := io.ReadAll(gz)
		mu.Lock()
		*calls = append(*calls, ingestCall{
			auth:        r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"),
			encoding:    r.Header.Get("Content-Encoding"),
			userAgent:   r.Header.Get("User-Agent"),
			path:        r.URL.Path,
			lines:       strings.Split(strings.TrimRight(string(raw), "\n"), "\n"),
		})
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"accepted":1}`))
	}))
	t.Cleanup(server.Close)
	oldClient := httpClient
	httpClient = server.Client()
	t.Cleanup(func() { httpClient = oldClient })
	return server, calls
}

func asymptoteConfig(t *testing.T, server *httptest.Server) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{
		Upload:    uploadAsymptote,
		LogPath:   filepath.Join(dir, "runtime.jsonl"),
		StatePath: filepath.Join(dir, "state.json"),
		IngestURL: server.URL,
		DeviceKey: "bcn_device_test0000_" + strings.Repeat("k", 43),
		Provider:  "claude_code_web",
		RunID:     "run-1",
	}
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigFromEnvSelectsAsymptote(t *testing.T) {
	t.Setenv("BEACON_CLOUD_UPLOAD", "asymptote")
	t.Setenv("BEACON_CLOUD_INGEST_URL", "https://ingest.example/")
	t.Setenv("BEACON_CLOUD_DEVICE_KEY", "bcn_device_abc")
	t.Setenv("BEACON_RUN_ID", "run-9")
	cfg := normalizeConfig(ConfigFromEnv())
	if cfg.Upload != uploadAsymptote || cfg.IngestURL != "https://ingest.example" || cfg.DeviceKey != "bcn_device_abc" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if !uploadConfigured(cfg) {
		t.Fatal("asymptote target with https URL and device key should be configured without a bucket")
	}
}

func TestAsymptoteTargetRefusesInsecureURLsAndForeignKeys(t *testing.T) {
	base := Config{Upload: uploadAsymptote, DeviceKey: "bcn_device_x", IngestURL: "https://ingest.example"}
	if !uploadConfigured(base) {
		t.Fatal("baseline should be configured")
	}
	insecure := base
	insecure.IngestURL = "http://ingest.example"
	if uploadConfigured(insecure) {
		t.Fatal("plain http to a remote host must not carry a device key")
	}
	loopback := base
	loopback.IngestURL = "http://127.0.0.1:8080"
	if !uploadConfigured(loopback) {
		t.Fatal("plain http to loopback is allowed for local development")
	}
	foreign := base
	foreign.DeviceKey = "ask_live_notadevicekey"
	if uploadConfigured(foreign) {
		t.Fatal("only bcn_device_ keys may be sent to managed ingest")
	}
}

func TestUploadPostsOnlyNewLinesToManagedIngest(t *testing.T) {
	server, calls := startFakeIngest(t, 200)
	cfg := asymptoteConfig(t, server)
	appendLines(t, cfg.LogPath, `{"event":1}`, `{"event":2}`)

	if err := Upload(context.Background(), cfg, true); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one POST, got %d", len(*calls))
	}
	first := (*calls)[0]
	if first.path != "/v1/ingest/runtime" || first.auth != "Bearer "+cfg.DeviceKey || first.contentType != "application/x-ndjson" || first.encoding != "gzip" || first.userAgent != "beacon-hooks-cloud" {
		t.Fatalf("unexpected request shape: %+v", first)
	}
	if strings.Join(first.lines, "|") != `{"event":1}|{"event":2}` {
		t.Fatalf("first batch lines = %v", first.lines)
	}

	// The next Stop ships only what was appended since, plus never a partial trailing line.
	appendLines(t, cfg.LogPath, `{"event":3}`)
	f, _ := os.OpenFile(cfg.LogPath, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(`{"event":4,"partial":`)
	_ = f.Close()
	if err := Upload(context.Background(), cfg, true); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if len(*calls) != 2 || strings.Join((*calls)[1].lines, "|") != `{"event":3}` {
		t.Fatalf("second batch should carry only the new complete line: %+v", *calls)
	}

	// Nothing new: no request.
	if err := Upload(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("no new complete lines must mean no POST, got %d calls", len(*calls))
	}
}

func TestUploadSplitsLargeLogsIntoBatchesUnderTheIngestLimits(t *testing.T) {
	server, calls := startFakeIngest(t, 200)
	cfg := asymptoteConfig(t, server)
	old := asymptoteBatchLines
	asymptoteBatchLines = 2
	t.Cleanup(func() { asymptoteBatchLines = old })
	appendLines(t, cfg.LogPath, `{"n":1}`, `{"n":2}`, `{"n":3}`, `{"n":4}`, `{"n":5}`)
	if err := Upload(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, c := range *calls {
		sizes = append(sizes, len(c.lines))
	}
	if len(sizes) != 3 || sizes[0] != 2 || sizes[1] != 2 || sizes[2] != 1 {
		t.Fatalf("batches = %v, want 2,2,1", sizes)
	}
}

func TestUploadKeepsTheOffsetWhenTheKeyIsRejected(t *testing.T) {
	server, calls := startFakeIngest(t, 401)
	cfg := asymptoteConfig(t, server)
	appendLines(t, cfg.LogPath, `{"event":1}`)
	err := Upload(context.Background(), cfg, true)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected a revoked-key error, got %v", err)
	}
	if _, statErr := os.Stat(cfg.StatePath); !os.IsNotExist(statErr) {
		t.Fatal("a rejected batch must not advance the offset")
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d", len(*calls))
	}
}

func TestUploadNoopsWithoutForceOrRunID(t *testing.T) {
	server, calls := startFakeIngest(t, 200)
	cfg := asymptoteConfig(t, server)
	appendLines(t, cfg.LogPath, `{"event":1}`)
	if err := Upload(context.Background(), cfg, false); err != nil || len(*calls) != 0 {
		t.Fatalf("non-forced upload must not post: err=%v calls=%d", err, len(*calls))
	}
	cfg.RunID = ""
	if err := Upload(context.Background(), cfg, true); err != nil || len(*calls) != 0 {
		t.Fatalf("no run id must not post: err=%v calls=%d", err, len(*calls))
	}
}

func TestSessionStartFlushesThePreviousRunsTailToManagedIngest(t *testing.T) {
	server, calls := startFakeIngest(t, 200)
	cfg := asymptoteConfig(t, server)
	appendLines(t, cfg.LogPath, `{"event":1}`)
	if err := Upload(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	appendLines(t, cfg.LogPath, `{"event":2}`) // written after the last Stop hook ran

	// A new run starts in the same sandbox: only the unsent line goes out, then the log is cleared.
	next := cfg
	next.RunID = "run-2"
	if err := preserveExistingLog(next); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || strings.Join((*calls)[1].lines, "|") != `{"event":2}` {
		t.Fatalf("expected exactly the unsent tail to be flushed: %+v", *calls)
	}
	if _, err := os.Stat(cfg.LogPath); !os.IsNotExist(err) {
		t.Fatal("the flushed log should be removed before the new run starts")
	}
}

func TestSessionStartShipsALogWhoseRunNeverUploaded(t *testing.T) {
	server, calls := startFakeIngest(t, 200)
	cfg := asymptoteConfig(t, server)
	appendLines(t, cfg.LogPath, `{"event":1}`, `{"event":2}`) // the previous run's Stop hook never fired

	next := cfg
	next.RunID = "run-2"
	if err := preserveExistingLog(next); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || strings.Join((*calls)[0].lines, "|") != `{"event":1}|{"event":2}` {
		t.Fatalf("expected the whole never-sent log to be posted: %+v", *calls)
	}
	if _, err := os.Stat(cfg.LogPath); !os.IsNotExist(err) {
		t.Fatal("the shipped log should be removed, not renamed aside")
	}
	if matches, _ := filepath.Glob(cfg.LogPath + ".previous-*"); len(matches) != 0 {
		t.Fatalf("no .previous-* copy should be left behind: %v", matches)
	}
}
