package dashboard

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

// logFile is one file of a runtime log, opened during a snapshot.
type logFile struct {
	path    string
	archive int
	f       *os.File
	dev     uint64
	ino     uint64
	// head identifies the file's content: a hash of its first line. Together with the device and
	// inode it tells a rotated file apart from a new file that happens to reuse an inode.
	head string
	size int64
}

func (lf logFile) key() string {
	return fmt.Sprintf("%d:%d:%s", lf.dev, lf.ino, lf.head)
}

type fileCheckpoint struct {
	// dev and ino are as the files table stores them, so the row can be addressed again.
	dev     int64
	ino     int64
	head    string
	offset  int64
	lines   int64
	size    int64
	archive int
}

// snapshotLogFiles opens every file of a runtime log while holding the writers' lock shared, so no
// rotation can land between listing the files and opening them. The files are read after the lock
// is released, each up to the size it had here.
func snapshotLogFiles(logPath string) ([]logFile, error) {
	unlock := lockLogShared(logPath)
	defer unlock()
	var files []logFile
	for _, source := range eventSources(logPath) {
		f, err := openLogFile(source.path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			closeLogFiles(files)
			return nil, err
		}
		lf := logFile{path: source.path, archive: source.archive, f: f}
		info, err := f.Stat()
		if err == nil {
			lf.size = info.Size()
			lf.dev, lf.ino, err = logFileIdentity(f)
		}
		if err == nil {
			lf.head, err = readLogHead(f, lf.size)
		}
		if err != nil {
			_ = f.Close()
			closeLogFiles(files)
			return nil, err
		}
		files = append(files, lf)
	}
	// Oldest first: the highest archive number, down to the live file.
	sort.SliceStable(files, func(i, j int) bool {
		ai, aj := files[i].archive, files[j].archive
		if ai == 0 || aj == 0 {
			return aj == 0 && ai != 0
		}
		return ai > aj
	})
	return files, nil
}

func closeLogFiles(files []logFile) {
	for _, lf := range files {
		_ = lf.f.Close()
	}
}

// lockLogShared takes the lock Beacon's log writers hold while they append and rotate. A log
// without a lock file, or one this user cannot open, is read without it: a rotation racing the
// snapshot then costs at most a missed file, which the next catch-up picks up.
func lockLogShared(logPath string) func() {
	f, err := os.Open(logPath + ".lock")
	if err != nil {
		return func() {}
	}
	held, err := filelock.Shared(f)
	if err != nil {
		_ = f.Close()
		return func() {}
	}
	return func() {
		_ = held.Release()
		_ = f.Close()
	}
}

// readLogHead hashes the file's first complete line. An empty file, or one whose first line is
// still being written, has no head yet and is skipped until it does.
func readLogHead(f *os.File, size int64) (string, error) {
	if size == 0 {
		return "", nil
	}
	limit := size
	if limit > historyMaxLineBytes+1 {
		limit = historyMaxLineBytes + 1
	}
	reader := bufio.NewReader(io.NewSectionReader(f, 0, limit))
	line, err := reader.ReadBytes('\n')
	if err == io.EOF {
		if int64(len(line)) > historyMaxLineBytes {
			// A first line longer than any event is identified by its prefix alone.
			sum := sha256.Sum256(line)
			return hex.EncodeToString(sum[:16]), nil
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:16]), nil
}

