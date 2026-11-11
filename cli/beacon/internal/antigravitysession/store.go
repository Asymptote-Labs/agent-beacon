package antigravitysession

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const (
	fullTranscriptName = "transcript_full.jsonl"
	transcriptName     = "transcript.jsonl"
	summariesDBName    = "conversation_summaries.db"
	summariesTable     = "conversation_summaries"
)

// Store reads Antigravity CLI conversations from a brain directory.
type Store struct {
	// BrainDir holds one directory per conversation, named by its conversationId.
	BrainDir string
	// DBPath is conversation_summaries.db, which sits beside the brain directory.
	DBPath string
}

// DefaultBrainDir is ~/.gemini/antigravity-cli/brain.
func DefaultBrainDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(".", ".gemini", "antigravity-cli", "brain")
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain")
}

// NewStore reads dir as the brain directory, or DefaultBrainDir when dir is empty. The summaries
// database is looked for beside it, which is where Antigravity keeps it.
func NewStore(dir string) *Store {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultBrainDir()
	}
	return &Store{BrainDir: dir, DBPath: filepath.Join(filepath.Dir(dir), summariesDBName)}
}

func (s *Store) Exists() bool {
	info, err := sessionfs.Stat(s.BrainDir)
	return err == nil && info.IsDir()
}

