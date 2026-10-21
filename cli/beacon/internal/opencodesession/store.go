package opencodesession

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sqliteuri"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	defaultListLimit = 10000
	maxSessionBytes  = 1 << 20
)

type Store struct {
	DataDir          string
	SQLitePath       string
	LegacyStorageDir string
}

// NewStore opens the OpenCode store at dataDir, which is a data directory or an opencode.db path.
// Empty means where OpenCode itself writes: its data directory, with the database named by
// OPENCODE_DB when that is set.
func NewStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		data := DefaultDataDir()
		if data == "" {
			return nil, errors.New("opencode data directory is empty")
		}
		return &Store{
			DataDir:          data,
			SQLitePath:       defaultDBPath(data),
			LegacyStorageDir: filepath.Join(data, "storage"),
		}, nil
	}
	dataDir = filepath.Clean(dataDir)
	if strings.HasSuffix(dataDir, ".db") {
		return &Store{
			DataDir:          filepath.Dir(dataDir),
			SQLitePath:       dataDir,
			LegacyStorageDir: filepath.Join(filepath.Dir(dataDir), "storage"),
		}, nil
	}
	return &Store{
		DataDir:          dataDir,
		SQLitePath:       filepath.Join(dataDir, "opencode.db"),
		LegacyStorageDir: filepath.Join(dataDir, "storage"),
	}, nil
}

// DefaultDataDir is OpenCode's data directory, which holds opencode.db and the legacy storage tree.
// OpenCode uses XDG paths on every platform, Windows included. Its plugins and configuration live in
// a separate directory (~/.config/opencode) that holds no sessions.
func DefaultDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode")
}

// defaultDBPath resolves OPENCODE_DB as OpenCode does: an absolute path is used as given and a
// relative one is resolved against the data directory. Unset, or the in-memory name, means
// opencode.db.
func defaultDBPath(data string) string {
	name := os.Getenv("OPENCODE_DB")
	if name == "" || name == ":memory:" {
		return filepath.Join(data, "opencode.db")
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Join(data, name)
}

func (s *Store) Exists() bool {
	return fileExists(s.SQLitePath) || dirExists(s.LegacyStorageDir)
}

func (s *Store) List() ([]TraceRef, error) {
	var refs []TraceRef
	var errs []error
	sqliteRefs, err := s.listSQLite()
	if err != nil {
		errs = append(errs, err)
	}
	refs = append(refs, sqliteRefs...)
	seen := make(map[string]bool, len(sqliteRefs))
	for _, ref := range sqliteRefs {
		seen[ref.ID] = true
	}
	legacyRefs, err := s.listLegacy()
	if err != nil {
		errs = append(errs, err)
	}
	for _, ref := range legacyRefs {
		if seen[ref.ID] {
			continue
		}
		refs = append(refs, ref)
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].UpdatedAtUnixMS != refs[j].UpdatedAtUnixMS {
			return refs[i].UpdatedAtUnixMS < refs[j].UpdatedAtUnixMS
		}
		return refs[i].ID < refs[j].ID
	})
	return refs, errors.Join(errs...)
}

func (s *Store) Read(ref TraceRef) ([]Record, error) {
	switch ref.Kind {
	case SourceSQLite:
		return s.readSQLite(ref)
	case SourceLegacy:
		return s.readLegacy(ref)
	default:
		return nil, fmt.Errorf("unknown opencode source kind %q", ref.Kind)
	}
}