func (s *historyStore) loadCheckpoints(q queryer, sourceID int64) (map[string]fileCheckpoint, error) {
	rows, err := q.Query(`SELECT dev, ino, head, offset, lines, size, archive FROM files WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]fileCheckpoint{}
	for rows.Next() {
		var cp fileCheckpoint
		if err := rows.Scan(&cp.dev, &cp.ino, &cp.head, &cp.offset, &cp.lines, &cp.size, &cp.archive); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%d:%d:%s", uint64(cp.dev), uint64(cp.ino), cp.head)] = cp
	}
	return out, rows.Err()
}

// needsCatchUp reports whether the log holds anything the store has not read, or whether a file the
// store was reading has gone.
func needsCatchUp(files []logFile, checkpoints map[string]fileCheckpoint) bool {
	seen := map[string]bool{}
	for _, lf := range files {
		if lf.head == "" {
			continue
		}
		cp, ok := checkpoints[lf.key()]
		if !ok || cp.offset != lf.size {
			return true
		}
		seen[lf.key()] = true
	}
	for key := range checkpoints {
		if !seen[key] {
			return true
		}
	}
	return false
}

// catchUp appends whatever the log gained since the last catch-up, prunes what is past retention or
// over the size cap, and returns the log's source ID. With nothing to do it is a stat of each log
// file and two small queries. force runs a full pass regardless, for a settings change.
func (s *historyStore) catchUp(logPath string, progress func(done, total int64), force bool) (int64, error) {
	files, err := snapshotLogFiles(logPath)
	if err != nil {
		return 0, err
	}
	defer closeLogFiles(files)

	derivationCurrent := s.metaInt("derivation_version", 0) == historyDerivationVersion
	sourceID, err := s.sourceID(s.db, logPath, false)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if sourceID != 0 && derivationCurrent && !force {
		checkpoints, err := s.loadCheckpoints(s.db, sourceID)
		if err != nil {
			return 0, err
		}
		expired, err := s.hasExpiredTraces(time.Now())
		if err != nil {
			return 0, err
		}
		if !needsCatchUp(files, checkpoints) && !expired {
			return sourceID, nil
		}
	}

	unlock, err := s.lockExclusive()
	if err != nil {
		return 0, err
	}
	defer unlock()
	retentionDays := s.metaInt("retention_days", defaultHistoryRetentionDays)
	maxBytes := s.metaInt("max_bytes", defaultHistoryMaxBytes)
	// Re-read under the lock: another process may have caught up, or rebuilt, while this one
	// waited for it.
	derivationCurrent = s.metaInt("derivation_version", 0) == historyDerivationVersion

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if !derivationCurrent {
		if err := rebuildHistoryDerived(tx); err != nil {
			return 0, fmt.Errorf("rebuild local history: %w", err)
		}
	}
	sourceID, err = s.sourceID(tx, logPath, true)
	if err != nil {
		return 0, err
	}
	checkpoints, err := s.loadCheckpoints(tx, sourceID)
	if err != nil {
		return 0, err
	}

	pass, err := newHistoryPass(tx, sourceID)
	if err != nil {
		return 0, err
	}
	defer pass.close()

	var total, done int64
	for _, lf := range files {
		if cp, ok := checkpoints[lf.key()]; ok && cp.offset <= lf.size {
			total += lf.size - cp.offset
		} else {
			total += lf.size
		}
	}
	seen := map[string]bool{}
	for _, lf := range files {
		if lf.head == "" {
			continue
		}
		key := lf.key()
		seen[key] = true
		cp := checkpoints[key]
		if cp.archive != lf.archive && cp.offset > 0 && cp.offset <= lf.size {
			if err := relabelIDlessEvents(tx, sourceID, lf); err != nil {
				return 0, err
			}
		}
		if cp.offset > lf.size {
			// Shorter than what was read from it: not the file the checkpoint describes.
			cp = fileCheckpoint{}
		}
		start := cp.offset
		offset, lines, err := pass.ingestFile(lf, cp.offset, cp.lines, func(n int64) {
			if progress != nil {
				progress(done+n, total)
			}
		})
		if err != nil {
			return 0, err
		}
		done += offset - start
		if _, err := tx.Exec(`INSERT INTO files (source_id, dev, ino, head, offset, lines, size, archive) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (source_id, dev, ino, head) DO UPDATE SET offset = excluded.offset, lines = excluded.lines, size = excluded.size, archive = excluded.archive`,
			sourceID, int64(lf.dev), int64(lf.ino), lf.head, offset, lines, lf.size, lf.archive); err != nil {
			return 0, err
		}
	}
	// A file leaves the log when rotation deletes the oldest archive. An archive read to its end
	// leaving is normal. A file that was the live log at the last catch-up and is gone now went
	// through every archive slot in between, so the log rotated past the store and whatever was
	// written to that file after it was read, and to any file created and deleted since, is lost.
	// Its size is unknown, so it is counted as a gap; unread bytes known at the last catch-up are
	// added to gap_bytes as a lower bound.
	var gaps, gap int64
	for key, cp := range checkpoints {
		if seen[key] {
			continue
		}
		if cp.size > cp.offset {
			gap += cp.size - cp.offset
		}
		if cp.archive == 0 || cp.size > cp.offset {
			gaps++
		}
		if _, err := tx.Exec(`DELETE FROM files WHERE source_id = ? AND dev = ? AND ino = ? AND head = ?`, sourceID, cp.dev, cp.ino, cp.head); err != nil {
			return 0, err
		}
	}
	if gaps > 0 {
		if _, err := tx.Exec(`UPDATE sources SET gaps = gaps + ?, gap_bytes = gap_bytes + ? WHERE id = ?`, gaps, gap, sourceID); err != nil {
			return 0, err
		}
	}
	if err := pass.flush(); err != nil {
		return 0, err
	}
	pruned, err := pruneHistory(tx, time.Now(), retentionDays, maxBytes)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('indexed_at', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if pruned {
		// Hand the freed pages back to the file system, so the cap bounds the file on disk.
		_, _ = s.db.Exec(`PRAGMA incremental_vacuum`)
	}
	if pruned || done > 4<<20 {
		// A large catch-up leaves a write-ahead log as large as what it wrote, and the database
		// file only shrinks after a vacuum once the log is folded into it.
		_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	return sourceID, nil
}

// historyCheckpointEvery is how often, in events, a trace's running aggregate is saved. An event
// that arrives after later ones -- an OpenTelemetry batch lands seconds after the hook events it
// precedes -- belongs earlier in its trace, and the summary is refolded from the last checkpoint
// before it rather than from the start of a trace that may hold tens of thousands of events.
const historyCheckpointEvery = 256

// historyOrder is where an event sits in its trace. It is the order SortRecordsAppendOrder gives
// the JSONL path: timestamp, then unsequenced before sequenced, then sequence, then position in the
// log, which is the row ID because rows are stored in log order.
type historyOrder struct {
	ts  int64
	seq uint64
	id  int64
}

func (a historyOrder) less(b historyOrder) bool {
	if a.ts != b.ts {
		return a.ts < b.ts
	}
	if (a.seq != 0) != (b.seq != 0) {
		return a.seq == 0
	}
	if a.seq != b.seq {
		return a.seq < b.seq
	}
	return a.id < b.id
}

// orderTime is the event's timestamp as SortRecordsAppendOrder compares it: parsed with the same
// function, and a timestamp that does not parse sorting first, as the zero time does.
func orderTime(value string) int64 {
	parsed, err := asymptoteobserve.ParseTimestamp(value)
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

// historyEvent is an event on its way into a trace's fold.
type historyEvent struct {
	id       int64
	seq      int
	order    historyOrder
	recordID string
	event    schema.Event
}

// historyPass is one catch-up's worth of appends. New events are stored as they are read, and
// each trace's summary is brought up to date when the pass flushes.
type historyPass struct {
	tx          *sql.Tx
	sourceID    int64
	pending     map[string][]*historyEvent
	pendingN    int
	insertEvent *sql.Stmt
	insertFTS   *sql.Stmt
	updateSeq   *sql.Stmt
}

// historyFlushEvery bounds how many new events a pass holds in memory before folding them in.
const historyFlushEvery = 4096

func newHistoryPass(tx *sql.Tx, sourceID int64) (*historyPass, error) {
	insertEvent, err := tx.Prepare(`INSERT INTO events (source_id, file_key, offset, record_id, line_no, idless, trace_key, seq, event_type, event_id, ts_ns,
			ord_ts, ord_seq, span_id, parent_span_id, otel_trace_id, span_name, hay, line)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source_id, file_key, offset) DO NOTHING`)
	if err != nil {
		return nil, err
	}
	insertFTS, err := tx.Prepare(`INSERT INTO event_fts (rowid, hay) VALUES (?, ?)`)
	if err != nil {
		_ = insertEvent.Close()
		return nil, err
	}
	updateSeq, err := tx.Prepare(`UPDATE events SET seq = ? WHERE id = ?`)
	if err != nil {
		_ = insertEvent.Close()
		_ = insertFTS.Close()
		return nil, err
	}
	return &historyPass{
		tx:          tx,
		sourceID:    sourceID,
		pending:     map[string][]*historyEvent{},
		insertEvent: insertEvent,
		insertFTS:   insertFTS,
		updateSeq:   updateSeq,
	}, nil
}

func (p *historyPass) close() {
	_ = p.insertEvent.Close()
	_ = p.insertFTS.Close()
	_ = p.updateSeq.Close()
}

// ingestFile reads complete lines from offset to the file's snapshot size. A final line without a
// newline is left for the next catch-up; Beacon's writers append whole lines, so that is a line
// another process is still writing.
func (p *historyPass) ingestFile(lf logFile, offset, lines int64, progress func(int64)) (int64, int64, error) {
	reader := bufio.NewReaderSize(io.NewSectionReader(lf.f, offset, lf.size-offset), 256*1024)
	source := eventSource{path: lf.path, archive: lf.archive}
	key := lf.key()
	pos := offset
	var read int64
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, err
		}
		start := pos
		pos += int64(len(line))
		read += int64(len(line))
		lines++
		if progress != nil && lines%2000 == 0 {
			progress(read)
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || len(trimmed) > historyMaxLineBytes {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal(trimmed, &event); err != nil {
			continue
		}
		normalizeDashboardEvent(&event)
		record := EventRecord{ID: source.lineID(int(lines)), Line: int(lines), Event: event, Raw: trimmed}
		if err := p.add(key, start, record); err != nil {
			return 0, 0, err
		}
	}
	if progress != nil {
		progress(read)
	}
	return pos, lines, nil
}

// historyRow is what the store derives from one event, apart from its place in the trace.
type historyRow struct {
	traceKey     string
	eventType    string
	eventID      string
	tsNS         int64
	order        historyOrder
	spanID       string
	parentSpanID string
	otelTraceID  string
	spanName     string
	hay          string
}

// deriveRow computes an event's trace and every derived column the way the JSONL path would. The
// event number is not among them: it is the event's place in its trace, set when the trace folds.
func deriveRow(record EventRecord) historyRow {
	event := record.Event
	te := traceEventFromRecord(record, 0)
	row := historyRow{
		traceKey:  traceProjectionID(event),
		eventType: te.Type,
		eventID:   te.ID,
		tsNS:      timeKey(event.Timestamp),
		order:     historyOrder{ts: orderTime(event.Timestamp), seq: event.Sequence},
		hay:       strings.ToLower(traceEventHaystack(te)),
	}
	if te.Trace != nil && te.Trace.SpanID != "" {
		row.spanID = te.Trace.SpanID
		row.parentSpanID = te.Trace.ParentSpanID
		row.otelTraceID = te.Trace.ID
		row.spanName = firstNonEmpty(te.Summary, te.Title, te.Action)
	}
	return row
}

func (p *historyPass) add(fileKey string, offset int64, record EventRecord) error {
	row := deriveRow(record)
	stored, err := storedLine(record.Raw)
	if err != nil {
		return err
	}
	idless := 0
	if record.Event.Event.ID == "" {
		idless = 1
	}
	result, err := p.insertEvent.Exec(p.sourceID, fileKey, offset, record.ID, record.Line, idless, row.traceKey, row.eventType, row.eventID, row.tsNS,
		row.order.ts, int64(row.order.seq), row.spanID, row.parentSpanID, row.otelTraceID, row.spanName, row.hay, compressLine(stored))
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		// Already stored: a checkpoint lost after a crash re-reads lines the store has.
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := p.insertFTS.Exec(id, row.hay); err != nil {
		return err
	}
	row.order.id = id
	p.pending[row.traceKey] = append(p.pending[row.traceKey], &historyEvent{id: id, order: row.order, recordID: record.ID, event: record.Event})
	p.pendingN++
	if p.pendingN >= historyFlushEvery {
		return p.flush()
	}
	return nil
}

// flush folds every pending event into its trace.
func (p *historyPass) flush() error {
	keys := make([]string, 0, len(p.pending))
	for key := range p.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := p.foldTrace(key, p.pending[key]); err != nil {
			return err
		}
	}
	p.pending = map[string][]*historyEvent{}
	p.pendingN = 0
	return nil
}

type historyTraceState struct {
	id        int64
	exists    bool
	count     int
	aggregate string
	hay       string
}

func (p *historyPass) traceState(traceKey string) (historyTraceState, error) {
	var state historyTraceState
	err := p.tx.QueryRow(`SELECT id, event_count, aggregate, hay FROM traces WHERE source_id = ? AND trace_key = ?`, p.sourceID, traceKey).
		Scan(&state.id, &state.count, &state.aggregate, &state.hay)
	if err == sql.ErrNoRows {
		return historyTraceState{}, nil
	}
	state.exists = err == nil
	return state, err
}

// foldTrace places new events in their trace and brings the trace's summary up to date. New events
// that all sort after the trace's last event are folded onto its stored aggregate. Otherwise the
// trace is refolded from the last checkpoint before the earliest of them, renumbering the events
// after it.
func (p *historyPass) foldTrace(traceKey string, added []*historyEvent) error {
	sort.Slice(added, func(i, j int) bool { return added[i].order.less(added[j].order) })
	state, err := p.traceState(traceKey)
	if err != nil {
		return err
	}
	var agg *traceAggregate
	base := 0
	var existing []*historyEvent
	if state.exists {
		position, err := p.insertionPoint(traceKey, added[0].order)
		if err != nil {
			return err
		}
		if position > state.count {
			if agg, err = decodeTraceAggregate(state.aggregate); err != nil {
				return err
			}
			base = state.count
		} else {
			var checkpoint string
			err := p.tx.QueryRow(`SELECT seq, state FROM trace_checkpoints WHERE source_id = ? AND trace_key = ? AND seq < ? ORDER BY seq DESC LIMIT 1`,
				p.sourceID, traceKey, position).Scan(&base, &checkpoint)
			switch {
			case err == sql.ErrNoRows:
				base = 0
			case err != nil:
				return err
			default:
				if agg, err = decodeTraceAggregate(checkpoint); err != nil {
					return err
				}
			}
			if _, err := p.tx.Exec(`DELETE FROM trace_checkpoints WHERE source_id = ? AND trace_key = ? AND seq > ?`, p.sourceID, traceKey, base); err != nil {
				return err
			}
			if existing, err = p.storedEvents(traceKey, base); err != nil {
				return err
			}
		}
	}
	folder := &traceFolder{pass: p, traceKey: traceKey, agg: agg, seq: base}
	i, j := 0, 0
	for i < len(existing) || j < len(added) {
		var next *historyEvent
		if j >= len(added) || (i < len(existing) && existing[i].order.less(added[j].order)) {
			next = existing[i]
			i++
		} else {
			next = added[j]
			j++
		}
		if err := folder.fold(next); err != nil {
			return err
		}
	}
	return folder.finish(state)
}

// insertionPoint is where an event with the given order lands among the trace's stored events:
// one past the last stored event that sorts before it. Late events land near the end, so the scan
// runs backwards from there.
func (p *historyPass) insertionPoint(traceKey string, order historyOrder) (int, error) {
	rows, err := p.tx.Query(`SELECT seq, ord_ts, ord_seq, id FROM events WHERE source_id = ? AND trace_key = ? AND seq > 0 ORDER BY seq DESC`, p.sourceID, traceKey)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var stored historyOrder
		var ordSeq int64
		if err := rows.Scan(&seq, &stored.ts, &ordSeq, &stored.id); err != nil {
			return 0, err
		}
		stored.seq = uint64(ordSeq)
		if stored.less(order) {
			return seq + 1, rows.Err()
		}
	}
	return 1, rows.Err()
}

// storedEvents loads the trace's events after position base, in order, to be refolded.
func (p *historyPass) storedEvents(traceKey string, base int) ([]*historyEvent, error) {
	rows, err := p.tx.Query(`SELECT id, seq, ord_ts, ord_seq, record_id, line FROM events WHERE source_id = ? AND trace_key = ? AND seq > ? ORDER BY seq`,
		p.sourceID, traceKey, base)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*historyEvent
	for rows.Next() {
		ev := &historyEvent{}
		var ordSeq int64
		var line []byte
		if err := rows.Scan(&ev.id, &ev.seq, &ev.order.ts, &ordSeq, &ev.recordID, &line); err != nil {
			return nil, err
		}
		ev.order.seq = uint64(ordSeq)
		ev.order.id = ev.id
		if ev.event, err = decodeStoredEvent(line); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func decodeStoredEvent(compressed []byte) (schema.Event, error) {
	raw, err := decompressLine(compressed)
	if err != nil {
		return schema.Event{}, err
	}
	var event schema.Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return schema.Event{}, err
	}
	normalizeDashboardEvent(&event)
	return event, nil
}

// traceFolder runs readTraceAggregates' fold over one trace's events in order, numbering them,
// saving a checkpoint every historyCheckpointEvery events, and writing the summary at the end.
type traceFolder struct {
	pass     *historyPass
	traceKey string
	agg      *traceAggregate
	seq      int
}

func (f *traceFolder) fold(ev *historyEvent) error {
	f.seq++
	if f.agg == nil {
		f.agg = newTraceAggregate(f.traceKey, ev.event)
	}
	te := traceEventFromRecord(EventRecord{ID: ev.recordID, Event: ev.event}, f.seq)
	f.agg.update(ev.event, te)
	if ev.seq != f.seq {
		if _, err := f.pass.updateSeq.Exec(f.seq, ev.id); err != nil {
			return err
		}
		ev.seq = f.seq
	}
	if f.seq%historyCheckpointEvery == 0 {
		state, err := json.Marshal(f.agg.state())
		if err != nil {
			return err
		}
		if _, err := f.pass.tx.Exec(`INSERT INTO trace_checkpoints (source_id, trace_key, seq, state) VALUES (?, ?, ?, ?)
			ON CONFLICT (source_id, trace_key, seq) DO UPDATE SET state = excluded.state`, f.pass.sourceID, f.traceKey, f.seq, string(state)); err != nil {
			return err
		}
	}
	return nil
}

func (f *traceFolder) finish(previous historyTraceState) error {
	p := f.pass
	summary := f.agg.finishedSummary()
	hay := strings.ToLower(traceSummaryHaystack(summary))
	state, err := json.Marshal(f.agg.state())
	if err != nil {
		return err
	}
	updated := timeKey(summary.UpdatedAt)
	retain := updated
	if retain <= math.MinInt64+1 {
		retain = time.Now().UnixNano()
	}
	if previous.exists {
		if _, err := p.tx.Exec(`INSERT INTO trace_fts (trace_fts, rowid, hay) VALUES ('delete', ?, ?)`, previous.id, previous.hay); err != nil {
			return err
		}
		if _, err := p.tx.Exec(`UPDATE traces SET started_ns = ?, updated_ns = ?, retain_ns = ?, state = ?, visibility = ?, event_count = ?, aggregate = ?, hay = ? WHERE id = ?`,
			timeKey(summary.StartedAt), updated, retain, summary.Sharing.State, summary.Sharing.Visibility, f.seq, string(state), hay, previous.id); err != nil {
			return err
		}
		_, err := p.tx.Exec(`INSERT INTO trace_fts (rowid, hay) VALUES (?, ?)`, previous.id, hay)
		return err
	}
	result, err := p.tx.Exec(`INSERT INTO traces (source_id, trace_key, started_ns, updated_ns, retain_ns, state, visibility, event_count, aggregate, hay) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.sourceID, f.traceKey, timeKey(summary.StartedAt), updated, retain, summary.Sharing.State, summary.Sharing.Visibility, f.seq, string(state), hay)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	_, err = p.tx.Exec(`INSERT INTO trace_fts (rowid, hay) VALUES (?, ?)`, id, hay)
	return err
}

