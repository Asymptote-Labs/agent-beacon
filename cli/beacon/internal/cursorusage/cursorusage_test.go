package cursorusage

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func createFixtureDB(t *testing.T, rows map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for key, value := range rows {
		if _, err := db.Exec(`INSERT INTO cursorDiskKV (key, value) VALUES (?, ?)`, key, []byte(value)); err != nil {
			t.Fatalf("insert %s: %v", key, err)
		}
	}
	return path
}

func TestExtractGenerationsToleratesShapesAndSkipsBadRows(t *testing.T) {
	path := createFixtureDB(t, map[string]string{
		// Camel-case tokenCount container with model info and bubble timestamp.
		"bubbleId:composer-1:bubble-1": `{
			"type": 2,
			"createdAt": 1751500000000,
			"modelInfo": {"modelName": "claude-4.5-sonnet"},
			"tokenCount": {"inputTokens": 1200, "outputTokens": 300, "cacheReadTokens": 5000, "cacheWriteTokens": 70}
		}`,
		// Snake-case usage container, model at top level, no timestamp anywhere.
		"bubbleId:composer-2:bubble-2": `{
			"model": "gpt-5.2",
			"usage": {"input_tokens": 10, "output_tokens": 20, "reasoning_tokens": 5}
		}`,
		// Zero counts: some Cursor builds record zeros; must be skipped, not estimated.
		"bubbleId:composer-1:bubble-3": `{"tokenCount": {"inputTokens": 0, "outputTokens": 0}}`,
		// No usage at all (a user message bubble).
		"bubbleId:composer-1:bubble-4": `{"type": 1, "text": "hello"}`,
		// Malformed JSON must be counted and skipped, never fail the sweep.
		"bubbleId:composer-1:bubble-5": `{"tokenCount": not-json`,
		// Composer timestamp fallback for bubble-6 (no bubble createdAt).
		"bubbleId:composer-3:bubble-6": `{"tokenCount": {"inputTokens": 42}}`,
		"composerData:composer-3":      `{"createdAt": 1751000000000}`,
	})

	db, cleanup, err := OpenSnapshot(path)
	defer cleanup()
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	generations, stats, err := ExtractGenerations(db)
	if err != nil {
		t.Fatalf("ExtractGenerations: %v", err)
	}
	if stats.Bubbles != 6 || stats.ParseErrors != 1 {
		t.Fatalf("stats = %+v, want 6 bubbles and 1 parse error", stats)
	}
	// bubble-3 (zeros) and bubble-4 (no usage) are zero-skips.
	if stats.SkippedZero != 2 {
		t.Fatalf("stats.SkippedZero = %d, want 2", stats.SkippedZero)
	}
	if len(generations) != 3 {
		t.Fatalf("generations = %d, want 3: %+v", len(generations), generations)
	}

	byBubble := map[string]Generation{}
	for _, g := range generations {
		byBubble[g.BubbleID] = g
	}

	g1 := byBubble["bubble-1"]
	if g1.Model != "claude-4.5-sonnet" || g1.ComposerID != "composer-1" {
		t.Fatalf("bubble-1 = %+v", g1)
	}
	if *g1.InputTokens != 1200 || *g1.OutputTokens != 300 || *g1.CacheReadTokens != 5000 || *g1.CacheCreationTokens != 70 {
		t.Fatalf("bubble-1 counts = %+v", g1)
	}
	if g1.TimestampSource != "bubble" || g1.Timestamp != time.UnixMilli(1751500000000).UTC() {
		t.Fatalf("bubble-1 timestamp = %v (%s)", g1.Timestamp, g1.TimestampSource)
	}

	g2 := byBubble["bubble-2"]
	if g2.Model != "gpt-5.2" || *g2.InputTokens != 10 || *g2.OutputTokens != 20 || *g2.ReasoningTokens != 5 {
		t.Fatalf("bubble-2 = %+v", g2)
	}
	if g2.TimestampSource != "sync" || !g2.Timestamp.IsZero() {
		t.Fatalf("bubble-2 timestamp source = %q, want sync", g2.TimestampSource)
	}

	g6 := byBubble["bubble-6"]
	if g6.TimestampSource != "composer" || g6.Timestamp != time.UnixMilli(1751000000000).UTC() {
		t.Fatalf("bubble-6 timestamp = %v (%s), want composer fallback", g6.Timestamp, g6.TimestampSource)
	}
}

