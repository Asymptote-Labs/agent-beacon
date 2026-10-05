package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

// The local history store backs `beacon endpoint traces` once the user opts in to it.
//
// It replaces traces.db, which was a cache: dropped and rebuilt from whatever JSONL was on disk
// whenever the log changed, so it never held more than the rotation window, about a day or two on
// an active machine. history.db is appended to as the log grows, follows the log across rotation,
// and keeps what rotation deletes, up to a retention period and a size cap.
//
// It exists only after the user opts in: `beacon endpoint traces reindex`, `beacon mcp setup`, or
// the first-run prompt. Until then every trace read scans the JSONL, as it also does whenever the
// store cannot answer a query.
//
// The store must answer the same question the JSONL scan answers. Every event is stored, in log
// order, and projected by the same code (traceEventFromRecord and traceAggregate), so trace
// membership, event numbers, summaries and search results match the JSONL path for the same log.

const (
	// historySchemaVersion is the table layout. A store written by another layout is not read;
	// every query falls back to the JSONL scan until `traces reset` or `traces reindex --rebuild`.
	historySchemaVersion = 1
	// historyDerivationVersion covers everything computed from a stored line: trace keys, event
	// numbers, types, summaries and search text. Bump it when that projection changes; the next
	// catch-up recomputes it from the stored lines, so history survives the change.
	historyDerivationVersion = 3 // 2: trace events carry the event's policy block; 3: and its error

	defaultHistoryRetentionDays = 90
	// maxHistoryRetentionDays is 100 years: a retention this long or longer means nothing expires.
	maxHistoryRetentionDays = 100 * 365
	defaultHistoryMaxBytes      = int64(1) << 30

	// historyMaxLineBytes matches the JSONL scanner's line limit, so a line too long for one path
	// is skipped by the other rather than read by only one of them.
	historyMaxLineBytes = 4 * 1024 * 1024

	// historyLockWait bounds how long a reader waits for another process's catch-up.
	historyLockWait = 30 * time.Second
)

var (
	errHistoryNotEnabled     = errors.New("local history is not set up")
	errTraceStoreUnsupported = errors.New("local history does not support this query")
)

// HistoryStatus is `beacon endpoint traces status`. The first seven fields keep the names the
// traces.db status used.
type HistoryStatus struct {
	Path          string `json:"path"`
	Traces        int    `json:"traces"`
	Events        int    `json:"events"`
	IndexRows     int    `json:"index_rows"`
	SizeBytes     int64  `json:"size_bytes"`
	WALBytes      int64  `json:"wal_bytes,omitempty"`
	IndexedAt     string `json:"indexed_at,omitempty"`
	Enabled       bool   `json:"enabled"`
	Source        string `json:"source,omitempty"`
	OldestEventAt string `json:"oldest_event_at,omitempty"`
	NewestEventAt string `json:"newest_event_at,omitempty"`
	Gaps          int    `json:"gaps,omitempty"`
	GapBytes      int64  `json:"gap_bytes,omitempty"`
	RetentionDays int64  `json:"retention_days,omitempty"`
	MaxBytes      int64  `json:"max_bytes,omitempty"`
}

// HistoryOptions configures EnableHistoryStore. Zero values keep the store's current settings.
type HistoryOptions struct {
	RetentionDays int
	MaxBytes      int64
	// Rebuild recomputes every derived column from the stored lines.
	Rebuild bool
	// Progress, when set, is called while a catch-up reads the log, with bytes read and total.
	Progress func(done, total int64)
}

// TraceStorePath is where the local history lives. The log path is accepted for the callers that
// predate history.db; the history is per user, not per log.
func TraceStorePath(string) string {
	return endpointconfig.HistoryStorePath()
}

// HistoryStoreEnabled reports whether the user has opted in to the local history.
func HistoryStoreEnabled() bool {
	_, err := os.Stat(endpointconfig.HistoryStorePath())
	return err == nil
}

// TraceStoreStatus reports on the local history for one log without creating it.
func TraceStoreStatus(logPath string) (HistoryStatus, error) {
	store, err := openHistoryStore(false)
	if errors.Is(err, errHistoryNotEnabled) {
		return HistoryStatus{Path: endpointconfig.HistoryStorePath()}, nil
	}
	if err != nil {
		return HistoryStatus{}, err
	}
	defer store.close()
	sourceID, err := store.catchUp(logPath, nil, false)
	if err != nil {
		return HistoryStatus{}, err
	}
	return store.status(sourceID)
}