// traceAggregateState is a trace's running aggregate between catch-ups: the summary before finish()
// and the sets finish() reads. finish() runs on a copy each time a summary is read, so it never
// feeds back into the running state.
type traceAggregateState struct {
	Summary       TraceSummaryV1 `json:"summary"`
	Models        []string       `json:"models,omitempty"`
	Methods       []string       `json:"methods,omitempty"`
	TitlePriority int            `json:"title_priority"`
}

func (a *traceAggregate) state() traceAggregateState {
	return traceAggregateState{
		Summary:       a.summary,
		Models:        sortedKeys(a.models),
		Methods:       sortedKeys(a.methods),
		TitlePriority: a.titlePriority,
	}
}

func decodeTraceAggregate(raw string) (*traceAggregate, error) {
	var state traceAggregateState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	agg := &traceAggregate{
		summary:       state.Summary,
		spans:         map[string]*TraceSpanV1{},
		models:        map[string]bool{},
		methods:       map[string]bool{},
		titlePriority: state.TitlePriority,
	}
	for _, model := range state.Models {
		agg.models[model] = true
	}
	for _, method := range state.Methods {
		agg.methods[method] = true
	}
	return agg, nil
}

func (a *traceAggregate) finishedSummary() TraceSummaryV1 {
	copied := *a
	copied.finish()
	return copied.summary
}

