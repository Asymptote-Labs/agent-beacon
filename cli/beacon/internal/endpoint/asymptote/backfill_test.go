package asymptote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func backfillTestLine(ts, method, id string) string {
	return `{"timestamp":"` + ts + `","vendor":"beacon","event":{"action":"prompt.submitted","id":"` + id + `"},"harness":{"name":"codex","collection_method":"` + method + `"}}`
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stagedIDs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		start := strings.Index(line, `"id":"`) + len(`"id":"`)
		ids = append(ids, line[start:start+strings.Index(line[start:], `"`)])
	}
	return ids
}

func TestStageBackfillCopiesRecentPollLinesOldestFileFirst(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeLines(t, logPath+".1",
		backfillTestLine("2026-09-01T00:00:00Z", "poll", "too-old"),
		backfillTestLine("2026-09-20T00:00:00Z", "poll", "archived"),
	)
	writeLines(t, logPath,
		backfillTestLine("2026-09-21T00:00:00Z", "hook", "live-hook"),
		"not json",
		backfillTestLine("2026-09-22T00:00:00Z", "poll", "recent"),
	)

	result, err := StageBackfill(logPath, BackfillOptions{Since: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != filepath.Join(filepath.Dir(logPath), BackfillLogName) || result.Events != 2 || result.Trimmed {
		t.Fatalf("result = %+v", result)
	}
	if got := stagedIDs(t, result.Path); strings.Join(got, ",") != "archived,recent" {
		t.Fatalf("staged %v, want [archived recent]", got)
	}
	if testenv.HasPOSIXFileModes() {
		if info, err := os.Stat(result.Path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("staged file mode: %v %v", info, err)
		}
	}
}

func TestStageBackfillKeepsTheNewestLinesThatFit(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	older := backfillTestLine("2026-09-20T00:00:00Z", "poll", "older")
	newer := backfillTestLine("2026-09-21T00:00:00Z", "poll", "newer")
	writeLines(t, logPath, older, newer)

	result, err := StageBackfill(logPath, BackfillOptions{MaxBytes: int64(len(newer) + 1)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Trimmed || result.Events != 1 || result.Bytes != int64(len(newer)+1) {
		t.Fatalf("result = %+v", result)
	}
	if got := stagedIDs(t, result.Path); strings.Join(got, ",") != "newer" {
		t.Fatalf("staged %v, want [newer]", got)
	}
}

func TestStageBackfillWritesNothingWithoutBackfill(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeLines(t, logPath, backfillTestLine("2026-09-21T00:00:00Z", "otlp", "live"))
	result, err := StageBackfill(logPath, BackfillOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Events != 0 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
		t.Fatalf("an empty backfill left a file behind: %v", err)
	}
}

func TestVectorConfigReadsTheStagedBackfillFromTheBeginningInEveryPrivacyMode(t *testing.T) {
	logPath := "/tmp/beacon/runtime.jsonl"
	for _, mode := range []string{"standard", "metadata-only"} {
		got, err := RenderVectorConfig(RenderOptions{
			LogPath:     logPath,
			IngestURL:   "https://ingest.example.test",
			SecretsFile: "/tmp/secrets.json",
			DataDir:     "/tmp/vector-data",
			PrivacyMode: mode,
		})
		if err != nil {
			t.Fatal(err)
		}
		source := "[sources.beacon_backfill]\ntype = \"file\"\ninclude = [\"/tmp/beacon/" + BackfillLogName + "\"]\nread_from = \"beginning\""
		if !strings.Contains(got, source) {
			t.Fatalf("%s config has no backfill source:\n%s", mode, got)
		}
		want := `inputs = ["beacon_runtime", "beacon_backfill"]`
		if mode == "metadata-only" {
			// The backfill goes through the same transform as live lines, never around it.
			if !strings.Contains(got, "[transforms.beacon_runtime_metadata]\ntype = \"remap\"\n"+want) ||
				strings.Count(got, `"beacon_backfill"`) != 1 {
				t.Fatalf("metadata-only config lets the backfill bypass the transform:\n%s", got)
			}
			continue
		}
		if !strings.Contains(got, "[sinks.asymptote_runtime]\ntype = \"http\"\n"+want) {
			t.Fatalf("standard config does not ship the backfill:\n%s", got)
		}
	}
	hand, err := VectorConfig(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hand, "{{") || !strings.Contains(hand, "/tmp/beacon/"+BackfillLogName) {
		t.Fatalf("hand-run config does not resolve the backfill path:\n%s", hand)
	}
}

func TestFirstConnectStagesTheBackfillBeforeTheForwarderStartsAndReconnectDoesNot(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	vector := fakeVector(t, "0.56.0", 0)
	fwd := &fakeForwarder{supported: true}
	opts := connectOptions(t, fd, fwd, vector)
	staged := BackfillLogPath(opts.LogPath)

	synced := 0
	opts.Backfill = &ConnectBackfill{
		Sync: func() error {
			synced++
			// The sweep runs before any key exists.
			if _, err := ReadDeviceKey(true); err == nil {
				t.Error("backfill sync ran after the device key was stored")
			}
			writeLines(t, opts.LogPath, backfillTestLine(time.Now().UTC().Format(time.RFC3339), "poll", "fresh"))
			return errors.New("one runtime could not be read")
		},
	}
	var loadsAtStage int
	fwd.onLoad = func() {
		if _, err := os.Stat(staged); err == nil {
			loadsAtStage++
		}
	}
	result, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("a failing backfill sync failed the connect: %v", err)
	}
	if synced != 1 || result.Backfill == nil || result.Backfill.Events != 1 || loadsAtStage != 1 {
		t.Fatalf("synced=%d backfill=%+v staged-before-load=%d", synced, result.Backfill, loadsAtStage)
	}

	// A re-connect resumes from the forwarder's checkpoints: no sweep, no new stage.
	if err := os.Remove(staged); err != nil {
		t.Fatal(err)
	}
	again, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if synced != 1 || again.Backfill != nil {
		t.Fatalf("re-connect synced=%d backfill=%+v", synced, again.Backfill)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("re-connect staged the backfill again")
	}
}

func TestPrivacyModeChangeDropsTheStagedBackfillWithTheCheckpoints(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	vector := fakeVector(t, "0.56.0", 0)
	fwd := &fakeForwarder{supported: true}
	opts := connectOptions(t, fd, fwd, vector)
	writeLines(t, opts.LogPath, backfillTestLine(time.Now().UTC().Format(time.RFC3339), "poll", "fresh"))
	opts.Backfill = &ConnectBackfill{}
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	staged := BackfillLogPath(opts.LogPath)
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("first connect did not stage: %v", err)
	}
	opts.PrivacyMode = "metadata-only"
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("a privacy mode change kept a staged backfill whose checkpoint it cleared")
	}
}

func TestDisconnectRemovesTheStagedBackfill(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	opts := connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0))
	writeLines(t, opts.LogPath, backfillTestLine(time.Now().UTC().Format(time.RFC3339), "poll", "fresh"))
	opts.Backfill = &ConnectBackfill{}
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := Disconnect(DisconnectOptions{UserMode: true, LogPath: opts.LogPath, Forwarder: fwd}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(BackfillLogPath(opts.LogPath)); !os.IsNotExist(err) {
		t.Fatal("disconnect left the staged backfill behind")
	}
}
