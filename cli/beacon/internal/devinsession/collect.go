// Package devinsession reads Devin CLI's local session history into Beacon endpoint events.
//
// Devin CLI keeps committed session history in a WAL-mode SQLite store (sessions.db under
// ~/.local/share/devin/cli, %APPDATA%\devin\cli on Windows). The store is a tree of message
// nodes per session; the collector walks it in row_id order and converts each row into the
// same event shape the live Devin hooks write, marked harness.collection_method=poll.
//
// Two things make this store unlike the others Beacon reads:
//
//   - Devin re-inserts history. On resume and on compaction it writes the whole conversation
//     again as new nodes, reusing each message's message_id; assistant rows are also re-inserted
//     with the same message_id for no recorded reason. Cursor-position dedupe therefore cannot
//     work: the collector keeps a per-session ring of recently seen message_ids in its cursor
//     (sha256 prefixes, capped at recentMessageIDLimit) and, for nodes that name the nodes they
//     were compacted from (extensions."compact/prior_node_ids"), looks those nodes up and skips
//     the row only when a prior node carried the same message_id.
//   - sessions.model is only the latest model. The per-turn model lives on each assistant row's
//     metadata.generation_model, so the mapper attributes every event to the model of the row it
//     came from, falling back to the last real generation_model seen in the session and then to
//     sessions.model. The internal labels "compactor" and "summarizer" are not models.
package devinsession

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionwindow"
	_ "modernc.org/sqlite"
)

const (
	// Harness must equal the hook path's --platform value (handoff.HarnessDevin, "devin-cli") so
	// hook and poll events for the same Devin session merge under one harness.name. It is written
	// as a literal rather than imported because internal/handoff imports every session collector,
	// this one included.
	Harness      = "devin-cli"
	StateVersion = 1

	// rowPageSize bounds one page of message_nodes rows. A real session can hold tens of
	// thousands of rows of up to ~1 MB of chat_message JSON each, so a session is never read
	// whole.
	rowPageSize = 500

	// recentMessageIDLimit caps the per-session ring of message_id digests kept for
	// resume/compaction re-insert dedupe. Measured on a real store: ring 4096 left 3 residual
	// duplicates out of 139,847 rows; ring 64 left 655.
	recentMessageIDLimit = 4096
)

// Cursor is one session's collection position.
type Cursor struct {
	// LastRowID is the highest message_nodes.row_id fully collected. Rows are paged in row_id
	// order and the cursor only advances past rows whose events all reached the log.
	LastRowID int64 `json:"last_row_id"`
	Started   bool  `json:"started"`
	// LastModel is the last real generation_model seen in this session, used as the model for
	// events that carry none of their own (prompts, session events, results of unknown calls).
	LastModel string `json:"last_model,omitempty"`
	// Recent holds 16-hex-char prefixes of sha256(message_id), most recent last, capped at
	// recentMessageIDLimit. It is what catches Devin's re-inserted rows across sweeps.
	Recent []string `json:"recent_message_ids,omitempty"`
	// PendingCalls are tool invocations whose result row has not been collected yet, keyed by
	// call id. Arguments are deliberately not persisted: an edit or write call's arguments can
	// be hundreds of kilobytes, and only the command/path classification fields are needed to
	// map the result.
	PendingCalls map[string]PendingCall `json:"pending_calls,omitempty"`
}

// PendingCall is the part of a tool invocation its result needs later.
type PendingCall struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Model     string `json:"model,omitempty"`
	Command   string `json:"command,omitempty"`
	Path      string `json:"path,omitempty"`
	MCPServer string `json:"mcp_server,omitempty"`
	MCPTool   string `json:"mcp_tool,omitempty"`
}

type State struct {
	Version  int                `json:"version"`
	Sessions map[string]*Cursor `json:"sessions"`
}

