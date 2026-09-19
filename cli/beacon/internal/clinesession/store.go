package clinesession

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	historyFileName      = "api_conversation_history.json"
	maxTaskDirsScanned   = 10000
	maxHistoryFileBytes  = 25 << 20
	clineVSCodeExtension = "saoudrizwan.claude-dev"
)

type Store struct {
	ClineDir            string
	TasksDirs           []string
	SessionsDir         string
	KanbanWorkspacesDir string
}

func NewStore(clineDir string) (*Store, error) {
	if strings.TrimSpace(clineDir) == "" {
		clineDir = DefaultClineDir()
	}
	if strings.TrimSpace(clineDir) == "" {
		return nil, errors.New("Cline directory is empty")
	}
	clineDir = filepath.Clean(clineDir)
	seen := map[string]bool{}
	var taskDirs []string
	add := func(path string) {
		if strings.TrimSpace(path) == "" || seen[path] {
			return
		}
		seen[path] = true
		taskDirs = append(taskDirs, path)
	}
	add(filepath.Join(clineDir, "data", "tasks"))
	add(filepath.Join(clineDir, "tasks"))
	if vs := DefaultClineVSCodeStorageDir(); vs != "" {
		add(filepath.Join(vs, "tasks"))
	}
	return &Store{
		ClineDir:            clineDir,
		TasksDirs:           taskDirs,
		SessionsDir:         filepath.Join(clineDir, "data", "sessions"),
		KanbanWorkspacesDir: filepath.Join(clineDir, "kanban", "workspaces"),
	}, nil
}

func DefaultClineDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".cline")
}

func DefaultClineVSCodeStorageDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage", clineVSCodeExtension)
	case "windows":
		base := strings.TrimSpace(os.Getenv("APPDATA"))
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "Code", "User", "globalStorage", clineVSCodeExtension)
	default:
		base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "Code", "User", "globalStorage", clineVSCodeExtension)
	}
}

func (s *Store) Exists() bool {
	if dirExists(s.SessionsDir) || dirExists(s.KanbanWorkspacesDir) {
		return true
	}
	for _, dir := range s.TasksDirs {
		if dirExists(dir) {
			return true
		}
	}
	return false
}

func (s *Store) List() ([]TraceRef, error) {
	byID := map[string]TraceRef{}
	s.collectSessionRefs(byID)
	s.collectHistoryRefs(byID)
	s.collectKanbanRefs(byID)
	refs := make([]TraceRef, 0, len(byID))
	for _, ref := range byID {
		refs = append(refs, ref)
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].UpdatedAtUnixMS != refs[j].UpdatedAtUnixMS {
			return refs[i].UpdatedAtUnixMS < refs[j].UpdatedAtUnixMS
		}
		return refs[i].ID < refs[j].ID
	})
	return refs, nil
}

func (s *Store) Read(ref TraceRef) ([]Record, error) {
	if ref.Kind == SourceKanban {
		return s.readKanban(ref)
	}
	entries, err := s.ReadEntries(ref.SourcePath)
	if err != nil {
		return nil, err
	}
	return RecordsFromEntries(ref, entries), nil
}

func (s *Store) ReadEntries(path string) ([]Entry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxHistoryFileBytes {
		return nil, fmt.Errorf("Cline history %s is %d bytes, over the %d-byte limit", path, info.Size(), maxHistoryFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse Cline history %s: %w", path, err)
	}
	items := historyEntriesFromParsed(parsed)
	entries := make([]Entry, 0, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]interface{})
		if !ok || obj == nil {
			continue
		}
		entries = append(entries, parseEntry(obj, i+1))
	}
	return entries, nil
}

func (s *Store) collectHistoryRefs(out map[string]TraceRef) {
	for _, root := range s.TasksDirs {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		count := 0
		for _, entry := range entries {
			if count >= maxTaskDirsScanned {
				break
			}
			if !entry.IsDir() {
				continue
			}
			count++
			id := entry.Name()
			if !validPathSegment(id) {
				continue
			}
			traceDir := filepath.Join(root, id)
			source := filepath.Join(traceDir, historyFileName)
			ref, ok := s.buildHistoryRef(id, source, traceDir, SourceHistory, "")
			if !ok {
				continue
			}
			if prev, exists := out[ref.ID]; !exists || ref.UpdatedAtUnixMS > prev.UpdatedAtUnixMS {
				out[ref.ID] = ref
			}
		}
	}
}

