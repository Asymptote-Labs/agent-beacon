package hermessession

import (
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	_ "modernc.org/sqlite"
)

const (
	Harness      = "hermes"
	StateVersion = 1
)

type Cursor struct {
	LastMessageID    int64                      `json:"last_message_id"`
	Started          bool                       `json:"started"`
	EndedAtMS        int64                      `json:"ended_at_ms,omitempty"`
	InputTokens      int64                      `json:"input_tokens,omitempty"`
	OutputTokens     int64                      `json:"output_tokens,omitempty"`
	CacheReadTokens  int64                      `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64                      `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64                      `json:"reasoning_tokens,omitempty"`
	CostUSD          float64                    `json:"cost_usd,omitempty"`
	PendingCalls     map[string]json.RawMessage `json:"pending_calls,omitempty"`
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
}

type Summary struct {
	Sessions        int `json:"sessions"`
	SessionsChanged int `json:"sessions_changed"`
	EventsEmitted   int `json:"events_emitted"`
	Errors          int `json:"errors"`
}

type Session struct {
	ID               string
	Source           string
	Model            string
	CWD              string
	Title            string
	ParentSessionID  string
	StartedAtMS      int64
	EndedAtMS        int64
	EndReason        string
	MessageCount     int64
	ToolCallCount    int64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	EstimatedCostUSD float64
	ActualCostUSD    float64
	CostStatus       string
	CostSource       string
}

type Message struct {
	ID                int64
	SessionID         string
	Role              string
	Content           string
	ToolCallID        string
	ToolCalls         string
	ToolName          string
	TimestampMS       int64
	TokenCount        int64
	FinishReason      string
	Reasoning         string
	ReasoningContent  string
	ReasoningDetails  string
	PlatformMessageID string
	Observed          bool
	Active            bool
}

type MappedEvent struct {
	SourceMessageID int64
	Event           schema.Event
}

func DefaultDBPath() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".hermes", "state.db")
	}
	return filepath.Join(os.TempDir(), "hermes", "state.db")
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "hermes.json")
}

func LoadState(path string) (*State, error) {
	state := &State{Version: StateVersion, Sessions: map[string]*Cursor{}}
	if path == "" {
		return state, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return nil, err
	}
	var stored State
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("read Hermes collector state %s: %w", path, err)
	}
	if stored.Version != StateVersion || stored.Sessions == nil {
		return state, nil
	}
	state.Sessions = stored.Sessions
	return state, nil
}

func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	s.Version = StateVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

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

func CollectOnce(opts CollectOptions) (summary Summary, err error) {
	dbPath := opts.DBPath
	if dbPath == "" {
		dbPath = DefaultDBPath()
	}
	if _, statErr := os.Stat(dbPath); statErr != nil {
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
	summary.Sessions = len(sessions)
	state, err := LoadState(opts.StatePath)
	if err != nil {
		return summary, err
	}
	defer func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save Hermes collector state: %w", saveErr)
		}
	}()
	var errs []error
	for _, session := range sessions {
		changed, collectErr := collectSession(store, session, state, opts, &summary)
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("Hermes session %s: %w", session.ID, collectErr))
			continue
		}
		if changed {
			summary.SessionsChanged++
		}
	}
	if len(errs) > 0 {
		return summary, errors.Join(errs...)
	}
	return summary, nil
}

func collectSession(store *Store, session Session, state *State, opts CollectOptions, summary *Summary) (bool, error) {
	cursor := state.cursor(session.ID)
	messages, err := store.ReadMessages(session.ID, cursor.LastMessageID)
	if err != nil {
		return false, err
	}
	mapped := MapSession(session, messages, cursor)
	if len(mapped) == 0 {
		advanceCursorMessages(cursor, messages)
		updateCursorTotals(cursor, session)
		return false, nil
	}
	for _, item := range mapped {
		if err := emit(item.Event, opts); err != nil {
			return true, err
		}
		summary.EventsEmitted++
	}
	for _, item := range mapped {
		if item.SourceMessageID > cursor.LastMessageID {
			cursor.LastMessageID = item.SourceMessageID
		}
		switch item.Event.Event.Action {
		case "session.started":
			cursor.Started = true
		case "session.ended":
			cursor.EndedAtMS = session.EndedAtMS
		}
	}
	advanceCursorMessages(cursor, messages)
	updateCursorTotals(cursor, session)
	return true, nil
}

