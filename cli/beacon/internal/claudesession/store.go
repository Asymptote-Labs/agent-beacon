package claudesession

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	MaxLineBytes = 8 * 1024 * 1024
)

type SessionRef struct {
	ID              string
	Path            string
	ProjectDir      string
	ProjectPath     string
	ModTimeUnixMS   int64
	SizeBytes       int64
	IsSidechain     bool
	ParentSessionID string
	Meta            *SubagentMeta
	Index           *IndexEntry
}

type Store struct {
	ProjectsDir string
}

type Stats struct {
	Lines       int
	Decoded     int
	Malformed   int
	PartialTail bool
	FirstError  error
}

func DefaultProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) != "" {
		return &Store{ProjectsDir: dir}, nil
	}
	def, err := DefaultProjectsDir()
	if err != nil {
		return nil, err
	}
	return &Store{ProjectsDir: def}, nil
}

func (s *Store) Exists() bool {
	info, err := os.Stat(s.ProjectsDir)
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]SessionRef, error) {
	entries, err := os.ReadDir(s.ProjectsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Claude projects directory: %w", err)
	}

	var refs []SessionRef
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projectDir := filepath.Join(s.ProjectsDir, entry.Name())
		index := readIndex(projectDir)
		if err := filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".jsonl" {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.IsDir() {
				return nil
			}
			ref := SessionRef{
				ID:            strings.TrimSuffix(filepath.Base(path), ".jsonl"),
				Path:          path,
				ProjectDir:    projectDir,
				ProjectPath:   projectPathFromDir(projectDir),
				ModTimeUnixMS: info.ModTime().UnixMilli(),
				SizeBytes:     info.Size(),
			}
			if idx := index[path]; idx != nil {
				ref.Index = idx
				if idx.ProjectPath != "" {
					ref.ProjectPath = idx.ProjectPath
				}
				if idx.SessionID != "" {
					ref.ID = idx.SessionID
				}
				ref.IsSidechain = idx.IsSidechain
			}
			if filepath.Base(filepath.Dir(path)) == "subagents" {
				ref.IsSidechain = true
				ref.ParentSessionID = filepath.Base(filepath.Dir(filepath.Dir(path)))
				ref.Meta = readSubagentMeta(strings.TrimSuffix(path, ".jsonl") + ".meta.json")
			}
			refs = append(refs, ref)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].ModTimeUnixMS != refs[j].ModTimeUnixMS {
			return refs[i].ModTimeUnixMS < refs[j].ModTimeUnixMS
		}
		return refs[i].Path < refs[j].Path
	})
	return refs, nil
}

func (s *Store) Read(ref SessionRef) ([]Record, Stats, error) {
	f, err := os.Open(ref.Path)
	if err != nil {
		return nil, Stats{}, err
	}
	defer f.Close()
	return decodeRecords(f)
}

func decodeRecords(r io.Reader) ([]Record, Stats, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxLineBytes)
	var records []Record
	var stats Stats
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		stats.Lines = lineNo
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		entry, err := decodeEntry(line)
		if err != nil {
			stats.Malformed++
			if stats.FirstError == nil {
				stats.FirstError = err
			}
			continue
		}
		stats.Decoded++
		records = append(records, Record{Line: lineNo, Entry: *entry})
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			stats.Malformed++
			if stats.FirstError == nil {
				stats.FirstError = fmt.Errorf("claude session line exceeds %d bytes", MaxLineBytes)
			}
			return records, stats, nil
		}
		return records, stats, err
	}
	return records, stats, nil
}

func readIndex(projectDir string) map[string]*IndexEntry {
	data, err := os.ReadFile(filepath.Join(projectDir, "sessions-index.json"))
	if err != nil {
		return nil
	}
	var index SessionIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil
	}
	out := make(map[string]*IndexEntry, len(index.Entries))
	for i := range index.Entries {
		entry := index.Entries[i]
		if entry.FullPath == "" {
			continue
		}
		out[entry.FullPath] = &entry
	}
	return out
}

func readSubagentMeta(path string) *SubagentMeta {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var meta SubagentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}
	return &meta
}

func projectPathFromDir(projectDir string) string {
	base := filepath.Base(projectDir)
	if !strings.HasPrefix(base, "-") {
		return ""
	}
	return string(filepath.Separator) + strings.ReplaceAll(strings.TrimPrefix(base, "-"), "-", string(filepath.Separator))
}
