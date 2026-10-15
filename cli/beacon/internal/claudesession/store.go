package claudesession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
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
	// Head is the transcript head List read to find the workspace when the index did not name
	// it; nil when the index supplied the workspace and the head was not read.
	Head *Head
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

// ConfigDir is Claude Code's config directory: a trimmed, non-empty CLAUDE_CONFIG_DIR, as Claude
// Code resolves it, otherwise <home>/.claude. settings.json, skills and projects live under it.
// It is empty when CLAUDE_CONFIG_DIR is unset and home is empty, rather than a relative ".claude"
// that would name a project's directory.
func ConfigDir(home string) string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	if strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// StateFile is Claude Code's user state file, the one `claude mcp add --scope user` writes:
// <home>/.claude.json, or .claude.json inside CLAUDE_CONFIG_DIR when that is set. Unlike the
// config directory's other files it sits beside .claude, not in it, by default. Empty under the
// same condition as ConfigDir.
func StateFile(home string) string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	if strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

// DefaultProjectsDir is where Claude Code keeps session transcripts: <config dir>/projects.
func DefaultProjectsDir() (string, error) {
	if strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) != "" {
		// No home directory is needed when the config directory is named outright.
		return filepath.Join(ConfigDir(""), "projects"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(ConfigDir(home), "projects"), nil
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
	info, err := sessionfs.Stat(s.ProjectsDir)
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]SessionRef, error) {
	entries, err := sessionfs.ReadDir(s.ProjectsDir)
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
		if err := sessionfs.WalkDir(projectDir, func(path string, d fs.DirEntry, walkErr error) error {
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
			if ref.Index == nil || ref.Index.ProjectPath == "" {
				head := ReadHead(path)
				ref.Head = &head
				if head.CWD != "" {
					ref.ProjectPath = head.CWD
				}
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
	f, err := sessionfs.Open(ref.Path)
	if err != nil {
		return nil, Stats{}, err
	}
	defer f.Close()
	return decodeRecords(f)
}

func decodeRecords(r io.Reader) ([]Record, Stats, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var records []Record
	var stats Stats
	lineNo := 0
	for {
		line, status, readErr := readLine(br, MaxLineBytes)
		switch status {
		case lineEOF:
			return records, stats, nil
		case linePartial:
			stats.PartialTail = true
			return records, stats, nil
		case lineError:
			return records, stats, readErr
		case lineOversized:
			lineNo++
			stats.Lines = lineNo
			stats.Malformed++
			if stats.FirstError == nil {
				stats.FirstError = fmt.Errorf("claude session line exceeds %d bytes", MaxLineBytes)
			}
			continue
		}
		lineNo++
		stats.Lines = lineNo
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
}

type lineStatus int

const (
	lineOK        lineStatus = iota
	lineOversized            // consumed and discarded; too large
	linePartial              // unterminated trailing fragment
	lineEOF                  // clean end of stream
	lineError                // I/O error
)

// readLine reads one JSONL line of at most max bytes from br, handling oversized lines (consumed
// and discarded so later lines are still reachable) and partial tails (unterminated trailing
// fragments from a file still being written, returned with linePartial for a caller that wants
// them).
func readLine(br *bufio.Reader, max int) ([]byte, lineStatus, error) {
	var buf []byte
	oversized := false
	for {
		chunk, err := br.ReadSlice('\n')
		if oversized || len(buf)+len(chunk) > max {
			oversized = true
			buf = nil
		} else {
			buf = append(buf, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if oversized {
				return nil, lineOversized, nil
			}
			if len(buf) > 0 {
				return bytes.TrimSuffix(buf, []byte("\r")), linePartial, nil
			}
			return nil, lineEOF, nil
		}
		if err != nil {
			return nil, lineError, err
		}
		break
	}
	if oversized {
		return nil, lineOversized, nil
	}
	return bytes.TrimSuffix(bytes.TrimSuffix(buf, []byte("\n")), []byte("\r")), lineOK, nil
}

func readIndex(projectDir string) map[string]*IndexEntry {
	data, err := sessionfs.ReadFile(filepath.Join(projectDir, "sessions-index.json"))
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
	data, err := sessionfs.ReadFile(path)
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
