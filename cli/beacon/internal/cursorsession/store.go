package cursorsession

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
	"strings"

	_ "modernc.org/sqlite"
)

const (
	composerDataPrefix = "composerData:"
	composerDataEnd    = "composerData;"
	bubblePrefix       = "bubbleId:"
	contentPrefix      = "composer.content."
)

type Store struct {
	GlobalDBPath string
	ProjectsDir  string
}

func NewStore(globalDBPath, projectsDir string) (*Store, error) {
	if strings.TrimSpace(globalDBPath) == "" {
		globalDBPath = DefaultGlobalDBPath()
	}
	if strings.TrimSpace(projectsDir) == "" {
		projectsDir = DefaultProjectsDir()
	}
	return &Store{GlobalDBPath: globalDBPath, ProjectsDir: projectsDir}, nil
}

func DefaultGlobalDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "Cursor", "User", "globalStorage", "state.vscdb")
		}
		return filepath.Join(home, "AppData", "Roaming", "Cursor", "User", "globalStorage", "state.vscdb")
	default:
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			return filepath.Join(xdg, "Cursor", "User", "globalStorage", "state.vscdb")
		}
		return filepath.Join(home, ".config", "Cursor", "User", "globalStorage", "state.vscdb")
	}
}

func DefaultProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".cursor", "projects")
}

func (s *Store) Exists() bool {
	return fileExists(s.GlobalDBPath) || dirExists(s.ProjectsDir)
}

func (s *Store) List() ([]TraceRef, error) {
	var refs []TraceRef
	var errs []error
	global, err := s.listGlobalStorage()
	if err != nil {
		errs = append(errs, err)
	}
	refs = append(refs, global...)
	transcripts, err := s.listTranscripts()
	if err != nil {
		errs = append(errs, err)
	}
	refs = append(refs, transcripts...)
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].UpdatedAtUnixMS != refs[j].UpdatedAtUnixMS {
			return refs[i].UpdatedAtUnixMS < refs[j].UpdatedAtUnixMS
		}
		return refs[i].ID < refs[j].ID
	})
	return refs, errors.Join(errs...)
}

func (s *Store) listGlobalStorage() ([]TraceRef, error) {
	if !fileExists(s.GlobalDBPath) {
		return nil, nil
	}
	db, err := openSQLiteReadOnly(s.GlobalDBPath)
	if err != nil {
		return nil, fmt.Errorf("open Cursor global storage: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT key, value FROM cursorDiskKV WHERE key >= ? AND key < ? AND LENGTH(value) > 10`, composerDataPrefix, composerDataEnd)
	if err != nil {
		return nil, fmt.Errorf("read Cursor composers: %w", err)
	}
	defer rows.Close()

	var refs []TraceRef
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		id := strings.TrimPrefix(key, composerDataPrefix)
		id = normalizeTraceID(id)
		if id == "" {
			continue
		}
		var composer composerData
		if err := json.Unmarshal(value, &composer); err != nil {
			continue
		}
		if len(composer.FullConversationHeadersOnly) == 0 {
			continue
		}
		updated := composer.LastUpdatedAt.Millis
		if updated == 0 {
			updated = composer.CreatedAt.Millis
		}
		preview := composerPreview(composer)
		refs = append(refs, TraceRef{
			ID:              id,
			Kind:            SourceGlobalStorage,
			SourcePath:      "global:" + id,
			Title:           firstNonEmpty(composer.Name, preview),
			Preview:         preview,
			Workspace:       s.resolveComposerWorkspace(db, id, composer),
			UpdatedAtUnixMS: updated,
			CreatedAtUnixMS: composer.CreatedAt.Millis,
			SizeBytes:       int64(len(value)),
		})
	}
	return refs, rows.Err()
}

func (s *Store) listTranscripts() ([]TraceRef, error) {
	if !dirExists(s.ProjectsDir) {
		return nil, nil
	}
	projectDirs, err := os.ReadDir(s.ProjectsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Cursor projects directory: %w", err)
	}
	var refs []TraceRef
	for _, project := range projectDirs {
		if !project.IsDir() {
			continue
		}
		projectPath := filepath.Join(s.ProjectsDir, project.Name())
		transcriptDir := filepath.Join(projectPath, "agent-transcripts")
		for _, entry := range listTranscriptFiles(transcriptDir) {
			preview := transcriptPreview(entry.Path)
			refs = append(refs, TraceRef{
				ID:               normalizeTraceID(entry.TraceID),
				Kind:             SourceTranscript,
				SourcePath:       entry.Path,
				Title:            firstNonEmpty(preview, "Cursor session "+shortID(entry.TraceID)),
				Preview:          preview,
				Workspace:        resolveDashEncodedProjectDirectory(project.Name()),
				UpdatedAtUnixMS:  entry.Info.ModTime().UnixMilli(),
				CreatedAtUnixMS:  birthUnixMS(entry.Path, entry.Info.ModTime().UnixMilli()),
				SizeBytes:        entry.Info.Size(),
				RelatedTo:        entry.RelatedTo,
				RelationshipType: entry.RelationshipType,
			})
		}
	}
	return refs, nil
}

type transcriptEntry struct {
	TraceID          string
	Path             string
	Info             os.FileInfo
	RelatedTo        string
	RelationshipType string
}

func listTranscriptFiles(dir string) []transcriptEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []transcriptEntry
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() && isTranscriptFilename(entry.Name()) {
			out = append(out, transcriptEntry{TraceID: traceIDFromFilename(entry.Name()), Path: path, Info: info})
			continue
		}
		if !entry.IsDir() {
			continue
		}
		children, err := os.ReadDir(path)
		if err == nil {
			for _, child := range children {
				childPath := filepath.Join(path, child.Name())
				childInfo, err := child.Info()
				if err == nil && childInfo.Mode().IsRegular() && isTranscriptFilename(child.Name()) {
					out = append(out, transcriptEntry{TraceID: traceIDFromFilename(child.Name()), Path: childPath, Info: childInfo})
				}
			}
		}
		out = append(out, listSubagentTranscriptEntries(filepath.Join(path, "subagents"), normalizeTraceID(entry.Name()))...)
	}
	return out
}