func (s *Store) listSQLite() ([]TraceRef, error) {
	if !fileExists(s.SQLitePath) {
		return nil, nil
	}
	db, err := openSQLiteReadOnly(s.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("open OpenCode sqlite: %w", err)
	}
	defer db.Close()
	// OpenCode 2.x keeps sessions in session_v2 and migrates 1.x sessions into it under the same
	// ids. session is the 1.x table, still read for a session that has not been migrated.
	var refs []TraceRef
	seen := map[string]bool{}
	for _, table := range []string{"session_v2", "session"} {
		if !sqliteTableExists(db, table) {
			continue
		}
		tableRefs, err := s.listSQLiteTable(db, table)
		for _, ref := range tableRefs {
			if !seen[ref.ID] {
				seen[ref.ID] = true
				refs = append(refs, ref)
			}
		}
		if err != nil {
			return refs, err
		}
	}
	return refs, nil
}

func (s *Store) listSQLiteTable(db *sql.DB, table string) ([]TraceRef, error) {
	cols := sqliteColumns(db, table)
	selects := []string{
		sqliteSelectColumn(cols, "id"),
		sqliteSelectColumn(cols, "title"),
		sqliteSelectColumn(cols, "directory"),
		sqliteSelectColumn(cols, "project_worktree"),
		sqliteSelectColumn(cols, "time_created"),
		sqliteSelectColumn(cols, "time_updated"),
		sqliteSelectColumn(cols, "parent_id"),
	}
	rows, err := db.Query("SELECT "+strings.Join(selects, ", ")+" FROM "+table+" ORDER BY COALESCE(time_updated, time_created, 0) DESC LIMIT ?", defaultListLimit)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode sessions: %w", err)
	}
	defer rows.Close()
	var refs []TraceRef
	for rows.Next() {
		var id, title, directory, worktree, parent sql.NullString
		var created, updated sql.NullInt64
		if err := rows.Scan(&id, &title, &directory, &worktree, &created, &updated, &parent); err != nil {
			continue
		}
		if !id.Valid || strings.TrimSpace(id.String) == "" {
			continue
		}
		dir := firstNonEmpty(nullString(directory), nullString(worktree))
		ref := TraceRef{
			ID:              id.String,
			Kind:            SourceSQLite,
			SourcePath:      s.SQLitePath,
			Title:           firstNonEmpty(nullString(title), "Untitled Session"),
			Preview:         firstNonEmpty(nullString(title), "(No preview)"),
			Directory:       dir,
			CreatedAtUnixMS: normalizeMillis(nullInt(created)),
			UpdatedAtUnixMS: normalizeMillis(firstNonZero(nullInt(updated), nullInt(created))),
		}
		if parent.Valid && parent.String != "" {
			ref.RelatedTo = parent.String
			ref.RelationshipType = "subagent"
		}
		if info, err := sessionfs.Stat(s.SQLitePath); err == nil {
			ref.SizeBytes = info.Size()
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (s *Store) readSQLite(ref TraceRef) ([]Record, error) {
	db, err := openSQLiteReadOnly(s.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("open OpenCode sqlite: %w", err)
	}
	defer db.Close()
	var records []Record
	// through is the latest 1.x message or part. OpenCode's migration copies a 1.x session into
	// session_message with the same timestamps, so only what 2.x wrote after it is read from there.
	var through int64
	if sqliteTableExists(db, "message") && sqliteTableExists(db, "part") {
		messages, err := readSQLiteMessages(db, ref.ID)
		if err != nil {
			return nil, err
		}
		parts, err := readSQLiteParts(db, ref.ID)
		if err != nil {
			return nil, err
		}
		records = recordsFromMessages(messages, parts)
		for _, msg := range messages {
			through = max(through, msg.Time.Created, msg.Time.Completed)
			for _, part := range parts[msg.ID] {
				through = max(through, part.TimeCreated)
			}
		}
	}
	if sqliteTableExists(db, "session_message") {
		v2, err := readSessionMessages(db, ref.ID, through, len(records)+1)
		if err != nil {
			return nil, err
		}
		records = append(records, v2...)
	}
	return records, nil
}

func readSQLiteMessages(db *sql.DB, sessionID string) ([]opencodeMessage, error) {
	cols := sqliteColumns(db, "message")
	rows, err := db.Query("SELECT "+strings.Join([]string{
		sqliteSelectColumn(cols, "id"),
		sqliteSelectColumn(cols, "session_id"),
		sqliteSelectColumn(cols, "time_created"),
		sqliteSelectColumn(cols, "data"),
	}, ", ")+" FROM message WHERE session_id = ? ORDER BY COALESCE(time_created, 0), id", sessionID)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode messages: %w", err)
	}
	defer rows.Close()
	var out []opencodeMessage
	for rows.Next() {
		var id, sid, data sql.NullString
		var created sql.NullInt64
		if err := rows.Scan(&id, &sid, &created, &data); err != nil {
			continue
		}
		msg := parseMessageJSON(nullString(data))
		msg.ID = firstNonEmpty(msg.ID, nullString(id))
		msg.SessionID = firstNonEmpty(msg.SessionID, nullString(sid), sessionID)
		if msg.Time.Created == 0 {
			msg.Time.Created = normalizeMillis(nullInt(created))
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func readSQLiteParts(db *sql.DB, sessionID string) (map[string][]opencodePart, error) {
	cols := sqliteColumns(db, "part")
	rows, err := db.Query("SELECT "+strings.Join([]string{
		sqliteSelectColumn(cols, "id"),
		sqliteSelectColumn(cols, "message_id"),
		sqliteSelectColumn(cols, "session_id"),
		sqliteSelectColumn(cols, "time_created"),
		sqliteSelectColumn(cols, "data"),
	}, ", ")+" FROM part WHERE session_id = ? ORDER BY COALESCE(time_created, 0), id", sessionID)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode parts: %w", err)
	}
	defer rows.Close()
	out := map[string][]opencodePart{}
	for rows.Next() {
		var id, messageID, sid, data sql.NullString
		var created sql.NullInt64
		if err := rows.Scan(&id, &messageID, &sid, &created, &data); err != nil {
			continue
		}
		part := parsePartJSON(nullString(data))
		part.ID = firstNonEmpty(part.ID, nullString(id))
		part.MessageID = firstNonEmpty(part.MessageID, nullString(messageID))
		part.SessionID = firstNonEmpty(part.SessionID, nullString(sid), sessionID)
		part.TimeCreated = normalizeMillis(nullInt(created))
		out[part.MessageID] = append(out[part.MessageID], part)
	}
	return out, rows.Err()
}

// readSessionMessages reads the session_message rows OpenCode 2.x wrote after through. Each row is
// one typed entry, and an assistant step carries its text, reasoning and tool calls in content.
func readSessionMessages(db *sql.DB, sessionID string, through int64, order int) ([]Record, error) {
	rows, err := db.Query("SELECT id, type, time_created, data FROM session_message WHERE session_id = ? AND time_created > ? ORDER BY seq", sessionID, through)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode session messages: %w", err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var id, typ, data sql.NullString
		var created sql.NullInt64
		if err := rows.Scan(&id, &typ, &created, &data); err != nil {
			continue
		}
		var raw map[string]interface{}
		_ = json.Unmarshal([]byte(nullString(data)), &raw)
		for _, rec := range sessionMessageRecords(nullString(id), nullString(typ), normalizeMillis(nullInt(created)), raw) {
			rec.Order = order
			order++
			out = append(out, rec)
		}
	}
	return out, rows.Err()
}

func sessionMessageRecords(id, typ string, created int64, raw map[string]interface{}) []Record {
	model := opencodeModel(raw)
	switch typ {
	case "user":
		text := stringFromMap(raw, "text")
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []Record{{NativeID: id, TimestampMS: created, Type: "user_message", Role: "user", Content: text, ModelID: model}}
	case "shell":
		// A command the user ran with !, recorded as the bash tool call 1.x stored it as.
		status := "completed"
		if stringFromMap(raw, "status") == "running" {
			status = "running"
		}
		output := map[string]interface{}{"output": stringFromMap(mapFromAny(raw["output"]), "output")}
		if exit, ok := raw["exit"]; ok {
			output["metadata"] = map[string]interface{}{"exit": exit}
		}
		recType := "tool_result"
		if status == "running" {
			recType = "tool_call"
		}
		return []Record{{NativeID: id, TimestampMS: firstNonZero(normalizeMillis(intFromAny(mapFromAny(raw["time"])["completed"])), created), Type: recType,
			ToolName: "bash", CallID: firstNonEmpty(stringFromMap(raw, "shellID"), id), Args: map[string]interface{}{"command": stringFromMap(raw, "command")},
			Output: output, Status: status, Raw: map[string]interface{}{"message": raw}}}
	case "compaction":
		// A completed compaction carries the usage of the model call that summarized the history.
		out := []Record{{NativeID: id, TimestampMS: created, Type: "compaction", ModelID: model, Raw: map[string]interface{}{"message": raw}}}
		if usage, ok := sessionMessageUsage(id, raw, created, model); ok {
			out = append(out, usage)
		}
		return out
	case "assistant":
	default:
		return nil
	}
	var out []Record
	content, _ := raw["content"].([]interface{})
	for i, value := range content {
		item := mapFromAny(value)
		nativeID := id + ":" + strconv.Itoa(i)
		switch stringFromMap(item, "type") {
		case "text", "reasoning":
			text := stringFromMap(item, "text")
			if strings.TrimSpace(text) == "" {
				continue
			}
			recType := "assistant_text"
			if stringFromMap(item, "type") == "reasoning" {
				recType = "agent_reasoning"
			}
			out = append(out, Record{NativeID: nativeID, TimestampMS: created, Type: recType, Role: "assistant", Content: text, ModelID: model, Raw: map[string]interface{}{"part": item}})
		case "tool":
			out = append(out, sessionToolRecord(id, item, created, model))
		}
	}
	if usage, ok := sessionMessageUsage(id, raw, created, model); ok {
		out = append(out, usage)
	}
	completed := firstNonZero(normalizeMillis(intFromAny(mapFromAny(raw["time"])["completed"])), created)
	if errMap := mapFromAny(raw["error"]); len(content) == 0 && len(errMap) > 0 {
		out = append(out, Record{NativeID: id + ":error", TimestampMS: completed, Type: "error", Content: stringFromMap(errMap, "message", "type"), ModelID: model, Raw: map[string]interface{}{"message": raw}})
	}
	return out
}

// sessionMessageUsage is the token_usage record for a session_message row's tokens and cost.
func sessionMessageUsage(id string, raw map[string]interface{}, created int64, model string) (Record, bool) {
	completed := firstNonZero(normalizeMillis(intFromAny(mapFromAny(raw["time"])["completed"])), created)
	tokens := tokenUsageFromAny(raw["tokens"])
	// 2.x stores output without reasoning; a total that covers both tells outputTokens to add it back.
	tokens.Total = tokens.Input + tokens.CacheRead + tokens.CacheWrite + tokens.Output + tokens.Reasoning
	tokens.CostUSD = floatFromAny(raw["cost"])
	if !hasUsage(tokens) {
		return Record{}, false
	}
	return Record{NativeID: id + ":usage", TimestampMS: completed, Type: "token_usage", ModelID: model, Tokens: &tokens}, true
}

func sessionToolRecord(messageID string, item map[string]interface{}, created int64, model string) Record {
	state := mapFromAny(item["state"])
	timeMap := mapFromAny(item["time"])
	status := stringFromMap(state, "status")
	rec := Record{
		NativeID:    messageID + ":" + stringFromMap(item, "id"),
		TimestampMS: firstNonZero(normalizeMillis(intFromAny(timeMap["created"])), created),
		Type:        "tool_call",
		ModelID:     model,
		ToolName:    stringFromMap(item, "name"),
		CallID:      stringFromMap(item, "id"),
		Args:        mapFromAny(state["input"]),
		Status:      firstNonEmpty(status, "invoked"),
		Raw:         map[string]interface{}{"part": item},
	}
	if status != "completed" && status != "error" {
		return rec
	}
	rec.Type = "tool_result"
	rec.TimestampMS = firstNonZero(normalizeMillis(intFromAny(timeMap["completed"])), rec.TimestampMS)
	var texts []string
	contentItems, _ := state["content"].([]interface{})
	for _, value := range contentItems {
		if text := stringFromMap(mapFromAny(value), "text"); text != "" {
			texts = append(texts, text)
		}
	}
	var output interface{} = strings.Join(texts, "\n")
	if metadata := mapFromAny(state["metadata"]); len(metadata) > 0 {
		output = map[string]interface{}{"output": output, "metadata": metadata}
	}
	rec.Output = output
	if errText := stringFromMap(mapFromAny(state["error"]), "message"); errText != "" {
		rec.Raw["error"] = errText
	}
	return rec
}

func (s *Store) listLegacy() ([]TraceRef, error) {
	sessionRoot := filepath.Join(s.LegacyStorageDir, "session")
	if !dirExists(sessionRoot) {
		return nil, nil
	}
	projects, err := sessionfs.ReadDir(sessionRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read OpenCode legacy sessions: %w", err)
	}
	var refs []TraceRef
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		projectID := project.Name()
		projectDir := legacyProjectDirectory(filepath.Join(s.LegacyStorageDir, "project", projectID+".json"))
		sessionDir := filepath.Join(sessionRoot, projectID)
		files, err := sessionfs.ReadDir(sessionDir)
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			path := filepath.Join(sessionDir, file.Name())
			ref, ok := readLegacySessionRef(path, projectDir)
			if ok {
				refs = append(refs, ref)
			}
		}
	}
	return refs, nil
}