type CollectOptions struct {
	DBPath    string
	StatePath string
	Write     bool
	LogPath   string
	UserMode  bool
	Print     bool
	Out       io.Writer
	// RotateBytes is the size at which this sweep rotates the runtime log; zero means the size
	// every Beacon writer uses.
	RotateBytes int64
	// Since, when set, limits the sweep to sessions modified at or after it, newest first. Older
	// sessions are not read and their cursors do not move, so a later sync still collects them.
	Since time.Time
	// Budget, when set, caps the bytes this sweep may append. The sweep stops at the first event
	// that does not fit and returns writer.ErrBudgetSpent, with cursors past only what was written.
	Budget *writer.Budget
	// LiveCoverage, when set, marks the sessions the live Devin hooks have already reported, by
	// the earliest hook event timestamp per session id. The hook payload carries prompts and tool
	// calls but no assistant text, reasoning, token metrics or model, so for rows at or after
	// live coverage began the sweep suppresses the actions hooks would double-count (prompts,
	// tool invocations and tool results, session.started and handoffs) and keeps the ones hooks
	// cannot see.
	LiveCoverage map[string]time.Time
}

type Summary struct {
	Sessions        int `json:"sessions"`
	SessionsChanged int `json:"sessions_changed"`
	EventsEmitted   int `json:"events_emitted"`
	Errors          int `json:"errors"`
	// EventsSuppressedLive counts events the sweep mapped but did not emit because live hook
	// coverage made them duplicates (see CollectOptions.LiveCoverage).
	EventsSuppressedLive int `json:"events_suppressed_live"`
	// EventsRetained is how many of the events this sweep wrote are still in the runtime log or
	// its archives when it finishes. EventsRotatedOut is how many another writer's rotations
	// discarded before it finished.
	EventsRetained   int `json:"events_retained"`
	EventsRotatedOut int `json:"events_rotated_out"`
	// RetentionLimited is set when the sweep stopped because writing more would have rotated its
	// own output out of the log; SessionsPending is how many sessions still had rows to read.
	RetentionLimited bool `json:"retention_limited"`
	SessionsPending  int  `json:"sessions_pending"`
}

// Session is one row of the sessions table.
type Session struct {
	ID               string
	WorkingDirectory string
	BackendType      string
	Model            string
	AgentMode        string
	Title            string
	Hidden           bool
	CreatedAtMS      int64
	LastActivityAtMS int64
	// Metadata is the raw sessions.metadata JSON; the mapper pulls total_acu_cost and
	// total_credit_cost out of it for session.started's raw block.
	Metadata string
}

// Row is one row of the message_nodes table.
type Row struct {
	RowID        int64
	SessionID    string
	NodeID       int64
	ParentNodeID *int64
	ChatMessage  string
	CreatedAtMS  int64
	// NodeMetadata is the raw node metadata JSON ("summarized_from", "compact/prior_node_ids"),
	// which may be SQL NULL or the literal string "null".
	NodeMetadata string
}

// MappedEvent is one endpoint event and the row it came from. SourceRowID is 0 for events that
// are not tied to a row (session.started).
type MappedEvent struct {
	SourceRowID int64
	TimestampMS int64
	Event       schema.Event
}

var stateFile = sessionsync.StateFile[State]{
	Name:  "Devin",
	New:   func() *State { return &State{Version: StateVersion, Sessions: map[string]*Cursor{}} },
	Valid: func(s *State) bool { return s.Version == StateVersion && s.Sessions != nil },
	Stamp: func(s *State) { s.Version = StateVersion },
}

func LoadState(path string) (*State, error) { return stateFile.Load(path) }

func (s *State) Save(path string) error { return stateFile.Save(path, s) }

func (s *State) cursor(sessionID string) *Cursor {
	if s.Sessions == nil {
		s.Sessions = map[string]*Cursor{}
	}
	cursor := s.Sessions[sessionID]
	if cursor == nil {
		cursor = &Cursor{}
		s.Sessions[sessionID] = cursor
	}
	return cursor
}

// rowJournal is the record of what mapping one row changed in the cursor. A row's events are
// emitted right after it maps, and if an emit fails the journal rolls the row's changes back so
// the retried sweep sees the row exactly as it was -- a ring entry kept from a row the cursor
// never reached would make the retry skip it as a duplicate, and a pending call kept from one
// would attribute its result wrongly.
type rowJournal struct {
	lastModel      string
	recentLen      int
	recentAdded    []string
	recentEvicted  []string
	pendingAdded   []string
	pendingRemoved map[string]PendingCall
}

