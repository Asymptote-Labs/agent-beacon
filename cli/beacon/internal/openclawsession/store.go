package openclawsession

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxSessionFilesScanned = 10000
	maxLineBytes           = 4 << 20
)

type Store struct {
	OpenClawDir string
}

func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		def, err := DefaultOpenClawDir()
		if err != nil {
			return nil, err
		}
		dir = def
	}
	return &Store{OpenClawDir: filepath.Clean(dir)}, nil
}

func DefaultOpenClawDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("OPENCLAW_STATE_DIR")); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(home, ".openclaw"), nil
}

func (s *Store) Exists() bool {
	info, err := os.Stat(filepath.Join(s.OpenClawDir, "agents"))
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]TraceRef, error) {
	roots, err := s.sessionRoots()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var refs []TraceRef
	for _, root := range roots {
		indexed := s.readSessionsIndex(root)
		for _, ref := range indexed {
			refs = append(refs, ref)
			seen[ref.SourcePath] = true
		}
		if len(indexed) > 0 && allRootFilesSeen(root, seen) {
			continue
		}
		for _, path := range collectSessionFilesFlat(root, max(2000, maxSessionFilesScanned/max(1, len(roots)))) {
			if seen[path] {
				continue
			}
			if ref, ok := s.readTraceMetadata(path, 0, ""); ok {
				refs = append(refs, ref)
				seen[path] = true
			}
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].UpdatedAtUnixMS != refs[j].UpdatedAtUnixMS {
			return refs[i].UpdatedAtUnixMS < refs[j].UpdatedAtUnixMS
		}
		if refs[i].Profile != refs[j].Profile {
			return refs[i].Profile < refs[j].Profile
		}
		return refs[i].ID < refs[j].ID
	})
	return refs, nil
}

func (s *Store) Read(ref TraceRef) ([]Record, error) {
	entries, err := readEntries(ref.SourcePath)
	if err != nil {
		return nil, err
	}
	return recordsFromEntries(entries, ref.UpdatedAtUnixMS), nil
}

func (s *Store) sessionRoots() ([]string, error) {
	agentsDir := filepath.Join(s.OpenClawDir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read OpenClaw agents directory: %w", err)
	}
	var roots []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(agentsDir, entry.Name(), "sessions")
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	return roots, nil
}

func (s *Store) readSessionsIndex(root string) []TraceRef {
	data, err := os.ReadFile(filepath.Join(root, "sessions.json"))
	if err != nil {
		return nil
	}
	var raw map[string]SessionIndexEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	var refs []TraceRef
	for _, entry := range raw {
		sourcePath := resolveIndexedSourcePath(entry.SessionFile, root)
		if entry.SessionID == "" || sourcePath == "" {
			continue
		}
		if _, err := os.Stat(sourcePath); err != nil {
			continue
		}
		updated := parseTimestamp(entry.UpdatedAt, 0)
		dir := firstNonEmpty(entry.WorkspaceDir, directoryFromAny(entry.SystemPromptReport))
		if ref, ok := s.readTraceMetadata(sourcePath, updated, dir); ok {
			ref.Profile = profileFromSessionRoot(root)
			refs = append(refs, ref)
		}
	}
	return refs
}

func (s *Store) readTraceMetadata(path string, updatedHint int64, dirHint string) (TraceRef, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return TraceRef{}, false
	}
	updated := updatedHint
	if updated <= 0 {
		updated = info.ModTime().UnixMilli()
	}
	entries, err := readEntries(path)
	if err != nil || len(entries) == 0 {
		return TraceRef{}, false
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	title := ""
	dir := dirHint
	for _, entry := range entries {
		ts := parseTimestamp(entry.Timestamp, updated)
		if ts > updated {
			updated = ts
		}
		switch strings.ToLower(entry.Type) {
		case "session", "session.started":
			if entry.ID != "" {
				id = entry.ID
			}
			dir = chooseDirectory(dir, entry.CWD)
		case "model_change":
			// Model switches are mapped from the full record later. Metadata only needs the
			// session's identity, title and workspace.
		case "message":
			if title == "" && entry.Message != nil && strings.EqualFold(entry.Message.Role, "user") {
				title = normalizeTitle(extractText(entry.Message.Content))
			}
		}
		dir = chooseDirectory(dir, directoryFromAny(entry.Raw))
	}
	if title == "" {
		title = "(No preview)"
	}
	return TraceRef{
		ID:              id,
		Profile:         profileFromSourcePath(path),
		SourcePath:      path,
		Title:           title,
		Preview:         title,
		Directory:       dir,
		UpdatedAtUnixMS: updated,
		SizeBytes:       info.Size(),
	}, true
}

func readEntries(path string) ([]RawEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var out []RawEntry
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		var entry RawEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entry.Raw = raw
		out = append(out, entry)
	}
	return out, scanner.Err()
}

func collectSessionFilesFlat(root string, limit int) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") || entry.Name() == "config-audit.jsonl" {
			continue
		}
		out = append(out, filepath.Join(root, entry.Name()))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func resolveIndexedSourcePath(path, root string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}

func allRootFilesSeen(root string, seen map[string]bool) bool {
	for _, path := range collectSessionFilesFlat(root, maxSessionFilesScanned) {
		if !seen[path] {
			return false
		}
	}
	return true
}

func profileFromSessionRoot(root string) string {
	if filepath.Base(root) != "sessions" {
		return "unknown"
	}
	return filepath.Base(filepath.Dir(root))
}

func profileFromSourcePath(path string) string {
	clean := filepath.Clean(path)
	parts := strings.Split(clean, string(filepath.Separator))
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "agents" && parts[i+2] == "sessions" {
			return parts[i+1]
		}
	}
	return "unknown"
}

func parseTimestamp(value any, fallback int64) int64 {
	switch v := value.(type) {
	case float64:
		if v > 0 && v < 1000000000000 {
			return int64(v * 1000)
		}
		return int64(v)
	case int64:
		if v > 0 && v < 1000000000000 {
			return v * 1000
		}
		return v
	case string:
		if v == "" {
			return fallback
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UnixMilli()
		}
	}
	return fallback
}

func normalizeTitle(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "[[reply_to_current]]")
	text = strings.TrimSpace(text)
	if len(text) > 100 {
		return text[:100]
	}
	return text
}

func chooseDirectory(current, candidate string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}
	return strings.TrimSpace(candidate)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