func (s *Store) readLegacy(ref TraceRef) ([]Record, error) {
	session := readLegacySession(ref.SourcePath)
	if session.ID == "" {
		return nil, fmt.Errorf("read OpenCode legacy session %s", ref.SourcePath)
	}
	messageDir := filepath.Join(s.LegacyStorageDir, "message", session.ID)
	files, err := sessionfs.ReadDir(messageDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var messages []opencodeMessage
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		msg := readLegacyMessage(filepath.Join(messageDir, file.Name()))
		if msg.ID != "" {
			messages = append(messages, msg)
		}
	}
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].Time.Created != messages[j].Time.Created {
			return messages[i].Time.Created < messages[j].Time.Created
		}
		return messages[i].ID < messages[j].ID
	})
	parts := map[string][]opencodePart{}
	for _, msg := range messages {
		partDir := filepath.Join(s.LegacyStorageDir, "part", msg.ID)
		partFiles, err := sessionfs.ReadDir(partDir)
		if err != nil {
			continue
		}
		sort.SliceStable(partFiles, func(i, j int) bool { return naturalLess(partFiles[i].Name(), partFiles[j].Name()) })
		for _, file := range partFiles {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			part := readLegacyPart(filepath.Join(partDir, file.Name()))
			if part.ID != "" {
				part.MessageID = firstNonEmpty(part.MessageID, msg.ID)
				parts[msg.ID] = append(parts[msg.ID], part)
			}
		}
	}
	return recordsFromMessages(messages, parts), nil
}