func CollectOnce(opts CollectOptions) (summary Summary, err error) {
	dbPath := opts.DBPath
	if dbPath == "" {
		dbPath = DefaultDBPath()
	}
	if _, statErr := sessionfs.Stat(dbPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return summary, nil
		}
		return summary, statErr
	}
	store, err := OpenStore(dbPath)
	if err != nil {
		return summary, err
	}
	defer store.Close()
	sessions, err := store.ListSessions()
	if err != nil {
		return summary, err
	}
	sessions = sessionwindow.Recent(sessions, opts.Since, func(s Session) int64 {
		if s.LastActivityAtMS > s.CreatedAtMS {
			return s.LastActivityAtMS
		}
		return s.CreatedAtMS
	})
	summary.Sessions = len(sessions)
	state, err := LoadState(opts.StatePath)
	if err != nil {
		return summary, err
	}
	defer func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save Devin collector state: %w", saveErr)
		}
	}()

	sink := opts.sink()
	sink.GuardRetention()
	var errs []error
	for i, session := range sessions {
		changed, collectErr := collectSession(store, session, state, opts, sink, &summary)
		if errors.Is(collectErr, writer.ErrRetentionWindowFull) {
			// Not a failed session: this one and the rest are left for the next sweep, with
			// cursors that have not moved past anything that was not written.
			if changed {
				summary.SessionsChanged++
			}
			summary.RetentionLimited = true
			summary.SessionsPending = 1 + countPending(store, sessions[i+1:], state)
			errs = append(errs, fmt.Errorf("stopped after %d events with %d Devin CLI session(s) pending: %w",
				summary.EventsEmitted, summary.SessionsPending, collectErr))
			break
		}
		if errors.Is(collectErr, writer.ErrBudgetSpent) {
			// Same: the budget is spent and the rest wait for a later sync.
			if changed {
				summary.SessionsChanged++
			}
			errs = append(errs, collectErr)
			break
		}
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("Devin CLI session %s: %w", session.ID, collectErr))
			continue
		}
		if changed {
			summary.SessionsChanged++
		}
	}
	var rotatedOut error
	summary.EventsRetained, summary.EventsRotatedOut, rotatedOut = sink.Retention()
	if rotatedOut != nil {
		errs = append(errs, rotatedOut)
	}
	if len(errs) > 0 {
		return summary, errors.Join(errs...)
	}
	return summary, nil
}

// countPending is how many of sessions have rows the cursor has not reached, which is what a
// retention-limited sweep leaves for the next one.
func countPending(store *Store, sessions []Session, state *State) int {
	n := 0
	for _, session := range sessions {
		last, err := store.LastRowID(session.ID)
		if err != nil {
			continue
		}
		cursor := state.Sessions[session.ID]
		if cursor == nil || cursor.LastRowID < last {
			n++
		}
	}
	return n
}

func collectSession(store *Store, session Session, state *State, opts CollectOptions, sink sessionsync.Sink, summary *Summary) (bool, error) {
	cursor := state.cursor(session.ID)
	m := newMapper(session, cursor)
	changed := false

	// session.started carries no row: it precedes the first page and does not move LastRowID.
	// sessions.created_at is whole seconds and lands after the first prompt on real data, so the
	// stamp is the earlier of it and the first collected row's timestamp.
	if !cursor.Started {
		startTS := session.CreatedAtMS
		if firstRows, err := store.ReadRows(session.ID, cursor.LastRowID, 1); err != nil {
			return changed, err
		} else if len(firstRows) > 0 {
			if firstTS := rowTimestampMS(firstRows[0], mapValue(decodeObject(firstRows[0].ChatMessage)["metadata"])); firstTS > 0 && (startTS == 0 || firstTS < startTS) {
				startTS = firstTS
			}
		}
		started := m.mapSessionStarted(startTS)
		if suppressedLive(started.Event.Event.Action, started.TimestampMS, session, opts) {
			summary.EventsSuppressedLive++
		} else if err := sink.Emit(started.Event); err != nil {
			return true, err
		} else {
			summary.EventsEmitted++
		}
		cursor.Started = true
		changed = true
	}

	for {
		rows, err := store.ReadRows(session.ID, cursor.LastRowID, rowPageSize)
		if err != nil {
			return changed, err
		}
		if len(rows) == 0 {
			return changed, nil
		}
		priorIDs, err := store.priorMessageIDs(session.ID, collectPriorNodeIDs(rows))
		if err != nil {
			return changed, err
		}
		// The cursor moves row by row, only past rows whose events are all written: a retried
		// sweep starts at the first unwritten row rather than re-emitting the page. A row whose
		// emit fails partway through rolls its cursor changes back (see rowJournal); the events
		// it did write re-emit on the retry with identical deterministic ids, which is
		// acceptable -- a row emits at most a handful of events.
		for _, row := range rows {
			journal := m.beginRow()
			for _, item := range m.mapRow(row, priorIDs) {
				if suppressedLive(item.Event.Event.Action, item.TimestampMS, session, opts) {
					summary.EventsSuppressedLive++
					continue
				}
				if err := sink.Emit(item.Event); err != nil {
					m.rollbackRow(journal)
					return true, err
				}
				summary.EventsEmitted++
			}
			cursor.LastRowID = row.RowID
			changed = true
		}
	}
}

