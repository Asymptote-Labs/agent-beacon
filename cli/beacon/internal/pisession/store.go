package pisession

import (
	"bufio"
	"bytes"
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
	maxSessionFiles = 15000
	maxLineBytes    = 10 * 1024 * 1024

	piAgentDirEnv = "PI_CODING_AGENT_DIR"
)

// Store reads Pi's durable JSONL session store.
type Store struct {
	SessionsDir string
}

// SessionRef is one Pi JSONL transcript.
type SessionRef struct {
	ID        string
	Path      string
	ModTimeMS int64
	SizeBytes int64
	Workspace string
	Header    map[string]interface{}
}

// Entry is one decoded line from a Pi transcript.
type Entry struct {
	Line int
	Data map[string]interface{}
}

type ReadStats struct {
	Malformed   int
	MaxLine     int
	PartialTail bool
}

func NewStore(sessionsDir string) (*Store, error) {
	if strings.TrimSpace(sessionsDir) == "" {
		def, err := DefaultSessionsDir()
		if err != nil {
			return nil, err
		}
		sessionsDir = def
	}
	return &Store{SessionsDir: sessionsDir}, nil
}

func DefaultSessionsDir() (string, error) {
	agentDir, err := defaultAgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(agentDir, "sessions"), nil
}

func defaultAgentDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if override := strings.TrimSpace(os.Getenv(piAgentDirEnv)); override != "" {
		return expandTilde(override, home), nil
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

func expandTilde(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, filepath.FromSlash(path[2:]))
	}
	return path
}

func (s *Store) Exists() bool {
	info, err := os.Stat(s.SessionsDir)
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]SessionRef, error) {
	items, err := listJSONL(s.SessionsDir)
	if err != nil {
		return nil, err
	}
	refs := make([]SessionRef, 0, len(items))
	for _, path := range items {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		header := readHeader(path)
		id := stringValue(header["id"])
		if id == "" {
			id = extractSessionIDFromPath(path)
		}
		refs = append(refs, SessionRef{
			ID:        id,
			Path:      path,
			ModTimeMS: info.ModTime().UnixMilli(),
			SizeBytes: info.Size(),
			Workspace: stringValue(header["cwd"]),
			Header:    header,
		})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].ModTimeMS != refs[j].ModTimeMS {
			return refs[i].ModTimeMS < refs[j].ModTimeMS
		}
		return refs[i].Path < refs[j].Path
	})
	return refs, nil
}

func listJSONL(root string) ([]string, error) {
	if !dirExists(root) {
		return nil, nil
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(out) >= maxSessionFiles {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func readHeader(path string) map[string]interface{} {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item map[string]interface{}
		if json.Unmarshal([]byte(line), &item) == nil && strings.EqualFold(stringValue(item["type"]), "session") {
			return item
		}
		return nil
	}
	return nil
}

func (s *Store) Read(ref SessionRef) ([]Entry, ReadStats, error) {
	file, err := os.Open(ref.Path)
	if err != nil {
		return nil, ReadStats{}, err
	}
	defer file.Close()
	br := bufio.NewReaderSize(file, 64*1024)
	var entries []Entry
	stats := ReadStats{}
	for {
		raw, status, readErr := readLine(br)
		switch status {
		case lineEOF:
			return entries, stats, nil
		case linePartial:
			stats.PartialTail = true
			return entries, stats, nil
		case lineError:
			return entries, stats, readErr
		case lineOversized:
			stats.MaxLine++
			stats.Malformed++
			continue
		}
		stats.MaxLine++
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		var item map[string]interface{}
		if jsonErr := json.Unmarshal([]byte(line), &item); jsonErr != nil {
			stats.Malformed++
			continue
		}
		entries = append(entries, Entry{Line: stats.MaxLine, Data: item})
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

// readLine reads one JSONL line from br, handling oversized lines (consumed and
// discarded so later lines are still reachable) and partial tails (unterminated
// trailing fragments from a file still being written). Allocation is bounded by
// maxLineBytes so a damaged or in-progress file cannot dictate memory use.
func readLine(br *bufio.Reader) ([]byte, lineStatus, error) {
	var buf []byte
	oversized := false
	for {
		chunk, err := br.ReadSlice('\n')
		if oversized || len(buf)+len(chunk) > maxLineBytes {
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
				return nil, linePartial, nil
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

func extractSessionIDFromPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if idx := strings.LastIndex(base, "_"); idx >= 0 && idx+1 < len(base) {
		return base[idx+1:]
	}
	return base
}