type opencodeMessage struct {
	ID        string
	SessionID string
	Role      string
	ModelID   string
	Summary   bool
	ParentID  string
	Error     map[string]interface{}
	Time      struct {
		Created   int64
		Completed int64
	}
	Tokens TokenUsage
	Cost   float64
	Raw    map[string]interface{}
}

type opencodePart struct {
	ID          string
	SessionID   string
	MessageID   string
	Type        string
	Text        string
	Tool        string
	CallID      string
	State       map[string]interface{}
	TimeCreated int64
	Raw         map[string]interface{}
}

func recordsFromMessages(messages []opencodeMessage, parts map[string][]opencodePart) []Record {
	var records []Record
	order := 1
	for _, msg := range messages {
		if msg.Role == "assistant" && msg.Summary {
			continue
		}
		msgParts := parts[msg.ID]
		for _, part := range msgParts {
			rec, ok := recordFromPart(msg, part, order)
			if !ok {
				continue
			}
			records = append(records, rec)
			order++
		}
		if msg.Role == "assistant" && hasUsage(msg.Tokens) {
			records = append(records, Record{
				Order:       order,
				NativeID:    msg.ID + ":usage",
				TimestampMS: firstNonZero(msg.Time.Completed, msg.Time.Created),
				Type:        "token_usage",
				ModelID:     msg.ModelID,
				Tokens:      &msg.Tokens,
			})
			order++
		}
		if len(msgParts) == 0 && len(msg.Error) > 0 {
			records = append(records, Record{
				Order:       order,
				NativeID:    msg.ID + ":error",
				TimestampMS: firstNonZero(msg.Time.Completed, msg.Time.Created),
				Type:        "error",
				Content:     stringFromMap(msg.Error, "message", "name", "type"),
				ModelID:     msg.ModelID,
				Raw:         map[string]interface{}{"message": msg.Raw},
			})
			order++
		}
	}
	return records
}