func listSubagentTranscriptEntries(dir, parentID string) []transcriptEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []transcriptEntry
	for _, entry := range entries {
		if !isTranscriptFilename(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, transcriptEntry{
			TraceID:          traceIDFromFilename(entry.Name()),
			Path:             path,
			Info:             info,
			RelatedTo:        parentID,
			RelationshipType: "subagent",
		})
	}
	return out
}

func (s *Store) Read(ref TraceRef) ([]Record, error) {
	switch ref.Kind {
	case SourceGlobalStorage:
		return s.readGlobalStorage(ref)
	case SourceTranscript:
		return s.readTranscript(ref)
	default:
		return nil, nil
	}
}

func (s *Store) readGlobalStorage(ref TraceRef) ([]Record, error) {
	db, err := openSQLiteReadOnly(s.GlobalDBPath)
	if err != nil {
		return nil, fmt.Errorf("open Cursor global storage: %w", err)
	}
	defer db.Close()
	id := strings.TrimPrefix(ref.SourcePath, "global:")
	if id == "" {
		id = ref.ID
	}
	var raw []byte
	if err := db.QueryRow(`SELECT value FROM cursorDiskKV WHERE key = ?`, composerDataPrefix+id).Scan(&raw); err != nil {
		return nil, err
	}
	var composer composerData
	if err := json.Unmarshal(raw, &composer); err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(composer.FullConversationHeadersOnly))
	model := ""
	if composer.ModelConfig != nil {
		model = composer.ModelConfig.ModelName
	}
	order := 1
	for _, header := range composer.FullConversationHeadersOnly {
		bubble, ok := readBubble(db, id, header.BubbleID)
		if !ok {
			continue
		}
		ts := bubble.CreatedAt.Millis
		duration := bubble.TurnDurationMS
		used, limit := contextFromAny(bubble, bubble.Context)
		switch bubble.Type {
		case 1:
			if strings.TrimSpace(bubble.Text) != "" {
				records = append(records, Record{Order: order, NativeID: header.BubbleID, Type: "user_message", TimestampMS: ts, ContextUsedTokens: used, ContextLimitTokens: limit, Content: bubble.Text})
				order++
			}
		case 2:
			for i, block := range bubble.AllThinkingBlocks {
				if strings.TrimSpace(block.Thinking) == "" {
					continue
				}
				records = append(records, Record{Order: order, NativeID: fmt.Sprintf("%s:thinking:%d", header.BubbleID, i), Type: "agent_thinking", TimestampMS: ts, DurationMS: duration, ContextUsedTokens: used, ContextLimitTokens: limit, Content: block.Thinking})
				order++
			}
			if bubble.Thinking != nil && strings.TrimSpace(bubble.Thinking.Text) != "" {
				records = append(records, Record{Order: order, NativeID: header.BubbleID + ":thinking", Type: "agent_thinking", TimestampMS: ts, DurationMS: bubble.ThinkingDurationMS, ContextUsedTokens: used, ContextLimitTokens: limit, Content: bubble.Thinking.Text})
				order++
			}
			if bubble.ToolFormerData != nil {
				callID := fmt.Sprintf("%s:tool", header.BubbleID)
				args := decodeArgs(bubble.ToolFormerData.Params)
				if bubble.ToolFormerData.Name == "edit_file_v2" {
					args = s.enrichEditFileV2Args(db, args, bubble.ToolFormerData.Result)
				}
				records = append(records, Record{Order: order, NativeID: header.BubbleID + ":tool_call", Type: "tool_call", TimestampMS: ts, DurationMS: duration, ContextUsedTokens: used, ContextLimitTokens: limit, CallID: callID, ToolName: firstNonEmpty(bubble.ToolFormerData.Name, "unknown_tool"), Args: args})
				order++
				if output := formatToolFormerResult(bubble.ToolFormerData); output != "" {
					status := "success"
					if bubble.ToolFormerData.Status == "error" {
						status = "error"
					}
					records = append(records, Record{Order: order, NativeID: header.BubbleID + ":tool_result", Type: "tool_result", TimestampMS: ts, DurationMS: duration, ContextUsedTokens: used, ContextLimitTokens: limit, CallID: callID, ToolName: firstNonEmpty(bubble.ToolFormerData.Name, "unknown_tool"), Args: args, Output: output, Status: status})
					order++
				}
			} else if strings.TrimSpace(bubble.Text) != "" {
				records = append(records, Record{Order: order, NativeID: header.BubbleID, Type: "agent_text", TimestampMS: ts, DurationMS: duration, ContextUsedTokens: used, ContextLimitTokens: limit, Content: bubble.Text, Model: model})
				order++
			}
			if bubble.ErrorDetails != nil && strings.TrimSpace(bubble.ErrorDetails.Message) != "" {
				msg := bubble.ErrorDetails.Message
				if strings.TrimSpace(bubble.ErrorDetails.Title) != "" {
					msg = bubble.ErrorDetails.Title + ": " + msg
				}
				records = append(records, Record{Order: order, NativeID: header.BubbleID + ":error", Type: "error", TimestampMS: ts, DurationMS: duration, ContextUsedTokens: used, ContextLimitTokens: limit, Content: msg})
				order++
			}
		}
	}
	if summary := compactionSummary(composer); summary != nil {
		used, limit := contextFromAny(composer, composer.Context, summary)
		records = append(records, Record{Order: order, NativeID: "compaction:" + stringValue(summary["compacted_through_id"]), Type: "system_event", Subtype: "compaction", TimestampMS: ref.UpdatedAtUnixMS, ContextUsedTokens: used, ContextLimitTokens: limit, Data: summary})
	}
	return records, nil
}