// List returns every conversation with a transcript, oldest first.
//
// A conversation without a summaries row, or a machine without the database, is listed with no
// Summary: the row only adds the working directory and title, and the transcript is collectable
// without them. A database that exists but cannot be read is the same, except that the error is
// returned alongside the conversations, as Cursor's and OpenCode's stores do with a store they
// could not read: the sweep reports it and still collects every transcript.
func (s *Store) List() ([]SessionRef, error) {
	entries, err := sessionfs.ReadDir(s.BrainDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var refs []SessionRef
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sourcePath := filepath.Join(s.BrainDir, entry.Name())
		logs := filepath.Join(sourcePath, ".system_generated", "logs")
		ref := SessionRef{ID: entry.Name(), SourcePath: sourcePath}
		// The full transcript is preferred because its tool arguments are typed. transcript.jsonl
		// holds the same steps with every argument JSON-encoded as a string, and is read only when
		// the full one is not there.
		if info, err := sessionfs.Stat(filepath.Join(logs, fullTranscriptName)); err == nil && !info.IsDir() {
			ref.TranscriptPath, ref.Full, ref.ModTimeUnixMS = filepath.Join(logs, fullTranscriptName), true, info.ModTime().UnixMilli()
		} else if info, err := sessionfs.Stat(filepath.Join(logs, transcriptName)); err == nil && !info.IsDir() {
			ref.TranscriptPath, ref.ModTimeUnixMS = filepath.Join(logs, transcriptName), info.ModTime().UnixMilli()
		} else {
			continue
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil, nil
	}
	summaries, summariesErr := s.readSummaries()
	for i := range refs {
		if summary, ok := summaries[refs[i].ID]; ok {
			summary := summary
			refs[i].Summary = &summary
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ModTimeUnixMS == refs[j].ModTimeUnixMS {
			return refs[i].ID < refs[j].ID
		}
		return refs[i].ModTimeUnixMS < refs[j].ModTimeUnixMS
	})
	return refs, summariesErr
}

// readSummaries reads the conversation_summaries rows, by conversation id.
//
// A missing database or table is not an error: it means no working directory or title, not a
// conversation that cannot be collected. The columns are looked up rather than assumed, so a
// column Antigravity drops reads as empty instead of failing every sweep; only conversation_id
// is required.
func (s *Store) readSummaries() (map[string]Summary, error) {
	if info, err := sessionfs.Stat(s.DBPath); err != nil || info.IsDir() {
		return nil, nil
	}
	db, err := sessionsync.OpenSQLite(s.DBPath, "busy_timeout(1000)")
	if err != nil {
		return nil, fmt.Errorf("open Antigravity conversation summaries: %w", err)
	}
	defer db.Close()

	columns, err := sessionsync.TableColumns(db, summariesTable)
	if err != nil {
		return nil, fmt.Errorf("read Antigravity conversation summaries: %w", err)
	}
	if !columns["conversation_id"] {
		return nil, nil
	}
	selected := []string{"conversation_id"}
	for _, column := range []string{"title", "workspace_uris", "project_id", "parent_conversation_id", "nesting_depth"} {
		if columns[column] {
			selected = append(selected, column)
		} else {
			selected = append(selected, "NULL")
		}
	}
	rows, err := db.Query("SELECT " + strings.Join(selected, ", ") + " FROM " + summariesTable)
	if err != nil {
		return nil, fmt.Errorf("read Antigravity conversation summaries: %w", err)
	}
	defer rows.Close()
	out := map[string]Summary{}
	for rows.Next() {
		var id, title, workspaces, project, parent sql.NullString
		var depth sql.NullInt64
		if err := rows.Scan(&id, &title, &workspaces, &project, &parent, &depth); err != nil {
			return nil, fmt.Errorf("read Antigravity conversation summaries: %w", err)
		}
		if strings.TrimSpace(id.String) == "" {
			continue
		}
		out[id.String] = Summary{
			Title:                strings.TrimSpace(title.String),
			WorkingDirectory:     firstWorkspacePath(workspaces.String),
			ProjectID:            strings.TrimSpace(project.String),
			ParentConversationID: strings.TrimSpace(parent.String),
			NestingDepth:         depth.Int64,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read Antigravity conversation summaries: %w", err)
	}
	return out, nil
}

// firstWorkspacePath is the first entry of workspace_uris, a JSON array of file:// URIs, as a
// local path. The first workspace is the one Antigravity was started in.
func firstWorkspacePath(raw string) string {
	var uris []string
	if json.Unmarshal([]byte(raw), &uris) != nil || len(uris) == 0 {
		return ""
	}
	path := asymptoteobserve.FilePathFromURI(strings.TrimSpace(uris[0]))
	if path == "" {
		return ""
	}
	return filepath.FromSlash(path)
}

// Read parses the conversation's transcript.
func (s *Store) Read(ref SessionRef) (SessionData, Stats, error) {
	data := SessionData{Ref: ref}
	steps, lines, malformed, err := readTranscript(ref.TranscriptPath, ref.Full)
	data.Steps, data.Lines = steps, lines
	return data, Stats{Malformed: malformed}, err
}

// readTranscript parses a transcript, returning the steps, the number of lines it scanned, how many
// did not parse, and whether the file could be read through.
//
// The line count is reported separately from len(steps) and the two are not interchangeable: the
// collector compares the count against its line cursor to notice a transcript rewritten shorter,
// and a single malformed line would otherwise read as a rewrite. A scan that stops on an error is a
// read failure rather than a short file for the same reason, so the session fails this sweep with
// its cursor untouched.
func readTranscript(path string, full bool) ([]Step, int, int, error) {
	f, err := sessionfs.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, 0, nil
		}
		return nil, 0, 0, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	var out []Step
	malformed, lineNo := 0, 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		// Physical line numbers, so a half-written line at the tail of a live conversation does
		// not renumber the lines after it once it lands.
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var step Step
		if err := json.Unmarshal([]byte(line), &step); err != nil {
			malformed++
			continue
		}
		step.Line = lineNo
		for i := range step.ToolCalls {
			step.ToolCalls[i].Arguments = decodeArgs(step.ToolCalls[i].Args, !full)
		}
		out = append(out, step)
	}
	if err := scanner.Err(); err != nil {
		return out, lineNo, malformed, fmt.Errorf("read %s: %w", path, err)
	}
	return out, lineNo, malformed, nil
}

// decodeArgs decodes a call's arguments. transcript.jsonl writes each value JSON-encoded as a
// string ("\"cat notes.txt\"", "5000"), so with encoded set each string that holds JSON is decoded
// once more; a string that does not is kept as written.
func decodeArgs(raw json.RawMessage, encoded bool) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var args map[string]interface{}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	if !encoded {
		return args
	}
	for key, value := range args {
		text, ok := value.(string)
		if !ok {
			continue
		}
		var decoded interface{}
		if json.Unmarshal([]byte(text), &decoded) == nil {
			args[key] = decoded
		}
	}
	return args
}
