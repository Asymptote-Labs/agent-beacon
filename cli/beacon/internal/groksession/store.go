package groksession

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Store struct {
	Dir string
}

func DefaultSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(".", ".grok", "sessions")
	}
	return filepath.Join(home, ".grok", "sessions")
}

func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultSessionsDir()
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) Exists() bool {
	info, err := os.Stat(s.Dir)
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]SessionRef, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var refs []SessionRef
	for _, project := range entries {
		if !project.IsDir() {
			continue
		}
		workspace, err := url.PathUnescape(project.Name())
		if err != nil {
			workspace = project.Name()
		}
		projectDir := filepath.Join(s.Dir, project.Name())
		sessions, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, session := range sessions {
			if !session.IsDir() {
				continue
			}
			sourcePath := filepath.Join(projectDir, session.Name())
			if !fileExists(filepath.Join(sourcePath, "summary.json")) || !fileExists(filepath.Join(sourcePath, "chat_history.jsonl")) {
				continue
			}
			ref := SessionRef{ID: session.Name(), Workspace: workspace, SourcePath: sourcePath}
			if info, err := os.Stat(sourcePath); err == nil {
				ref.ModTimeUnixMS = info.ModTime().UnixMilli()
			}
			if summary, err := readJSONFile[Summary](filepath.Join(sourcePath, "summary.json")); err == nil {
				ref.Summary = summary
				if ts := parseMillis(summary.UpdatedAt); ts > ref.ModTimeUnixMS {
					ref.ModTimeUnixMS = ts
				}
				if summary.CurrentModelID == "" && summary.SessionSummary == "" && summary.GeneratedTitle == "" {
					ref.Summary = nil
				}
			}
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ModTimeUnixMS == refs[j].ModTimeUnixMS {
			return refs[i].ID < refs[j].ID
		}
		return refs[i].ModTimeUnixMS < refs[j].ModTimeUnixMS
	})
	return refs, nil
}

func (s *Store) Read(ref SessionRef) (SessionData, Stats, error) {
	data := SessionData{Ref: ref, TerminalLogs: map[string]string{}}
	var stats Stats
	if prompt, err := readJSONFile[PromptContext](filepath.Join(ref.SourcePath, "prompt_context.json")); err == nil {
		data.Prompt = prompt
		if prompt.WorkingDirectory != "" {
			data.Ref.Workspace = prompt.WorkingDirectory
		}
	}
	data.Chat, data.ChatLines, stats.Malformed = readJSONLines[ChatMessage](filepath.Join(ref.SourcePath, "chat_history.jsonl"))
	lifecycle, lifecycleLines, malformed := readJSONLines[LifecycleEvent](filepath.Join(ref.SourcePath, "events.jsonl"))
	stats.Malformed += malformed
	data.Lifecycle = lifecycle
	data.LifecycleLines = lifecycleLines
	if ref.Summary == nil {
		if summary, err := readJSONFile[Summary](filepath.Join(ref.SourcePath, "summary.json")); err == nil {
			data.Ref.Summary = summary
		}
	}
	if err := readTerminalLogs(filepath.Join(ref.SourcePath, "terminal"), data.TerminalLogs); err != nil {
		return data, stats, err
	}
	return data, stats, nil
}

func readJSONFile[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &out, nil
}

// readJSONLines parses one of a session's JSONL files, returning the records, the number of lines
// it scanned, and how many of them did not parse.
//
// The line count is reported separately from len(records) and the two are not interchangeable: a
// blank or unparseable line advances the count without producing a record. The collector compares
// the count against its line cursor to notice a file that was rewritten shorter, and using the
// record count there would read a single malformed line as a rewrite.
//
// When the file cannot be opened the line count is -1, distinguishing a transient read failure from
// a genuinely empty or truncated file. The collector must skip the shrink check for that file.
func readJSONLines[T any](path string) ([]T, int, int) {
	f, err := os.Open(path)
	if err != nil {
		return nil, -1, 0
	}
	defer f.Close()
	var out []T
	malformed := 0
	lineNo := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		// Index is the physical line number rather than the position among the lines that parsed.
		// Grok appends to these files while a session runs, so a sweep can catch a line that is only
		// half written; it is counted as malformed now and parses on the next sweep. Numbering by
		// parse position would renumber every line after it once it parsed, and the collector's
		// cursor -- which is a line number -- would skip or repeat records it had already read.
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			malformed++
			continue
		}
		switch v := any(&item).(type) {
		case *ChatMessage:
			v.Index = lineNo
		case *LifecycleEvent:
			v.Index = lineNo
		}
		out = append(out, item)
	}
	if err := scanner.Err(); err != nil {
		malformed++
	}
	return out, lineNo, malformed
}

func readTerminalLogs(dir string, logs map[string]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		callID := strings.TrimSuffix(entry.Name(), ".log")
		if idx := strings.LastIndex(callID, "-"); idx > len("call-") {
			callID = callID[:idx]
		}
		logs[callID] = string(data)
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