func (s *Store) collectSessionRefs(out map[string]TraceRef) {
	entries, err := os.ReadDir(s.SessionsDir)
	if err != nil {
		return
	}
	count := 0
	for _, entry := range entries {
		if count >= maxTaskDirsScanned {
			break
		}
		if !entry.IsDir() || !validPathSegment(entry.Name()) {
			continue
		}
		count++
		leadID := entry.Name()
		dir := filepath.Join(s.SessionsDir, leadID)
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		meta := readJSONRecord(filepath.Join(dir, leadID+".json"))
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".messages.json") {
				continue
			}
			base := strings.TrimSuffix(file.Name(), ".messages.json")
			if !validPathSegment(base) {
				continue
			}
			source := filepath.Join(dir, file.Name())
			id := leadID
			relatedTo := ""
			if base != leadID {
				id = "subagent:" + leadID + ":" + base
				relatedTo = leadID
			}
			ref, ok := s.buildHistoryRef(id, source, dir, SourceMessages, relatedTo)
			if !ok {
				continue
			}
			if title := normalizeTitleCandidate(stringValue(meta["task"])); title != "" && relatedTo == "" {
				ref.Title = title
				ref.Preview = title
			}
			if relatedTo != "" {
				ref.RelationshipType = "subagent"
			}
			if dir := extractDirectoryFromRecord(meta); dir != "" {
				ref.Directory = dir
			}
			if prev, exists := out[ref.ID]; !exists || ref.UpdatedAtUnixMS > prev.UpdatedAtUnixMS {
				out[ref.ID] = ref
			}
		}
	}
}

func (s *Store) collectKanbanRefs(out map[string]TraceRef) {
	workspaces, err := os.ReadDir(s.KanbanWorkspacesDir)
	if err != nil {
		return
	}
	for _, workspace := range workspaces {
		if !workspace.IsDir() || !validPathSegment(workspace.Name()) {
			continue
		}
		workspaceDir := filepath.Join(s.KanbanWorkspacesDir, workspace.Name())
		sessionsPath := filepath.Join(workspaceDir, "sessions.json")
		sessions := readJSONRecord(sessionsPath)
		if sessions == nil {
			continue
		}
		board := readJSONRecord(filepath.Join(workspaceDir, "board.json"))
		count := 0
		for taskID, raw := range sessions {
			if count >= maxTaskDirsScanned {
				break
			}
			if !validPathSegment(taskID) || strings.HasPrefix(taskID, "__home_agent__:") {
				continue
			}
			session, ok := raw.(map[string]interface{})
			if !ok || session == nil {
				continue
			}
			count++
			card := findKanbanCard(board, taskID)
			prompt := stringValue(card["prompt"])
			title := normalizeTitleCandidate(prompt)
			if title == "" {
				title = "Task " + truncate(taskID, 12)
			}
			updated := parseTimestampMS(firstNonNil(session["updatedAt"], session["lastOutputAt"], session["startedAt"], card["updatedAt"], card["createdAt"]), 0)
			if updated == 0 {
				updated = newestPathMtimeMS(sessionsPath, filepath.Join(workspaceDir, "board.json"))
			}
			ref := TraceRef{
				ID:              "kanban:" + workspace.Name() + ":" + taskID,
				Kind:            SourceKanban,
				SourcePath:      sessionsPath,
				Title:           title,
				Preview:         firstNonEmpty(extractKanbanSummary(session), extractKanbanError(session), prompt, title),
				Directory:       firstNonEmpty(normalizeDirectory(stringValue(session["workspacePath"])), normalizeDirectory(stringValue(session["cwd"]))),
				UpdatedAtUnixMS: updated,
			}
			if info, err := os.Stat(sessionsPath); err == nil {
				ref.SizeBytes = info.Size()
			}
			if prev, exists := out[ref.ID]; !exists || ref.UpdatedAtUnixMS > prev.UpdatedAtUnixMS {
				out[ref.ID] = ref
			}
		}
	}
}