// suppressedLive reports whether this event is one the live hook path already recorded. Devin
// hooks see the prompt and every tool call as it happens but the payload carries no assistant
// text, reasoning, token metrics or model, so actions hooks would double-count are skipped for
// rows at or after live coverage began (with a small clock-skew allowance), while agent.message,
// agent.reasoning, token.usage, session.context, session.compacted and approval.denied always
// emit -- the hook never saw them.
func suppressedLive(action string, timestampMS int64, session Session, opts CollectOptions) bool {
	cutover, ok := opts.LiveCoverage[session.ID]
	if !ok {
		return false
	}
	if timestampMS < cutover.UnixMilli()-2000 {
		return false
	}
	switch action {
	case "session.started", "prompt.submitted", "session.handoff",
		"tool.invoked", "mcp.tool_invoked",
		"command.executed", "file.read", "file.modified", "tool.completed", "tool.failed":
		return true
	}
	return false
}

func (o CollectOptions) sink() sessionsync.Sink {
	return sessionsync.Sink{Print: o.Print, Out: o.Out, Write: o.Write, Log: writer.Options{Path: o.LogPath, UserMode: o.UserMode, RotateSize: o.RotateBytes, Budget: o.Budget}}
}

// Store is a read-only handle on sessions.db.
type Store struct {
	path           string
	db             *sql.DB
	sessionColumns map[string]bool
	nodeColumns    map[string]bool
}