func TestExtractGenerationsErrorsWithoutBubbleStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE other (key TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	db.Close()

	snap, cleanup, err := OpenSnapshot(path)
	defer cleanup()
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	if _, _, err := ExtractGenerations(snap); !errors.Is(err, ErrNoBubbleStore) {
		t.Fatalf("err = %v, want ErrNoBubbleStore", err)
	}
}

func TestOpenSnapshotReadsWALResidentRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatalf("disable autocheckpoint: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cursorDiskKV (key, value) VALUES (?, ?)`,
		"bubbleId:composer-wal:bubble-wal", []byte(`{"tokenCount": {"inputTokens": 9}}`)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("fixture did not produce a WAL file: %v", err)
	}

	// Snapshot while the source connection is still open, as with a live Cursor.
	snap, cleanup, err := OpenSnapshot(path)
	defer cleanup()
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	generations, _, err := ExtractGenerations(snap)
	if err != nil {
		t.Fatalf("ExtractGenerations: %v", err)
	}
	if len(generations) != 1 || generations[0].BubbleID != "bubble-wal" {
		t.Fatalf("generations = %+v, want the WAL-resident row", generations)
	}
}

func TestEventFromGenerationRoundTripsIntoSchema(t *testing.T) {
	input := int64(1200)
	output := int64(300)
	cacheRead := int64(5000)
	g := Generation{
		ComposerID:      "composer-1",
		BubbleID:        "bubble-1",
		Model:           "claude-4.5-sonnet",
		Timestamp:       time.UnixMilli(1751500000000).UTC(),
		TimestampSource: "bubble",
		InputTokens:     &input,
		OutputTokens:    &output,
		CacheReadTokens: &cacheRead,
	}
	ev := EventFromGeneration(g)
	if err := ev.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded schema.Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Event.Action != "token.usage" || decoded.Event.Category != "metric" {
		t.Fatalf("event = %+v", decoded.Event)
	}
	if decoded.Harness.Name != "cursor" || decoded.Session.ID != "composer-1" || decoded.Model != "claude-4.5-sonnet" {
		t.Fatalf("attribution = harness=%q session=%v model=%q", decoded.Harness.Name, decoded.Session, decoded.Model)
	}
	usage := decoded.GenAI.Usage
	if *usage.InputTokens != 1200 || *usage.OutputTokens != 300 || *usage.CacheRead.InputTokens != 5000 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.CacheCreation != nil || usage.Reasoning != nil || usage.CostUSD != nil {
		t.Fatalf("absent fields must stay absent: %+v", usage)
	}
	if name, _ := decoded.Raw["metric_name"].(string); name != MetricName {
		t.Fatalf("raw.metric_name = %q, want %q", name, MetricName)
	}
	cursorRaw := decoded.Raw["cursor"].(map[string]interface{})
	if cursorRaw["bubble_id"] != "bubble-1" || cursorRaw["source"] != "state.vscdb" {
		t.Fatalf("raw.cursor = %#v", cursorRaw)
	}
	if decoded.Timestamp != "2025-07-02T23:46:40Z" {
		t.Fatalf("timestamp = %q", decoded.Timestamp)
	}
}

func syncFixturePath(t *testing.T) string {
	t.Helper()
	return createFixtureDB(t, map[string]string{
		"bubbleId:composer-1:bubble-1": `{"createdAt": 1751500000000, "modelInfo": {"modelName": "claude-4.5-sonnet"}, "tokenCount": {"inputTokens": 100, "outputTokens": 10}}`,
		"bubbleId:composer-1:bubble-2": `{"createdAt": 1751500060000, "modelInfo": {"modelName": "claude-4.5-sonnet"}, "tokenCount": {"inputTokens": 200, "outputTokens": 20}}`,
		"bubbleId:composer-2:bubble-3": `{"createdAt": 1751500120000, "model": "gpt-5.2", "usage": {"input_tokens": 300, "output_tokens": 30}}`,
	})
}