func (s *Store) readKanban(ref TraceRef) ([]Record, error) {
	workspaceID, taskID, ok := parseKanbanID(ref.ID)
	if !ok {
		return nil, fmt.Errorf("invalid Cline kanban trace id %q", ref.ID)
	}
	workspaceDir := filepath.Join(s.KanbanWorkspacesDir, workspaceID)
	sessions := readJSONRecord(filepath.Join(workspaceDir, "sessions.json"))
	session, _ := sessions[taskID].(map[string]interface{})
	if session == nil {
		return nil, fmt.Errorf("Cline kanban task %s not found", taskID)
	}
	card := findKanbanCard(readJSONRecord(filepath.Join(workspaceDir, "board.json")), taskID)
	created := parseTimestampMS(firstNonNil(session["startedAt"], card["createdAt"]), ref.UpdatedAtUnixMS)
	updated := parseTimestampMS(firstNonNil(session["updatedAt"], session["lastOutputAt"], card["updatedAt"], card["createdAt"]), ref.UpdatedAtUnixMS)
	model := firstNonEmpty(stringValue(session["modelId"]), stringValue(session["model"]))
	order := 1
	var records []Record
	if prompt := stringValue(card["prompt"]); strings.TrimSpace(prompt) != "" {
		records = append(records, Record{Order: order, Type: "user_message", TimestampMS: created, Content: prompt, ModelID: model, Raw: map[string]interface{}{"session": session, "card": card}})
		order++
	}
	if summary := extractKanbanSummary(session); summary != "" {
		records = append(records, Record{Order: order, Type: "agent_text", TimestampMS: updated, Content: summary, ModelID: model, Raw: map[string]interface{}{"session": session, "card": card}})
		order++
	}
	if errText := extractKanbanError(session); errText != "" {
		records = append(records, Record{Order: order, Type: "agent_text", TimestampMS: updated, Content: errText, ModelID: model, Raw: map[string]interface{}{"session": session, "card": card}})
	}
	return records, nil
}