func readBubble(db *sql.DB, composerID, bubbleID string) (bubbleData, bool) {
	var raw []byte
	err := db.QueryRow(`SELECT value FROM cursorDiskKV WHERE key = ?`, bubblePrefix+composerID+":"+bubbleID).Scan(&raw)
	if err != nil {
		return bubbleData{}, false
	}
	var bubble bubbleData
	if err := json.Unmarshal(raw, &bubble); err != nil {
		return bubbleData{}, false
	}
	return bubble, true
}

func (s *Store) readTranscript(ref TraceRef) ([]Record, error) {
	data, err := os.ReadFile(ref.SourcePath)
	if err != nil {
		return nil, err
	}
	records := parseTranscriptRecords(data)
	out := make([]Record, 0, len(records))
	order := 1
	pending := map[string]Record{}
	for i, rec := range records {
		ts := parseTranscriptTimestamp(rec, ref.UpdatedAtUnixMS)
		model := firstNonEmpty(pickString(rec, "modelId", "model", "newModel"), pickNestedString(rec, "data", "model"), pickNestedString(rec, "data", "modelId"))
		typ := strings.ToLower(pickString(rec, "type"))
		role := normalizeRole(pickString(rec, "role"))
		baseID := firstNonEmpty(pickString(rec, "id"), fmt.Sprintf("entry-%d", i+1))
		used, limit := contextFromAny(rec, rec["data"], rec["message"])
		before := len(out)
		switch {
		case typ == "message":
			msg := asMap(rec["message"])
			if msg == nil {
				msg = map[string]interface{}{
					"role":    role,
					"text":    firstNonEmpty(pickString(rec, "text"), stringFrom(rec["message"])),
					"content": rec["content"],
					"model":   model,
				}
			}
			order = appendTranscriptMessage(outPtr(&out), pending, msg, baseID, ts, role, model, order)
		case typ == "user_message":
			msg := map[string]interface{}{"role": "user", "text": firstNonEmpty(pickString(rec, "text"), stringFrom(rec["message"])), "content": rec["content"]}
			order = appendTranscriptMessage(outPtr(&out), pending, msg, baseID, ts, "user", model, order)
		case typ == "tool_call" || typ == "tool_use" || typ == "function_call":
			call := asMap(firstNonNil(rec["toolCall"], rec["functionCall"], rec["data"], rec))
			r := transcriptToolCall(call, baseID, ts, order)
			pending[r.CallID] = r
			out = append(out, r)
			order++
		case typ == "tool_result" || typ == "tool_output" || typ == "function_call_output":
			result := asMap(firstNonNil(rec["toolResult"], rec["functionResult"], rec["data"], rec))
			r := transcriptToolResult(result, baseID, ts, order, pending)
			out = append(out, r)
			order++
		}
		if len(out) == before && role != "" {
			order = appendTranscriptMessage(outPtr(&out), pending, rec, baseID, ts, role, model, order)
		}
		if used > 0 || limit > 0 {
			for idx := before; idx < len(out); idx++ {
				if out[idx].ContextUsedTokens == 0 {
					out[idx].ContextUsedTokens = used
				}
				if out[idx].ContextLimitTokens == 0 {
					out[idx].ContextLimitTokens = limit
				}
			}
		}
	}
	return out, nil
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

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