func OpenStore(path string) (*Store, error) {
	db, err := sessionsync.OpenSQLite(path, "busy_timeout(1000)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Store{path: path, db: db}
	for _, table := range []string{"sessions", "message_nodes"} {
		columns, err := tableColumns(db, table)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		if table == "sessions" {
			store.sessionColumns = columns
		} else {
			store.nodeColumns = columns
		}
	}
	for _, required := range []struct{ table, column string }{
		{"sessions", "id"},
		{"message_nodes", "row_id"},
		{"message_nodes", "session_id"},
		{"message_nodes", "node_id"},
		{"message_nodes", "chat_message"},
		{"message_nodes", "created_at"},
	} {
		columns := store.nodeColumns
		if required.table == "sessions" {
			columns = store.sessionColumns
		}
		if !columns[required.column] {
			_ = db.Close()
			return nil, fmt.Errorf("Devin sessions database %s is missing required column %s.%s", path, required.table, required.column)
		}
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Path() string { return s.path }

// tableColumns lists a table's columns, so the queries below can select optional columns when
// they exist and NULL when they do not. Columns or JSON keys this collector does not know are
// ignored either way.
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf(`SELECT name FROM pragma_table_info(%s)`, quoteIdent(table)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func quoteIdent(name string) string {
	return `'` + strings.ReplaceAll(name, `'`, `''`) + `'`
}

// pick renders a column name for SELECT when it exists and NULL aliased to it when it does not.
func pick(columns map[string]bool, name string) string {
	if columns[name] {
		return name
	}
	return "NULL AS " + name
}

func (s *Store) ListSessions() ([]Session, error) {
	orderBy := "created_at, id"
	if !s.sessionColumns["created_at"] {
		orderBy = "id"
	}
	rows, err := s.db.Query(fmt.Sprintf(`
SELECT id, %s, %s, %s, %s, %s, %s, %s, %s, %s
FROM sessions
ORDER BY `+orderBy,
		pick(s.sessionColumns, "working_directory"),
		pick(s.sessionColumns, "backend_type"),
		pick(s.sessionColumns, "model"),
		pick(s.sessionColumns, "agent_mode"),
		pick(s.sessionColumns, "title"),
		pick(s.sessionColumns, "hidden"),
		pick(s.sessionColumns, "created_at"),
		pick(s.sessionColumns, "last_activity_at"),
		pick(s.sessionColumns, "metadata")))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var session Session
		var wd, backend, model, mode, title, metadata sql.NullString
		var created, activity sql.NullInt64
		var hidden sql.NullInt64
		if err := rows.Scan(&session.ID, &wd, &backend, &model, &mode, &title, &hidden, &created, &activity, &metadata); err != nil {
			return nil, err
		}
		session.WorkingDirectory = wd.String
		session.BackendType = backend.String
		session.Model = model.String
		session.AgentMode = mode.String
		session.Title = title.String
		session.Hidden = hidden.Int64 != 0
		session.CreatedAtMS = unixSecondsMS(created)
		session.LastActivityAtMS = unixSecondsMS(activity)
		session.Metadata = metadata.String
		out = append(out, session)
	}
	return out, rows.Err()
}

// ReadRows returns up to limit message_nodes rows for one session past afterRowID, in row_id
// order. Pages keep a large session out of memory: row chat_message values can reach ~1 MB.
func (s *Store) ReadRows(sessionID string, afterRowID int64, limit int) ([]Row, error) {
	rows, err := s.db.Query(fmt.Sprintf(`
SELECT row_id, session_id, node_id, %s, chat_message, created_at, %s
FROM message_nodes
WHERE session_id = ? AND row_id > ?
ORDER BY row_id
LIMIT ?`, pick(s.nodeColumns, "parent_node_id"), pick(s.nodeColumns, "metadata")), sessionID, afterRowID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var row Row
		var parent sql.NullInt64
		var created sql.NullInt64
		var metadata sql.NullString
		if err := rows.Scan(&row.RowID, &row.SessionID, &row.NodeID, &parent, &row.ChatMessage, &created, &metadata); err != nil {
			return nil, err
		}
		if parent.Valid {
			row.ParentNodeID = &parent.Int64
		}
		row.CreatedAtMS = unixSecondsMS(created)
		row.NodeMetadata = metadata.String
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) LastRowID(sessionID string) (int64, error) {
	var last sql.NullInt64
	if err := s.db.QueryRow(`SELECT max(row_id) FROM message_nodes WHERE session_id = ?`, sessionID).Scan(&last); err != nil {
		return 0, err
	}
	return last.Int64, nil
}

// priorMessageIDs resolves the message_ids of given node_ids within one session, in chunks of at
// most 500 bound parameters. A compaction copy names the nodes it was compacted from; matching
// the copy's message_id against a prior node's is what tells a resume copy from new content.
func (s *Store) priorMessageIDs(sessionID string, nodeIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(nodeIDs) == 0 {
		return out, nil
	}
	const chunk = 500
	for start := 0; start < len(nodeIDs); start += chunk {
		end := start + chunk
		if end > len(nodeIDs) {
			end = len(nodeIDs)
		}
		part := nodeIDs[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		args := make([]interface{}, 0, len(part)+1)
		args = append(args, sessionID)
		for _, id := range part {
			args = append(args, id)
		}
		rows, err := s.db.Query(fmt.Sprintf(`
SELECT node_id, json_extract(chat_message, '$.message_id')
FROM message_nodes
WHERE session_id = ? AND node_id IN (%s)`, placeholders), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var nodeID int64
			var messageID sql.NullString
			if err := rows.Scan(&nodeID, &messageID); err != nil {
				rows.Close()
				return nil, err
			}
			out[nodeID] = messageID.String
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func unixSecondsMS(value sql.NullInt64) int64 {
	if !value.Valid || value.Int64 <= 0 {
		return 0
	}
	return value.Int64 * 1000
}