func finishedSummaryFromState(raw string) (TraceSummaryV1, error) {
	agg, err := decodeTraceAggregate(raw)
	if err != nil {
		return TraceSummaryV1{}, err
	}
	return agg.finishedSummary(), nil
}

// rebuildHistoryDerived recomputes every derived column, the search index and the summaries from
// the stored lines, for a store written by an older projection.
func rebuildHistoryDerived(tx *sql.Tx) error {
	for _, stmt := range []string{
		`INSERT INTO event_fts (event_fts) VALUES ('delete-all')`,
		`INSERT INTO trace_fts (trace_fts) VALUES ('delete-all')`,
		`DELETE FROM traces`,
		`DELETE FROM trace_checkpoints`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	update, err := tx.Prepare(`UPDATE events SET trace_key = ?, seq = 0, event_type = ?, event_id = ?, ts_ns = ?, ord_ts = ?, ord_seq = ?,
		span_id = ?, parent_span_id = ?, otel_trace_id = ?, span_name = ?, hay = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer update.Close()
	insertFTS, err := tx.Prepare(`INSERT INTO event_fts (rowid, hay) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer insertFTS.Close()
	// First every event's derived columns, which may move it to another trace.
	var last int64
	for {
		rows, err := tx.Query(`SELECT id, record_id, line FROM events WHERE id > ? ORDER BY id LIMIT 2000`, last)
		if err != nil {
			return err
		}
		type stored struct {
			id       int64
			recordID string
			line     []byte
		}
		var batch []stored
		for rows.Next() {
			var row stored
			if err := rows.Scan(&row.id, &row.recordID, &row.line); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, row)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			last = row.id
			event, err := decodeStoredEvent(row.line)
			if err != nil {
				return err
			}
			derived := deriveRow(EventRecord{ID: row.recordID, Event: event})
			if _, err := update.Exec(derived.traceKey, derived.eventType, derived.eventID, derived.tsNS, derived.order.ts, int64(derived.order.seq),
				derived.spanID, derived.parentSpanID, derived.otelTraceID, derived.spanName, derived.hay, row.id); err != nil {
				return err
			}
			if _, err := insertFTS.Exec(row.id, derived.hay); err != nil {
				return err
			}
		}
	}
	// Then every trace folded from its first event.
	rows, err := tx.Query(`SELECT DISTINCT source_id, trace_key FROM events ORDER BY source_id, trace_key`)
	if err != nil {
		return err
	}
	type traceRef struct {
		sourceID int64
		traceKey string
	}
	var traces []traceRef
	for rows.Next() {
		var ref traceRef
		if err := rows.Scan(&ref.sourceID, &ref.traceKey); err != nil {
			rows.Close()
			return err
		}
		traces = append(traces, ref)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, ref := range traces {
		pass, err := newHistoryPass(tx, ref.sourceID)
		if err != nil {
			return err
		}
		events, err := pass.storedEventsInOrder(ref.traceKey)
		if err == nil && len(events) > 0 {
			folder := &traceFolder{pass: pass, traceKey: ref.traceKey}
			for _, ev := range events {
				if err = folder.fold(ev); err != nil {
					break
				}
			}
			if err == nil {
				err = folder.finish(historyTraceState{})
			}
		}
		pass.close()
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO meta (key, value) VALUES ('derivation_version', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		strconv.Itoa(historyDerivationVersion))
	return err
}

// storedEventsInOrder loads every event of a trace in trace order, for a rebuild.
func (p *historyPass) storedEventsInOrder(traceKey string) ([]*historyEvent, error) {
	rows, err := p.tx.Query(`SELECT id, ord_ts, ord_seq, record_id, line FROM events WHERE source_id = ? AND trace_key = ?`, p.sourceID, traceKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*historyEvent
	for rows.Next() {
		ev := &historyEvent{}
		var ordSeq int64
		var line []byte
		if err := rows.Scan(&ev.id, &ev.order.ts, &ordSeq, &ev.recordID, &line); err != nil {
			return nil, err
		}
		ev.order.seq = uint64(ordSeq)
		ev.order.id = ev.id
		if ev.event, err = decodeStoredEvent(line); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].order.less(out[j].order) })
	return out, nil
}

// pruneHistory drops whole traces: first those whose last event is older than the retention
// period, then the oldest until the store fits its size cap. A trace is never cut in part, so the
// event numbers of what remains do not move.
func pruneHistory(tx *sql.Tx, now time.Time, retentionDays, maxBytes int64) (bool, error) {
	pruned := false
	cutoff := retentionCutoff(now, retentionDays)
	expired, err := historyTraceRows(tx, `SELECT id, source_id, trace_key FROM traces WHERE retain_ns < ? ORDER BY retain_ns`, cutoff)
	if err != nil {
		return false, err
	}
	for _, row := range expired {
		if err := deleteHistoryTrace(tx, row); err != nil {
			return false, err
		}
		pruned = true
	}
	// Over the cap, drop the oldest traces until the store is back to four fifths of it, so pruning
	// runs once per fifth of the cap rather than on every catch-up. Deleted rows leave markers in
	// the search index until its segments merge, so the index is merged once after the deletes;
	// the freed pages go back to the file system when the caller vacuums after commit.
	for attempt := 0; attempt < 4; attempt++ {
		used, err := historyUsedBytes(tx)
		if err != nil {
			return false, err
		}
		if used <= maxBytes {
			return pruned, nil
		}
		var totalEvents int64
		if err := tx.QueryRow(`SELECT COALESCE(SUM(event_count), 0) FROM traces`).Scan(&totalEvents); err != nil {
			return false, err
		}
		if totalEvents == 0 {
			return pruned, nil
		}
		// Events are most of the store, so a trace's share of the events estimates its share of the
		// bytes.
		excess := used - maxBytes/5*4
		eventsToFree := int64(math.Ceil(float64(excess) / float64(used) * float64(totalEvents)))
		oldest, err := historyOldestTraces(tx, eventsToFree)
		if err != nil {
			return false, err
		}
		if len(oldest) == 0 {
			return pruned, nil
		}
		for _, row := range oldest {
			if err := deleteHistoryTrace(tx, row); err != nil {
				return false, err
			}
		}
		pruned = true
		for _, stmt := range []string{
			`INSERT INTO event_fts (event_fts) VALUES ('optimize')`,
			`INSERT INTO trace_fts (trace_fts) VALUES ('optimize')`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return false, err
			}
		}
	}
	return pruned, nil
}

// historyOldestTraces returns the oldest traces holding at least events events between them.
func historyOldestTraces(tx *sql.Tx, events int64) ([]historyTraceRow, error) {
	rows, err := tx.Query(`SELECT id, source_id, trace_key, event_count FROM traces ORDER BY retain_ns, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []historyTraceRow
	var total int64
	for total < events && rows.Next() {
		var row historyTraceRow
		var count int64
		if err := rows.Scan(&row.id, &row.sourceID, &row.traceKey, &count); err != nil {
			return nil, err
		}
		out = append(out, row)
		total += count
	}
	return out, rows.Err()
}

type historyTraceRow struct {
	id       int64
	sourceID int64
	traceKey string
}

func historyTraceRows(tx *sql.Tx, query string, args ...any) ([]historyTraceRow, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []historyTraceRow
	for rows.Next() {
		var row historyTraceRow
		if err := rows.Scan(&row.id, &row.sourceID, &row.traceKey); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func deleteHistoryTrace(tx *sql.Tx, trace historyTraceRow) error {
	rows, err := tx.Query(`SELECT id, hay FROM events WHERE source_id = ? AND trace_key = ?`, trace.sourceID, trace.traceKey)
	if err != nil {
		return err
	}
	type ftsRow struct {
		id  int64
		hay string
	}
	var events []ftsRow
	for rows.Next() {
		var row ftsRow
		if err := rows.Scan(&row.id, &row.hay); err != nil {
			rows.Close()
			return err
		}
		events = append(events, row)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, row := range events {
		if _, err := tx.Exec(`INSERT INTO event_fts (event_fts, rowid, hay) VALUES ('delete', ?, ?)`, row.id, row.hay); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM events WHERE source_id = ? AND trace_key = ?`, trace.sourceID, trace.traceKey); err != nil {
		return err
	}
	var hay string
	if err := tx.QueryRow(`SELECT hay FROM traces WHERE id = ?`, trace.id).Scan(&hay); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO trace_fts (trace_fts, rowid, hay) VALUES ('delete', ?, ?)`, trace.id, hay); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM trace_checkpoints WHERE source_id = ? AND trace_key = ?`, trace.sourceID, trace.traceKey); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM traces WHERE id = ?`, trace.id)
	return err
}

// historyUsedBytes is the space the store's live pages take, which pruning can reduce inside the
// transaction; the file shrinks to match once the freed pages are vacuumed after commit.
func historyUsedBytes(tx *sql.Tx) (int64, error) {
	var pages, free, size int64
	if err := tx.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		return 0, err
	}
	return (pages - free) * size, nil
}

// hasExpiredTraces reports whether any trace is past retention, so an idle log still has its old
// history pruned on schedule.
func (s *historyStore) hasExpiredTraces(now time.Time) (bool, error) {
	cutoff := retentionCutoff(now, s.metaInt("retention_days", defaultHistoryRetentionDays))
	var oldest sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(retain_ns) FROM traces`).Scan(&oldest); err != nil {
		return false, err
	}
	return oldest.Valid && oldest.Int64 < cutoff, nil
}