// ReindexTraceStore creates the local history if it does not exist and catches it up with the log.
func ReindexTraceStore(logPath string) error {
	_, err := EnableHistoryStore(logPath, HistoryOptions{})
	return err
}

// EnableHistoryStore creates the local history if needed, applies opts, and catches it up with the
// log. This is the opt-in: nothing else creates the store.
func EnableHistoryStore(logPath string, opts HistoryOptions) (HistoryStatus, error) {
	store, err := openHistoryStore(true)
	if err != nil {
		return HistoryStatus{}, err
	}
	defer store.close()
	if opts.RetentionDays > 0 {
		if err := store.setMeta("retention_days", strconv.Itoa(opts.RetentionDays)); err != nil {
			return HistoryStatus{}, err
		}
	}
	if opts.MaxBytes > 0 {
		if err := store.setMeta("max_bytes", strconv.FormatInt(opts.MaxBytes, 10)); err != nil {
			return HistoryStatus{}, err
		}
	}
	if opts.Rebuild {
		if err := store.setMeta("derivation_version", "0"); err != nil {
			return HistoryStatus{}, err
		}
	}
	removeLegacyTraceStore(logPath)
	sourceID, err := store.catchUp(logPath, opts.Progress, true)
	if err != nil {
		return HistoryStatus{}, err
	}
	return store.status(sourceID)
}

// ResetHistoryStore deletes the local history. The runtime log is untouched.
func ResetHistoryStore() error {
	path := endpointconfig.HistoryStorePath()
	var problems []string
	for _, suffix := range []string{"", "-wal", "-shm", ".lock"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("remove local history: %s", strings.Join(problems, "; "))
	}
	return nil
}

// removeLegacyTraceStore deletes the traces.db cache history.db replaces. It sat beside the log
// directory; a system log's copy is root's and stays until the system install removes it.
func removeLegacyTraceStore(logPath string) {
	if strings.TrimSpace(logPath) == "" {
		return
	}
	dir := filepath.Dir(logPath)
	legacy := filepath.Join(dir, "traces.db")
	if filepath.Base(dir) == "logs" {
		legacy = filepath.Join(filepath.Dir(dir), "traces.db")
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(legacy + suffix)
	}
}

type historyStore struct {
	path string
	db   *sql.DB
}

func openHistoryStore(create bool) (*historyStore, error) {
	path := endpointconfig.HistoryStorePath()
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if !create {
			return nil, errHistoryNotEnabled
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		// Created here rather than by SQLite so the mode is 0600 from the first byte: the store
		// holds the same prompts and command output the log does. SQLite gives the -wal and -shm
		// files the database file's mode.
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil && !os.IsExist(err) {
			return nil, err
		}
		if f != nil {
			_ = f.Close()
		}
	}
	db, err := sql.Open("sqlite", historyURI(path))
	if err != nil {
		return nil, err
	}
	store := &historyStore{path: path, db: db}
	if err := store.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func historyURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file:" + filepath.ToSlash(abs) + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
}

func (s *historyStore) close() {
	_ = s.db.Close()
}