func TestSyncOnceIsIdempotentAcrossRuns(t *testing.T) {
	dbPath := syncFixturePath(t)
	dir := t.TempDir()
	opts := Options{
		DBPath:    dbPath,
		StatePath: filepath.Join(dir, "state.json"),
		LogPath:   filepath.Join(dir, "runtime.jsonl"),
		UserMode:  true,
	}

	sum, err := SyncOnce(opts)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if sum.Emitted != 3 || sum.SkippedDedup != 0 {
		t.Fatalf("first sync summary = %+v, want 3 emitted", sum)
	}

	sum, err = SyncOnce(opts)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if sum.Emitted != 0 || sum.SkippedDedup != 3 {
		t.Fatalf("second sync summary = %+v, want 0 emitted / 3 deduped", sum)
	}

	data, err := os.ReadFile(opts.LogPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("log lines = %d, want 3", len(lines))
	}
	// Events must land oldest-first so the rollup's append-order contract holds.
	var first schema.Event
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode first line: %v", err)
	}
	if first.Raw["cursor"].(map[string]interface{})["bubble_id"] != "bubble-1" {
		t.Fatalf("first line = %s, want bubble-1 first", lines[0])
	}
}

func TestSyncOnceRebuildsStateFromLog(t *testing.T) {
	dbPath := syncFixturePath(t)
	dir := t.TempDir()
	opts := Options{
		DBPath:    dbPath,
		StatePath: filepath.Join(dir, "state.json"),
		LogPath:   filepath.Join(dir, "runtime.jsonl"),
		UserMode:  true,
	}
	if _, err := SyncOnce(opts); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if err := os.Remove(opts.StatePath); err != nil {
		t.Fatalf("remove state: %v", err)
	}

	sum, err := SyncOnce(opts)
	if err != nil {
		t.Fatalf("sync after state loss: %v", err)
	}
	if sum.Emitted != 0 || sum.SkippedDedup != 3 {
		t.Fatalf("summary after rebuild = %+v, want 0 emitted / 3 deduped", sum)
	}
}

func TestSyncOncePrintIsAStatelessDryRun(t *testing.T) {
	dbPath := syncFixturePath(t)
	dir := t.TempDir()
	var buf bytes.Buffer
	opts := Options{
		DBPath:    dbPath,
		StatePath: filepath.Join(dir, "state.json"),
		LogPath:   filepath.Join(dir, "runtime.jsonl"),
		Print:     true,
		Out:       &buf,
	}
	sum, err := SyncOnce(opts)
	if err != nil {
		t.Fatalf("print sync: %v", err)
	}
	if sum.Emitted != 3 {
		t.Fatalf("summary = %+v, want 3 emitted", sum)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("printed lines = %d, want 3", len(lines))
	}
	if _, err := os.Stat(opts.StatePath); !os.IsNotExist(err) {
		t.Fatalf("dry run must not write state: %v", err)
	}
	if _, err := os.Stat(opts.LogPath); !os.IsNotExist(err) {
		t.Fatalf("dry run must not write the log: %v", err)
	}
}

func TestSyncOnceHonorsSince(t *testing.T) {
	dbPath := syncFixturePath(t)
	dir := t.TempDir()
	opts := Options{
		DBPath:    dbPath,
		StatePath: filepath.Join(dir, "state.json"),
		LogPath:   filepath.Join(dir, "runtime.jsonl"),
		UserMode:  true,
		Since:     time.UnixMilli(1751500060000).UTC(),
	}
	sum, err := SyncOnce(opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if sum.Emitted != 2 || sum.SkippedBefore != 1 {
		t.Fatalf("summary = %+v, want 2 emitted / 1 before since", sum)
	}
}