// retentionCutoff is the time before which a trace's last event is past retention. Retention is
// capped at maxHistoryRetentionDays, beyond which nothing expires: a longer period would overflow
// time.Duration, put the cutoff in the future, and prune everything.
func retentionCutoff(now time.Time, days int64) int64 {
	if days <= 0 || days >= maxHistoryRetentionDays {
		return math.MinInt64
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour).UnixNano()
}

// relabelIDlessEvents renames the events of a file that rotated since it was read. An event the
// writer left without an ID is named by its place in the log -- line-N in the live file,
// archive-K-line-N once rotation has moved the file to .K -- and the JSONL path names it by where
// the file is now, so the store follows.
func relabelIDlessEvents(tx *sql.Tx, sourceID int64, lf logFile) error {
	rows, err := tx.Query(`SELECT id, line_no, hay, line FROM events WHERE source_id = ? AND file_key = ? AND idless = 1`, sourceID, lf.key())
	if err != nil {
		return err
	}
	type stored struct {
		id     int64
		lineNo int
		hay    string
		line   []byte
	}
	var events []stored
	for rows.Next() {
		var row stored
		if err := rows.Scan(&row.id, &row.lineNo, &row.hay, &row.line); err != nil {
			rows.Close()
			return err
		}
		events = append(events, row)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	source := eventSource{path: lf.path, archive: lf.archive}
	for _, row := range events {
		event, err := decodeStoredEvent(row.line)
		if err != nil {
			return err
		}
		recordID := source.lineID(row.lineNo)
		derived := deriveRow(EventRecord{ID: recordID, Line: row.lineNo, Event: event})
		if _, err := tx.Exec(`INSERT INTO event_fts (event_fts, rowid, hay) VALUES ('delete', ?, ?)`, row.id, row.hay); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE events SET record_id = ?, event_id = ?, hay = ? WHERE id = ?`, recordID, derived.eventID, derived.hay, row.id); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO event_fts (rowid, hay) VALUES (?, ?)`, row.id, derived.hay); err != nil {
			return err
		}
	}
	return nil
}