func recordFromPart(msg opencodeMessage, part opencodePart, order int) (Record, bool) {
	timestamp := firstNonZero(partTimestamp(part), msg.Time.Created)
	nativeID := firstNonEmpty(msg.ID+":"+part.ID, strconv.Itoa(order))
	switch part.Type {
	case "text", "file":
		if strings.TrimSpace(part.Text) == "" {
			return Record{}, false
		}
		recType := "assistant_text"
		if msg.Role == "user" {
			recType = "user_message"
		}
		return Record{Order: order, NativeID: nativeID, TimestampMS: timestamp, Type: recType, Role: msg.Role, Content: part.Text, ModelID: msg.ModelID, Raw: map[string]interface{}{"part": part.Raw}}, true
	case "reasoning":
		if strings.TrimSpace(part.Text) == "" {
			return Record{}, false
		}
		return Record{Order: order, NativeID: nativeID, TimestampMS: timestamp, Type: "agent_reasoning", Role: msg.Role, Content: part.Text, ModelID: msg.ModelID, Raw: map[string]interface{}{"part": part.Raw}}, true
	case "tool":
		return toolRecord(msg, part, order, nativeID, timestamp)
	case "compaction":
		return Record{Order: order, NativeID: nativeID, TimestampMS: timestamp, Type: "compaction", ModelID: msg.ModelID, Raw: map[string]interface{}{"part": part.Raw}}, true
	default:
		return Record{}, false
	}
}