func advanceCursorMessages(cursor *Cursor, messages []Message) {
	for _, message := range messages {
		if message.ID > cursor.LastMessageID {
			cursor.LastMessageID = message.ID
		}
	}
}

func updateCursorTotals(cursor *Cursor, session Session) {
	if session.InputTokens > cursor.InputTokens {
		cursor.InputTokens = session.InputTokens
	}
	if session.OutputTokens > cursor.OutputTokens {
		cursor.OutputTokens = session.OutputTokens
	}
	if session.CacheReadTokens > cursor.CacheReadTokens {
		cursor.CacheReadTokens = session.CacheReadTokens
	}
	if session.CacheWriteTokens > cursor.CacheWriteTokens {
		cursor.CacheWriteTokens = session.CacheWriteTokens
	}
	if session.ReasoningTokens > cursor.ReasoningTokens {
		cursor.ReasoningTokens = session.ReasoningTokens
	}
	if cost := sessionCost(session); cost > cursor.CostUSD {
		cursor.CostUSD = cost
	}
	if session.EndedAtMS > 0 && cursor.EndedAtMS == 0 {
		cursor.EndedAtMS = session.EndedAtMS
	}
}

func emit(event schema.Event, opts CollectOptions) error {
	if opts.Print && opts.Out != nil {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := opts.Out.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	if opts.Write {
		if _, err := writer.AppendEvent(event, writer.Options{Path: opts.LogPath, UserMode: opts.UserMode}); err != nil {
			return err
		}
	}
	return nil
}

type Store struct {
	path string
	db   *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := openSQLiteReadOnly(path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{path: path, db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Path() string { return s.path }

func (s *Store) ListSessions() ([]Session, error) {
	rows, err := s.db.Query(`
SELECT id, source, model, cwd, title, parent_session_id, started_at, ended_at, end_reason,
       message_count, tool_call_count, input_tokens, output_tokens, cache_read_tokens,
       cache_write_tokens, reasoning_tokens, estimated_cost_usd, actual_cost_usd,
       cost_status, cost_source
FROM sessions
WHERE archived = 0
ORDER BY started_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		var model, cwd, title, parent, endReason, costStatus, costSource sql.NullString
		var started, ended sql.NullFloat64
		var msgCount, toolCount, input, output, cacheRead, cacheWrite, reasoning sql.NullInt64
		var estimatedCost, actualCost sql.NullFloat64
		if err := rows.Scan(&s.ID, &s.Source, &model, &cwd, &title, &parent, &started, &ended, &endReason,
			&msgCount, &toolCount, &input, &output, &cacheRead, &cacheWrite, &reasoning,
			&estimatedCost, &actualCost, &costStatus, &costSource); err != nil {
			return nil, err
		}
		s.Model = model.String
		s.CWD = cwd.String
		s.Title = title.String
		s.ParentSessionID = parent.String
		s.StartedAtMS = secondsToMillis(started)
		s.EndedAtMS = secondsToMillis(ended)
		s.EndReason = endReason.String
		s.MessageCount = msgCount.Int64
		s.ToolCallCount = toolCount.Int64
		s.InputTokens = input.Int64
		s.OutputTokens = output.Int64
		s.CacheReadTokens = cacheRead.Int64
		s.CacheWriteTokens = cacheWrite.Int64
		s.ReasoningTokens = reasoning.Int64
		s.EstimatedCostUSD = estimatedCost.Float64
		s.ActualCostUSD = actualCost.Float64
		s.CostStatus = costStatus.String
		s.CostSource = costSource.String
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s *Store) ReadMessages(sessionID string, afterID int64) ([]Message, error) {
	rows, err := s.db.Query(`
SELECT id, session_id, role, content, tool_call_id, tool_calls, tool_name, timestamp,
       token_count, finish_reason, reasoning, reasoning_content, reasoning_details,
       platform_message_id, observed, active
FROM messages
WHERE session_id = ? AND active = 1 AND id > ?
ORDER BY id`, sessionID, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var content, callID, calls, toolName, finish, reasoning, reasoningContent, reasoningDetails, platformID sql.NullString
		var ts sql.NullFloat64
		var token sql.NullInt64
		var observed, active sql.NullInt64
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &content, &callID, &calls, &toolName, &ts, &token, &finish,
			&reasoning, &reasoningContent, &reasoningDetails, &platformID, &observed, &active); err != nil {
			return nil, err
		}
		m.Content = content.String
		m.ToolCallID = callID.String
		m.ToolCalls = calls.String
		m.ToolName = toolName.String
		m.TimestampMS = secondsToMillis(ts)
		m.TokenCount = token.Int64
		m.FinishReason = finish.String
		m.Reasoning = reasoning.String
		m.ReasoningContent = reasoningContent.String
		m.ReasoningDetails = reasoningDetails.String
		m.PlatformMessageID = platformID.String
		m.Observed = observed.Int64 != 0
		m.Active = active.Int64 != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) LastMessageID(sessionID string) (int64, error) {
	var last sql.NullInt64
	if err := s.db.QueryRow(`SELECT max(id) FROM messages WHERE session_id = ? AND active = 1`, sessionID).Scan(&last); err != nil {
		return 0, err
	}
	return last.Int64, nil
}

func openSQLiteReadOnly(path string) (*sql.DB, error) {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	return sql.Open("sqlite", u.String())
}

func MapSession(session Session, messages []Message, cursor *Cursor) []MappedEvent {
	m := &mapper{session: session, cursor: cursor, calls: map[string]toolCall{}}
	if cursor != nil {
		for id, raw := range cursor.PendingCalls {
			var call toolCall
			if json.Unmarshal(raw, &call) == nil && call.ID != "" {
				m.calls[id] = call
			}
		}
	}
	m.mapSession()
	for _, message := range messages {
		m.mapMessage(message)
	}
	m.mapUsage()
	m.mapSessionEnd()
	if cursor != nil {
		if len(m.calls) > 0 {
			cursor.PendingCalls = make(map[string]json.RawMessage, len(m.calls))
			for id, call := range m.calls {
				if data, err := json.Marshal(call); err == nil {
					cursor.PendingCalls[id] = data
				}
			}
		} else {
			cursor.PendingCalls = nil
		}
	}
	return m.out
}

type mapper struct {
	session         Session
	cursor          *Cursor
	out             []MappedEvent
	calls           map[string]toolCall
	lastTimestampMS int64
}

type toolCall struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

func (m *mapper) mapSession() {
	if m.cursor != nil && m.cursor.Started {
		return
	}
	ev := m.base(m.session.StartedAtMS, "session.started", "session", schema.SeverityInfo, "Hermes session started")
	ev.Raw = map[string]interface{}{"hermes": map[string]interface{}{
		"source":            m.session.Source,
		"title":             m.session.Title,
		"parent_session_id": m.session.ParentSessionID,
	}}
	m.append(0, "session.started", ev)
}

func (m *mapper) mapSessionEnd() {
	if m.session.EndedAtMS == 0 || (m.cursor != nil && m.cursor.EndedAtMS == m.session.EndedAtMS) {
		return
	}
	ev := m.base(m.session.EndedAtMS, "session.ended", "session", schema.SeverityInfo, "Hermes session ended")
	ev.Raw = map[string]interface{}{"hermes": map[string]interface{}{
		"end_reason": m.session.EndReason,
	}}
	m.append(0, fmt.Sprintf("session.ended.%d", m.session.EndedAtMS), ev)
}

func (m *mapper) mapUsage() {
	if m.cursor == nil {
		return
	}
	usage := &schema.GenAIUsageInfo{}
	if delta := m.session.InputTokens - m.cursor.InputTokens; delta > 0 {
		usage.InputTokens = &delta
	}
	if delta := m.session.OutputTokens - m.cursor.OutputTokens; delta > 0 {
		usage.OutputTokens = &delta
	}
	if delta := m.session.CacheReadTokens - m.cursor.CacheReadTokens; delta > 0 {
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: &delta}
	}
	if delta := m.session.CacheWriteTokens - m.cursor.CacheWriteTokens; delta > 0 {
		usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: &delta}
	}
	if delta := m.session.ReasoningTokens - m.cursor.ReasoningTokens; delta > 0 {
		usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: &delta}
	}
	cost := sessionCost(m.session)
	if delta := cost - m.cursor.CostUSD; delta > 0 {
		usage.CostUSD = &delta
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.CacheRead == nil && usage.CacheCreation == nil && usage.Reasoning == nil && usage.CostUSD == nil {
		return
	}
	ts := m.session.EndedAtMS
	if ts == 0 {
		ts = m.lastTimestampMS
	}
	if ts == 0 {
		ts = m.session.StartedAtMS
	}
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}
	ev := m.base(ts, "token.usage", "metric", schema.SeverityInfo, "Hermes token usage")
	ev.GenAI = m.withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) { genAI.Usage = usage })
	ev.Raw = map[string]interface{}{"hermes": map[string]interface{}{
		"token_source": "session_totals_delta",
		"cost_status":  m.session.CostStatus,
		"cost_source":  m.session.CostSource,
	}}
	m.append(0, fmt.Sprintf("token.usage.%d.%d.%d.%d.%d.%g",
		m.session.InputTokens,
		m.session.OutputTokens,
		m.session.CacheReadTokens,
		m.session.CacheWriteTokens,
		m.session.ReasoningTokens,
		cost,
	), ev)
}

func (m *mapper) mapMessage(message Message) {
	if message.TimestampMS > m.lastTimestampMS {
		m.lastTimestampMS = message.TimestampMS
	}
	role := strings.ToLower(strings.TrimSpace(message.Role))
	switch role {
	case "user":
		if text := strings.TrimSpace(message.Content); text != "" {
			ev := m.base(message.TimestampMS, "prompt.submitted", "prompt", schema.SeverityInfo, "Hermes prompt submitted")
			ev.Prompt = &schema.PromptInfo{Text: text}
			ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
			m.append(message.ID, "prompt.submitted", ev)
		}
	case "assistant":
		for idx, text := range reasoningParts(message) {
			ev := m.base(message.TimestampMS, "agent.reasoning", "session", schema.SeverityInfo, "Hermes agent reasoning")
			ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
			ev.GenAI = m.withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
				genAI.Output = &schema.GenAIOutputInfo{Messages: []map[string]interface{}{{"role": "assistant", "content": text, "type": "reasoning"}}}
			})
			ev.Raw = map[string]interface{}{"hermes": map[string]interface{}{"reasoning_part": idx}}
			m.append(message.ID, fmt.Sprintf("reasoning.%d", idx), ev)
		}
		if text := strings.TrimSpace(message.Content); text != "" {
			ev := m.base(message.TimestampMS, "agent.message", "agent", schema.SeverityInfo, "Hermes assistant message")
			ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
			ev.GenAI = m.withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
				genAI.Output = &schema.GenAIOutputInfo{Messages: []map[string]interface{}{{"role": "assistant", "content": text}}}
			})
			m.append(message.ID, "agent.message", ev)
		}
		for idx, call := range parseToolCalls(message.ToolCalls) {
			m.calls[call.ID] = call
			ev := m.toolEvent(message.TimestampMS, call, "tool.invoked", "tool", schema.SeverityInfo, "Hermes tool invoked")
			m.append(message.ID, fmt.Sprintf("tool_call.%d", idx), ev)
		}
	case "tool":
		call := m.calls[message.ToolCallID]
		if call.ID == "" {
			call = toolCall{ID: message.ToolCallID, Name: message.ToolName}
		}
		if call.Name == "" {
			call.Name = message.ToolName
		}
		delete(m.calls, message.ToolCallID)
		m.mapToolResult(message, call)
	}
}

func (m *mapper) mapToolResult(message Message, call toolCall) {
	result := decodeJSONObject(message.Content)
	action, category, severity, msg := classifyTool(call.Name, call.Arguments, result)
	ev := m.toolEvent(message.TimestampMS, call, action, category, severity, msg)
	if output := firstString(result, "output", "stdout", "stderr", "result", "content"); output != "" {
		ev.Content = asymptoteobserve.RetainedContent(output, asymptoteobserve.DefaultStringLimit)
	}
	switch action {
	case "command.executed":
		ev.Command = &schema.CommandInfo{Command: stringArg(call.Arguments, "command", "cmd")}
		if output := firstString(result, "output", "stdout", "stderr", "result"); output != "" {
			ev.Command.Output = output
		}
		if code, ok := intArg(result, "exit_code", "exitCode", "code"); ok {
			ev.Command.ExitCode = &code
			if code != 0 {
				ev.Error = &schema.ErrorInfo{Type: "command_failed"}
			}
		}
	case "file.read", "file.created", "file.modified":
		path := stringArg(call.Arguments, "path", "file_path", "target_path")
		ev.File = &schema.FileInfo{Path: path, Operation: fileOperationForAction(action)}
		if path != "" {
			ev.File.Language = strings.TrimPrefix(filepath.Ext(path), ".")
		}
		if diff := firstString(call.Arguments, "diff", "patch"); diff != "" {
			ev.File.Diff = diff
			ev.File.DiffBytes = len(diff)
			ev.File.DiffHash = sha256String(diff)
			ev.Content = asymptoteobserve.RetainedContent(diff, asymptoteobserve.DefaultStringLimit)
		}
	case "mcp.tool_invoked":
		ev.MCP = &schema.MCPInfo{Tool: call.Name}
	}
	if errorText := firstString(result, "error"); errorText != "" && ev.Error == nil {
		ev.Error = &schema.ErrorInfo{Type: "tool_error"}
	}
	ev.Raw = mergeRaw(ev.Raw, map[string]interface{}{"hermes": map[string]interface{}{
		"tool_result": result,
	}})
	m.append(message.ID, "tool_result", ev)
}

func (m *mapper) base(timestampMS int64, action, category string, severity schema.Severity, message string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Message:  message,
		Origin:   schema.OriginLocal,
		Fidelity: schema.FidelityObserved,
		Harness: schema.HarnessInfo{
			Name:             Harness,
			CollectionMethod: schema.CollectionMethodPoll,
		},
	})
	if timestampMS > 0 {
		ev.Timestamp = schema.FormatTimestamp(time.UnixMilli(timestampMS))
	}
	ev.Session = &schema.SessionInfo{ID: m.session.ID, WorkingDirectory: m.session.CWD}
	if m.session.Model != "" {
		ev.Model = asymptoteobserve.NormalizeModelName(m.session.Model)
		ev.GenAI = m.withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Request = &schema.GenAIRequestInfo{Model: ev.Model}
		})
	}
	return ev
}

func (m *mapper) toolEvent(timestampMS int64, call toolCall, action, category string, severity schema.Severity, message string) schema.Event {
	ev := m.base(timestampMS, action, category, severity, message)
	ev.Tool = &schema.ToolInfo{Name: call.Name}
	if command := stringArg(call.Arguments, "command", "cmd"); command != "" {
		ev.Tool.Command = command
	}
	if path := stringArg(call.Arguments, "path", "file_path", "target_path"); path != "" {
		ev.Tool.Path = path
	}
	ev.GenAI = m.withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
		genAI.Operation = &schema.GenAIOperationInfo{Name: "execute_tool"}
		genAI.Tool = &schema.GenAIToolInfo{
			Name: call.Name,
			Call: &schema.GenAIToolCallInfo{
				ID:        call.ID,
				Arguments: call.Arguments,
			},
		}
	})
	return ev
}

func (m *mapper) append(sourceMessageID int64, suffix string, ev schema.Event) {
	ev.Event.ID = hermesEventID(fmt.Sprintf("%s:%d:%s", m.session.ID, sourceMessageID, suffix))
	m.out = append(m.out, MappedEvent{SourceMessageID: sourceMessageID, Event: ev})
}

func (m *mapper) withGenAI(genAI *schema.GenAIInfo, edit func(*schema.GenAIInfo)) *schema.GenAIInfo {
	if genAI == nil {
		genAI = &schema.GenAIInfo{}
	}
	edit(genAI)
	return genAI
}

func parseToolCalls(raw string) []toolCall {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	out := make([]toolCall, 0, len(items))
	for _, item := range items {
		id := firstString(item, "id", "call_id")
		fn := asMap(item["function"])
		name := firstString(item, "name", "tool_name")
		args := map[string]interface{}{}
		if fn != nil {
			name = firstNonEmpty(name, firstString(fn, "name"))
			args = decodeArguments(fn["arguments"])
		} else {
			args = decodeArguments(item["arguments"])
		}
		if id == "" {
			id = firstString(item, "tool_call_id")
		}
		out = append(out, toolCall{ID: id, Name: name, Arguments: args})
	}
	return out
}

func classifyTool(name string, args, result map[string]interface{}) (string, string, schema.Severity, string) {
	lower := strings.ToLower(strings.TrimSpace(name))
	failed := firstString(result, "error") != "" || boolArg(result, "error") || boolArg(result, "failed")
	if lower == "terminal" || strings.Contains(lower, "shell") || strings.Contains(lower, "bash") || stringArg(args, "command", "cmd") != "" {
		return "command.executed", "command", schema.SeverityInfo, "Hermes command executed"
	}
	if isFileReadTool(lower) {
		return "file.read", "file", schema.SeverityInfo, "Hermes file read"
	}
	if isFileCreateTool(lower) {
		return "file.created", "file", schema.SeverityInfo, "Hermes file created"
	}
	if isFileEditTool(lower) {
		return "file.modified", "file", schema.SeverityInfo, "Hermes file modified"
	}
	if strings.Contains(lower, "__") || strings.HasPrefix(lower, "mcp_") {
		return "mcp.tool_invoked", "mcp", schema.SeverityInfo, "Hermes MCP tool invoked"
	}
	if failed {
		return "tool.failed", "tool", schema.SeverityHigh, "Hermes tool failed"
	}
	return "tool.completed", "tool", schema.SeverityInfo, "Hermes tool completed"
}

func isFileReadTool(lower string) bool {
	return strings.Contains(lower, "read") || strings.Contains(lower, "grep") || strings.Contains(lower, "search") || strings.Contains(lower, "list") || strings.Contains(lower, "find")
}

func isFileCreateTool(lower string) bool {
	return strings.Contains(lower, "create") || strings.Contains(lower, "write")
}

func isFileEditTool(lower string) bool {
	return strings.Contains(lower, "edit") || strings.Contains(lower, "patch") || strings.Contains(lower, "replace")
}

func fileOperationForAction(action string) string {
	switch action {
	case "file.read":
		return "read"
	case "file.created":
		return "create"
	case "file.modified":
		return "modify"
	default:
		return ""
	}
}

func reasoningParts(message Message) []string {
	seen := map[string]bool{}
	var out []string
	for _, text := range []string{message.Reasoning, message.ReasoningContent, message.ReasoningDetails} {
		text = strings.TrimSpace(text)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}

func decodeArguments(value interface{}) map[string]interface{} {
	switch typed := value.(type) {
	case string:
		return decodeJSONObject(typed)
	case map[string]interface{}:
		return typed
	default:
		return map[string]interface{}{}
	}
}

func decodeJSONObject(raw string) map[string]interface{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]interface{}{}
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]interface{}{"result": raw}
	}
	return out
}

func asMap(value interface{}) map[string]interface{} {
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func stringArg(m map[string]interface{}, keys ...string) string {
	return firstString(m, keys...)
}

func firstString(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		switch v := m[key].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return v
			}
		case fmt.Stringer:
			if strings.TrimSpace(v.String()) != "" {
				return v.String()
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case int:
			return strconv.Itoa(v)
		case int64:
			return strconv.FormatInt(v, 10)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func intArg(m map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			return int(v), true
		case int:
			return v, true
		case int64:
			return int(v), true
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func boolArg(m map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		v, ok := m[key].(bool)
		if ok && v {
			return true
		}
	}
	return false
}

func mergeRaw(raw map[string]interface{}, next map[string]interface{}) map[string]interface{} {
	if raw == nil {
		raw = map[string]interface{}{}
	}
	for k, v := range next {
		raw[k] = v
	}
	return raw
}

func secondsToMillis(value sql.NullFloat64) int64 {
	if !value.Valid || value.Float64 <= 0 {
		return 0
	}
	return int64(value.Float64 * 1000)
}

func sessionCost(session Session) float64 {
	if session.ActualCostUSD > 0 {
		return session.ActualCostUSD
	}
	return session.EstimatedCostUSD
}

func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

var hermesIDNamespace = [16]byte{
	0x58, 0x24, 0x3d, 0x29, 0x5e, 0xa1, 0x4b, 0xbe,
	0x85, 0xa7, 0x49, 0x11, 0xa7, 0x18, 0xdb, 0x0c,
}

func hermesEventID(dedupID string) string {
	digest := sha1.New()
	digest.Write(hermesIDNamespace[:])
	io.WriteString(digest, dedupID)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}