func (s *historyStore) ensureSchema() error {
	var tables int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&tables); err != nil {
		return err
	}
	if tables == 1 {
		version, err := s.meta("schema_version")
		if err != nil {
			return err
		}
		if version != strconv.Itoa(historySchemaVersion) {
			return fmt.Errorf("local history schema %q is not supported by this Beacon version; run `beacon endpoint traces reset` to start over", version)
		}
		if codec, err := s.meta("line_codec"); err != nil {
			return err
		} else if codec != historyLineCodec {
			return fmt.Errorf("local history line encoding %q is not supported by this Beacon version; run `beacon endpoint traces reset` to start over", codec)
		}
		return nil
	}
	// auto_vacuum only takes effect before the first table exists. Incremental mode lets pruning
	// hand pages back to the file system, so the size cap is a real bound on disk use.
	// One connection for the whole setup: auto_vacuum is a pending setting of the connection that
	// runs it, and only takes effect if that same connection creates the first table.
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	stmts := []string{
		`PRAGMA auto_vacuum = INCREMENTAL`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		// A source is one runtime log path. Every query is scoped to the log it was asked about,
		// so `--log-path` and a user-mode/system-mode switch never mix two logs' traces.
		// gaps counts the times the log rotated past the store before it was read: a file that
		// was the live log at one catch-up and gone by the next took the lines written to it in
		// between. gap_bytes is what is known to be lost, a lower bound.
		`CREATE TABLE sources (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL UNIQUE,
			gaps INTEGER NOT NULL DEFAULT 0,
			gap_bytes INTEGER NOT NULL DEFAULT 0
		)`,
		// files is the tail position in every log file seen: identity is the device, inode and
		// first line, so a rotated file is recognized under its new name and a new file that
		// reuses an inode is not mistaken for the old one.
		`CREATE TABLE files (
			source_id INTEGER NOT NULL,
			dev INTEGER NOT NULL,
			ino INTEGER NOT NULL,
			head TEXT NOT NULL,
			offset INTEGER NOT NULL,
			lines INTEGER NOT NULL,
			size INTEGER NOT NULL,
			archive INTEGER NOT NULL,
			PRIMARY KEY (source_id, dev, ino, head)
		)`,
		// One row per log line that parsed as an event, in log order. The row key is the file and
		// byte offset rather than the event ID: two lines can share an ID (a repeated line, or a
		// hook and an OTLP capture of one call) and the JSONL path keeps both.
		`CREATE TABLE events (
			id INTEGER PRIMARY KEY,
			source_id INTEGER NOT NULL,
			file_key TEXT NOT NULL,
			offset INTEGER NOT NULL,
			record_id TEXT NOT NULL,
			line_no INTEGER NOT NULL,
			idless INTEGER NOT NULL,
			trace_key TEXT NOT NULL,
			seq INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			event_id TEXT NOT NULL,
			ts_ns INTEGER NOT NULL,
			ord_ts INTEGER NOT NULL,
			ord_seq INTEGER NOT NULL,
			span_id TEXT NOT NULL DEFAULT '',
			parent_span_id TEXT NOT NULL DEFAULT '',
			otel_trace_id TEXT NOT NULL DEFAULT '',
			span_name TEXT NOT NULL DEFAULT '',
			hay TEXT NOT NULL,
			line BLOB NOT NULL,
			UNIQUE (source_id, file_key, offset)
		)`,
		`CREATE INDEX events_by_trace ON events (source_id, trace_key, seq)`,
		// Events the writer left without an ID are named by their place in the log, which moves
		// when the file rotates; this finds them.
		`CREATE INDEX events_idless ON events (source_id, file_key) WHERE idless = 1`,
		// traces keeps each trace's running aggregate, so a catch-up updates a summary with the new
		// events alone instead of rereading the trace.
		`CREATE TABLE traces (
			id INTEGER PRIMARY KEY,
			source_id INTEGER NOT NULL,
			trace_key TEXT NOT NULL,
			started_ns INTEGER NOT NULL,
			updated_ns INTEGER NOT NULL,
			retain_ns INTEGER NOT NULL,
			state TEXT NOT NULL,
			visibility TEXT NOT NULL,
			event_count INTEGER NOT NULL,
			aggregate TEXT NOT NULL,
			hay TEXT NOT NULL,
			UNIQUE (source_id, trace_key)
		)`,
		`CREATE INDEX traces_by_updated ON traces (source_id, updated_ns DESC, trace_key)`,
		// A trace's running aggregate every historyCheckpointEvery events, so a late event refolds
		// the summary from near where it lands.
		`CREATE TABLE trace_checkpoints (
			source_id INTEGER NOT NULL,
			trace_key TEXT NOT NULL,
			seq INTEGER NOT NULL,
			state TEXT NOT NULL,
			PRIMARY KEY (source_id, trace_key, seq)
		)`,
		`CREATE INDEX traces_by_retention ON traces (retain_ns)`,
		// Search runs over the same lowercased haystacks the JSONL path matches. The trigram
		// tokenizer makes a quoted term match exactly the rows containing it as a substring, so a
		// term of three or more characters needs no second check. case_sensitive 1 because the
		// text is lowercased before it is indexed, by the same strings.ToLower the JSONL matcher
		// uses, rather than by SQLite's own folding.
		`CREATE VIRTUAL TABLE event_fts USING fts5(hay, content='events', content_rowid='id', tokenize='trigram case_sensitive 1')`,
		`CREATE VIRTUAL TABLE trace_fts USING fts5(hay, content='traces', content_rowid='id', tokenize='trigram case_sensitive 1')`,
	}
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
			return err
		}
	}
	// The WAL switch in the connection string has already written the database header by now,
	// which fixes auto_vacuum at none until a VACUUM rewrites the file. The store is empty, so that
	// costs nothing.
	if _, err := conn.ExecContext(context.Background(), `VACUUM`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for key, value := range map[string]string{
		"schema_version":     strconv.Itoa(historySchemaVersion),
		"derivation_version": strconv.Itoa(historyDerivationVersion),
		"retention_days":     strconv.Itoa(defaultHistoryRetentionDays),
		"max_bytes":          strconv.FormatInt(defaultHistoryMaxBytes, 10),
		"line_codec":         historyLineCodec,
		"created_at":         now,
	} {
		if err := s.setMeta(key, value); err != nil {
			return err
		}
	}
	return nil
}