func toolRecord(msg opencodeMessage, part opencodePart, order int, nativeID string, timestamp int64) (Record, bool) {
	state := part.State
	if len(state) == 0 {
		return Record{}, false
	}
	args := mapFromAny(state["input"])
	status := stringFromAny(state["status"])
	if status == "" {
		status = "invoked"
	}
	recType := "tool_result"
	if status != "completed" && status != "error" {
		recType = "tool_call"
	}
	out := state["output"]
	if out == nil {
		out = state["result"]
	}
	if metadata := mapFromAny(state["metadata"]); len(metadata) > 0 {
		out = map[string]interface{}{"output": out, "metadata": metadata}
	}
	ts := timestamp
	if timeMap := mapFromAny(state["time"]); status == "completed" || status == "error" {
		if end := intFromAny(timeMap["end"]); end > 0 {
			ts = normalizeMillis(end)
		}
	} else if timeMap := mapFromAny(state["time"]); len(timeMap) > 0 {
		if start := intFromAny(timeMap["start"]); start > 0 {
			ts = normalizeMillis(start)
		}
	}
	raw := map[string]interface{}{"part": part.Raw}
	if errText := stringFromAny(state["error"]); errText != "" {
		raw["error"] = errText
	}
	return Record{
		Order:       order,
		NativeID:    nativeID,
		TimestampMS: ts,
		Type:        recType,
		ModelID:     msg.ModelID,
		ToolName:    part.Tool,
		CallID:      firstNonEmpty(part.CallID, part.ID),
		Args:        args,
		Output:      out,
		Status:      status,
		Raw:         raw,
	}, true
}