func parseKanbanID(id string) (string, string, bool) {
	if !strings.HasPrefix(id, "kanban:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(id, "kanban:")
	idx := strings.Index(rest, ":")
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

func findKanbanCard(board map[string]interface{}, taskID string) map[string]interface{} {
	columns, _ := board["columns"].([]interface{})
	for _, rawColumn := range columns {
		column, _ := rawColumn.(map[string]interface{})
		cards, _ := column["cards"].([]interface{})
		for _, rawCard := range cards {
			card, _ := rawCard.(map[string]interface{})
			if card != nil && stringValue(card["id"]) == taskID {
				return card
			}
		}
	}
	return nil
}

func extractKanbanSummary(session map[string]interface{}) string {
	activity, _ := session["latestHookActivity"].(map[string]interface{})
	if text := firstNonEmpty(stringValue(activity["finalMessage"]), stringValue(activity["activityText"])); text != "" {
		return text
	}
	state := strings.ReplaceAll(stringValue(session["state"]), "_", " ")
	if state == "" {
		return ""
	}
	parts := []string{strings.ToUpper(state[:1]) + state[1:]}
	if reason := stringValue(session["reviewReason"]); reason != "" {
		parts = append(parts, "("+reason+")")
	}
	if agent := stringValue(session["agentId"]); agent != "" {
		parts = append(parts, "via "+agent)
	}
	return strings.Join(parts, " ")
}

func extractKanbanError(session map[string]interface{}) string {
	if text := stringValue(session["warningMessage"]); text != "" {
		return text
	}
	state := strings.ToLower(stringValue(session["state"]))
	reason := strings.ToLower(stringValue(session["reviewReason"]))
	if state == "failed" || reason == "error" {
		return "Session failed."
	}
	if state == "interrupted" || reason == "interrupted" {
		return "Session interrupted."
	}
	return ""
}

func newestPathMtimeMS(paths ...string) int64 {
	var latest int64
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.ModTime().UnixMilli() > latest {
			latest = info.ModTime().UnixMilli()
		}
	}
	return latest
}

func (s *Store) buildHistoryRef(id, source, traceDir, kind, relatedTo string) (TraceRef, bool) {
	info, err := os.Stat(source)
	if err != nil || info.IsDir() {
		return TraceRef{}, false
	}
	entries, err := s.ReadEntries(source)
	if err != nil || len(entries) == 0 {
		return TraceRef{}, false
	}
	title := extractTitle(entries, id)
	ref := TraceRef{
		ID:              id,
		Kind:            kind,
		SourcePath:      source,
		Title:           title,
		Preview:         title,
		Directory:       resolveTaskDirectory(traceDir, entries),
		UpdatedAtUnixMS: resolveTraceTimestamp(entries, source),
		SizeBytes:       info.Size(),
		RelatedTo:       relatedTo,
	}
	if ref.UpdatedAtUnixMS == 0 {
		ref.UpdatedAtUnixMS = info.ModTime().UnixMilli()
	}
	return ref, true
}

func historyEntriesFromParsed(parsed interface{}) []interface{} {
	if items, ok := parsed.([]interface{}); ok {
		return items
	}
	obj, _ := parsed.(map[string]interface{})
	if obj == nil {
		return nil
	}
	for _, key := range []string{"messages", "history", "entries"} {
		if items, ok := obj[key].([]interface{}); ok {
			return items
		}
	}
	return nil
}

func extractTitle(entries []Entry, fallback string) string {
	var first string
	for _, entry := range entries {
		if strings.ToLower(strings.TrimSpace(entry.Role)) != "user" {
			continue
		}
		text := extractVisibleText("user", firstNonEmpty(extractText(entry.Content), entry.Text, entry.Message))
		if text == "" {
			continue
		}
		title := normalizeTitleCandidate(text)
		if title == "" {
			continue
		}
		if first == "" {
			first = title
		}
		if !isLowSignalTitle(title) {
			return title
		}
	}
	if first != "" {
		return first
	}
	return "Task " + truncate(fallback, 12)
}

func normalizeTitleCandidate(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return truncate(text, 80)
}

func isLowSignalTitle(text string) bool {
	value := strings.ToLower(strings.TrimSpace(text))
	if value == "" {
		return true
	}
	low := map[string]bool{"hi": true, "hey": true, "hello": true, "yo": true, "thanks": true, "thank you": true, "ok": true, "okay": true, "continue": true, "please continue": true, "keep going": true, "resume": true, "retry": true}
	return low[value]
}

func resolveTraceTimestamp(entries []Entry, source string) int64 {
	var latest int64
	for _, entry := range entries {
		if ts := parseTimestampMS(entry.Timestamp, 0); ts > latest {
			latest = ts
		}
	}
	if latest > 0 {
		return latest
	}
	if info, err := os.Stat(source); err == nil {
		return info.ModTime().UnixMilli()
	}
	return time.Now().UnixMilli()
}

func resolveTaskDirectory(traceDir string, entries []Entry) string {
	for _, name := range []string{"task_metadata.json", "metadata.json", "task.json"} {
		if dir := extractDirectoryFromRecord(readJSONRecord(filepath.Join(traceDir, name))); dir != "" {
			return dir
		}
	}
	for _, entry := range entries {
		if dir := extractDirectoryFromRecord(entry.Raw); dir != "" {
			return dir
		}
		if dir := extractWorkingDirectoryFromText(firstNonEmpty(extractText(entry.Content), entry.Text, entry.Message)); dir != "" {
			return dir
		}
	}
	return ""
}

func extractDirectoryFromRecord(record map[string]interface{}) string {
	if record == nil {
		return ""
	}
	candidates := []map[string]interface{}{record}
	for _, key := range []string{"metadata", "environment", "workspace"} {
		if nested, ok := record[key].(map[string]interface{}); ok {
			candidates = append(candidates, nested)
		}
	}
	for _, candidate := range candidates {
		for _, key := range []string{"cwd", "currentWorkingDirectory", "workingDirectory", "workspacePath", "workspaceRoot", "workspace_root", "projectPath", "projectDir", "root"} {
			if dir := normalizeDirectory(stringValue(candidate[key])); dir != "" {
				return dir
			}
		}
		if dir := extractWorkingDirectoryFromText(firstNonEmpty(extractText(candidate["content"]), stringValue(candidate["text"]), stringValue(candidate["message"]))); dir != "" {
			return dir
		}
	}
	return ""
}

func extractWorkingDirectoryFromText(text string) string {
	if text == "" {
		return ""
	}
	marker := "# Current Working Directory ("
	if idx := strings.Index(text, marker); idx >= 0 {
		rest := text[idx+len(marker):]
		if end := strings.Index(rest, ")"); end >= 0 {
			return normalizeDirectory(rest[:end])
		}
	}
	lower := strings.ToLower(text)
	needle := "current working directory is now "
	if idx := strings.Index(lower, needle); idx >= 0 {
		rest := strings.TrimSpace(text[idx+len(needle):])
		rest = strings.Trim(rest, "'\"` ")
		if end := strings.IndexAny(rest, "\n\r"); end >= 0 {
			rest = rest[:end]
		}
		return normalizeDirectory(rest)
	}
	return ""
}

func normalizeDirectory(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "file://") {
		if parsed, err := url.Parse(value); err == nil && parsed.Scheme == "file" {
			return parsed.Path
		}
		return ""
	}
	return value
}

func readJSONRecord(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func validPathSegment(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func readDirNames(path string) ([]fs.DirEntry, error) { return os.ReadDir(path) }
