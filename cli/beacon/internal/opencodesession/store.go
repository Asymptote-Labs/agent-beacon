package opencodesession

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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

func NewStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = DefaultDataDir()
	}
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("opencode data directory is empty")
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

func DefaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	switch runtime.GOOS {
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "opencode")
		}
		return filepath.Join(home, "AppData", "Roaming", "opencode")
	default:
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			return filepath.Join(xdg, "opencode")
		}
		return filepath.Join(home, ".config", "opencode")
	}
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
	if !sqliteTableExists(db, "session") {
		return nil, nil
	}
	cols := sqliteColumns(db, "session")
	selects := []string{
		sqliteSelectColumn(cols, "id"),
		sqliteSelectColumn(cols, "title"),
		sqliteSelectColumn(cols, "directory"),
		sqliteSelectColumn(cols, "project_worktree"),
		sqliteSelectColumn(cols, "time_created"),
		sqliteSelectColumn(cols, "time_updated"),
		sqliteSelectColumn(cols, "parent_id"),
	}
	rows, err := db.Query("SELECT "+strings.Join(selects, ", ")+" FROM session ORDER BY COALESCE(time_updated, time_created, 0) DESC LIMIT ?", defaultListLimit)
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
		if info, err := os.Stat(s.SQLitePath); err == nil {
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
	if !sqliteTableExists(db, "message") || !sqliteTableExists(db, "part") {
		return nil, nil
	}
	messages, err := readSQLiteMessages(db, ref.ID)
	if err != nil {
		return nil, err
	}
	parts, err := readSQLiteParts(db, ref.ID)
	if err != nil {
		return nil, err
	}
	return recordsFromMessages(messages, parts), nil
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

func (s *Store) listLegacy() ([]TraceRef, error) {
	sessionRoot := filepath.Join(s.LegacyStorageDir, "session")
	if !dirExists(sessionRoot) {
		return nil, nil
	}
	projects, err := os.ReadDir(sessionRoot)
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
		files, err := os.ReadDir(sessionDir)
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
	files, err := os.ReadDir(messageDir)
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
		partFiles, err := os.ReadDir(partDir)
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
	info, err := os.Stat(path)
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
	data, err := os.ReadFile(path)
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
	data, err := os.ReadFile(path)
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
	data, err := os.ReadFile(path)
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
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	return firstNonEmpty(stringFromMap(raw, "worktree"), stringFromMap(raw, "directory"), stringFromMap(raw, "name"))
}

func openSQLiteReadOnly(path string) (*sql.DB, error) {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	return sql.Open("sqlite", u.String())
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
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
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