func parseMessageJSON(data string) opencodeMessage {
	var raw map[string]interface{}
	_ = json.Unmarshal([]byte(data), &raw)
	return messageFromMap(raw)
}

func messageFromMap(raw map[string]interface{}) opencodeMessage {
	msg := opencodeMessage{Raw: raw}
	if raw == nil {
		return msg
	}
	msg.ID = stringFromMap(raw, "id")
	msg.SessionID = stringFromMap(raw, "sessionID", "session_id")
	msg.Role = stringFromMap(raw, "role")
	msg.ModelID = opencodeModel(raw)
	msg.Summary = boolFromAny(raw["summary"])
	msg.ParentID = stringFromMap(raw, "parentID", "parent_id")
	msg.Error = mapFromAny(raw["error"])
	timeMap := mapFromAny(raw["time"])
	msg.Time.Created = normalizeMillis(intFromAny(timeMap["created"]))
	msg.Time.Completed = normalizeMillis(intFromAny(timeMap["completed"]))
	msg.Tokens = tokenUsageFromAny(raw["tokens"])
	if cost := floatFromAny(raw["cost"]); cost > 0 {
		msg.Tokens.CostUSD = cost
		msg.Cost = cost
	}
	return msg
}

func parsePartJSON(data string) opencodePart {
	var raw map[string]interface{}
	_ = json.Unmarshal([]byte(data), &raw)
	return partFromMap(raw)
}

func partFromMap(raw map[string]interface{}) opencodePart {
	part := opencodePart{Raw: raw}
	if raw == nil {
		return part
	}
	part.ID = stringFromMap(raw, "id")
	part.SessionID = stringFromMap(raw, "sessionID", "session_id")
	part.MessageID = stringFromMap(raw, "messageID", "message_id")
	part.Type = stringFromMap(raw, "type")
	part.Text = stringFromMap(raw, "text")
	part.Tool = stringFromMap(raw, "tool")
	part.CallID = stringFromMap(raw, "callID", "call_id")
	part.State = mapFromAny(raw["state"])
	timeMap := mapFromAny(raw["time"])
	part.TimeCreated = normalizeMillis(firstNonZero(intFromAny(timeMap["start"]), intFromAny(timeMap["created"])))
	return part
}

func readLegacySessionRef(path, projectDir string) (TraceRef, bool) {
	info, err := sessionfs.Stat(path)
	if err != nil || info.IsDir() || info.Size() > maxSessionBytes {
		return TraceRef{}, false
	}
	session := readLegacySession(path)
	if session.ID == "" {
		return TraceRef{}, false
	}
	if timeMap := mapFromAny(session.Raw["time"]); boolFromAny(timeMap["archived"]) {
		return TraceRef{}, false
	}
	created := firstNonZero(session.Time.Created, info.ModTime().UnixMilli())
	updated := firstNonZero(session.Time.Completed, intFromAny(mapFromAny(session.Raw["time"])["updated"]), created)
	ref := TraceRef{
		ID:              session.ID,
		Kind:            SourceLegacy,
		SourcePath:      path,
		Title:           firstNonEmpty(stringFromMap(session.Raw, "title"), "Untitled Session"),
		Preview:         firstNonEmpty(stringFromMap(session.Raw, "title"), "(No preview)"),
		Directory:       firstNonEmpty(projectDir, stringFromMap(session.Raw, "directory")),
		CreatedAtUnixMS: normalizeMillis(created),
		UpdatedAtUnixMS: normalizeMillis(updated),
		SizeBytes:       info.Size(),
	}
	if parent := stringFromMap(session.Raw, "parentID", "parent_id"); parent != "" {
		ref.RelatedTo = parent
		ref.RelationshipType = "subagent"
	}
	return ref, true
}