func (s *historyStore) meta(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *historyStore) setMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *historyStore) metaInt(key string, fallback int64) int64 {
	value, err := s.meta(key)
	if err != nil {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// sourceID returns the source row for a log path, creating it when asked.
func (s *historyStore) sourceID(q queryer, logPath string, create bool) (int64, error) {
	key := traceSourceKey(logPath)
	var id int64
	err := q.QueryRow(`SELECT id FROM sources WHERE path = ?`, key).Scan(&id)
	if err == nil || err != sql.ErrNoRows || !create {
		return id, err
	}
	result, err := q.Exec(`INSERT INTO sources (path) VALUES (?)`, key)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// traceSourceKey normalizes a log path so the same log reached by a relative and an absolute path
// is one source rather than two.
func traceSourceKey(logPath string) string {
	abs, err := filepath.Abs(logPath)
	if err != nil {
		return logPath
	}
	return filepath.Clean(abs)
}

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

func (s *historyStore) status(sourceID int64) (HistoryStatus, error) {
	status := HistoryStatus{
		Path:          s.path,
		Enabled:       true,
		SizeBytes:     fileSize(s.path),
		WALBytes:      fileSize(s.path + "-wal"),
		RetentionDays: s.metaInt("retention_days", defaultHistoryRetentionDays),
		MaxBytes:      s.metaInt("max_bytes", defaultHistoryMaxBytes),
	}
	var oldest, newest sql.NullInt64
	if err := s.db.QueryRow(`SELECT path, gaps, gap_bytes FROM sources WHERE id = ?`, sourceID).Scan(&status.Source, &status.Gaps, &status.GapBytes); err != nil {
		return HistoryStatus{}, err
	}
	// From the traces table alone: counting the events table would read every stored event.
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(event_count), 0), MIN(NULLIF(started_ns, ?)), MAX(NULLIF(updated_ns, ?))
		FROM traces WHERE source_id = ?`, int64(math.MinInt64), int64(math.MinInt64), sourceID).Scan(&status.Traces, &status.Events, &oldest, &newest); err != nil {
		return HistoryStatus{}, err
	}
	// Rows searchable, counted the way traces.db counted them: one per trace and one per event.
	status.IndexRows = status.Traces + status.Events
	if oldest.Valid {
		status.OldestEventAt = time.Unix(0, oldest.Int64).UTC().Format(time.RFC3339Nano)
	}
	if newest.Valid {
		status.NewestEventAt = time.Unix(0, newest.Int64).UTC().Format(time.RFC3339Nano)
	}
	indexedAt, err := s.meta("indexed_at")
	if err != nil {
		return HistoryStatus{}, err
	}
	status.IndexedAt = indexedAt
	return status, nil
}

// lockExclusive serializes catch-ups across processes: the CLI, the dashboard and the MCP server can
// all hold the store open at once, and only one may append at a time. Readers never take it.
func (s *historyStore) lockExclusive() (func(), error) {
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(historyLockWait)
	for {
		held, err := filelock.TryExclusive(f)
		if err == nil {
			return func() {
				_ = held.Release()
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("local history is busy: another Beacon process has been updating it for %s", historyLockWait)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// timeKey turns a trace or event timestamp into an integer that sorts the way
// sortTraceSummaries orders the parsed times: later first, and an unparseable time after every
// valid one.
func timeKey(value string) int64 {
	parsed, err := schema.ParseTimestamp(value)
	if err != nil || parsed.IsZero() {
		return math.MinInt64
	}
	if parsed.Year() < 1678 {
		return math.MinInt64 + 1
	}
	if parsed.Year() > 2261 {
		return math.MaxInt64
	}
	return parsed.UnixNano()
}