func readLegacySession(path string) opencodeMessage {
	data, err := sessionfs.ReadFile(path)
	if err != nil {
		return opencodeMessage{}
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return opencodeMessage{}
	}
	msg := messageFromMap(raw)
	msg.ID = firstNonEmpty(msg.ID, strings.TrimSuffix(filepath.Base(path), ".json"))
	return msg
}

func readLegacyMessage(path string) opencodeMessage {
	data, err := sessionfs.ReadFile(path)
	if err != nil {
		return opencodeMessage{}
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return opencodeMessage{}
	}
	msg := messageFromMap(raw)
	msg.ID = firstNonEmpty(msg.ID, strings.TrimSuffix(filepath.Base(path), ".json"))
	return msg
}

func readLegacyPart(path string) opencodePart {
	data, err := sessionfs.ReadFile(path)
	if err != nil {
		return opencodePart{}
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return opencodePart{}
	}
	part := partFromMap(raw)
	part.ID = firstNonEmpty(part.ID, strings.TrimSuffix(filepath.Base(path), ".json"))
	return part
}

func legacyProjectDirectory(path string) string {
	data, err := sessionfs.ReadFile(path)
	if err != nil {
		return ""
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	return firstNonEmpty(stringFromMap(raw, "worktree"), stringFromMap(raw, "directory"), stringFromMap(raw, "name"))
}

// openSQLiteReadOnly opens OpenCode's store read-only. modernc.org/sqlite opens every database
// READWRITE|CREATE, so mode=ro in the URI is the one thing keeping this read from writing to it.
func openSQLiteReadOnly(path string) (*sql.DB, error) {
	path, err := sessionfs.SQLitePath(path)
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite", sqliteuri.File(path, url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)"}}))
}

func sqliteTableExists(db *sql.DB, table string) bool {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name)
	return err == nil
}

func sqliteColumns(db *sql.DB, table string) map[string]bool {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err == nil {
			cols[name] = true
		}
	}
	return cols
}

func sqliteSelectColumn(cols map[string]bool, name string) string {
	if cols[name] {
		return name
	}
	return "NULL AS " + name
}

func fileExists(path string) bool {
	info, err := sessionfs.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirExists(path string) bool {
	info, err := sessionfs.Stat(path)
	return err == nil && info.IsDir()
}

func nullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func nullInt(value sql.NullInt64) int64 {
	if value.Valid {
		return value.Int64
	}
	return 0
}

func normalizeMillis(value int64) int64 {
	if value > 0 && value < 1000000000000 {
		return value * 1000
	}
	return value
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func naturalLess(a, b string) bool {
	ai := firstNumber(a)
	bi := firstNumber(b)
	if ai != bi {
		return ai < bi
	}
	return a < b
}

func firstNumber(value string) int {
	start := -1
	for i, r := range value {
		if r >= '0' && r <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			n, _ := strconv.Atoi(value[start:i])
			return n
		}
	}
	if start >= 0 {
		n, _ := strconv.Atoi(value[start:])
		return n
	}
	return 0
}

func partTimestamp(part opencodePart) int64 {
	if part.TimeCreated > 0 {
		return part.TimeCreated
	}
	if timeMap := mapFromAny(part.Raw["time"]); len(timeMap) > 0 {
		return normalizeMillis(firstNonZero(intFromAny(timeMap["start"]), intFromAny(timeMap["created"])))
	}
	return 0
}
