package codexsession

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

const MaxLineBytes = 8 * 1024 * 1024

type SessionRef struct {
	ID            string
	Path          string
	CodexDir      string
	ModTimeUnixMS int64
	SizeBytes     int64
	Workspace     string
	Index         *IndexEntry
}

type Store struct {
	CodexDir    string
	SessionsDir string
}

type Stats struct {
	Lines       int
	Decoded     int
	Malformed   int
	PartialTail bool
	FirstError  error
}

type IndexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

func DefaultCodexDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(home, ".codex"), nil
}

func NewStore(dir string) (*Store, error) {
	codexDir := strings.TrimSpace(dir)
	if codexDir == "" {
		def, err := DefaultCodexDir()
		if err != nil {
			return nil, err
		}
		codexDir = def
	}
	return &Store{CodexDir: codexDir, SessionsDir: filepath.Join(codexDir, "sessions")}, nil
}

func (s *Store) Exists() bool {
	info, err := os.Stat(s.SessionsDir)
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]SessionRef, error) {
	index := s.readIndex()
	var refs []SessionRef
	err := filepath.WalkDir(s.SessionsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".jsonl" && ext != ".json" {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.IsDir() {
			return nil
		}
		ref := SessionRef{
			ID:            sessionIDFromPath(path),
			Path:          path,
			CodexDir:      s.CodexDir,
			ModTimeUnixMS: info.ModTime().UnixMilli(),
			SizeBytes:     info.Size(),
		}
		if meta := readMetadata(path); meta != nil {
			if id := firstNonEmpty(meta.ID, meta.SessionID); id != "" {
				ref.ID = id
			}
			ref.Workspace = meta.CWD
		}
		if idx := index[ref.ID]; idx != nil {
			ref.Index = idx
		}
		refs = append(refs, ref)
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Codex sessions directory: %w", err)
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
	br := bufio.NewReaderSize(r, 64*1024)
	var records []Record
	var stats Stats
	lineNo := 0
	for {
		line, partial, oversized, err := readLine(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if partial {
					stats.PartialTail = true
				}
				break
			}
			return records, stats, err
		}
		lineNo++
		stats.Lines = lineNo
		if oversized {
			stats.Malformed++
			if stats.FirstError == nil {
				stats.FirstError = fmt.Errorf("codex session line exceeds %d bytes", MaxLineBytes)
			}
			continue
		}
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
	return records, stats, nil
}

// readLine returns one complete line without its trailing newline.
//
// Three endings are distinguished. A complete line (terminated by \n) is returned with partial
// and oversized both false. A trailing fragment with no newline is a record Codex is still
// writing: it is reported as partial so the caller can set PartialTail and avoid advancing the
// cursor past an incomplete record. A line longer than MaxLineBytes is consumed and discarded
// as oversized so a corrupt file cannot dictate memory use.
func readLine(br *bufio.Reader) (line []byte, partial, oversized bool, err error) {
	var buf []byte
	for {
		chunk, readErr := br.ReadSlice('\n')
		if oversized || len(buf)+len(chunk) > MaxLineBytes {
			oversized = true
			buf = nil
		} else {
			buf = append(buf, chunk...)
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			if oversized {
				return nil, false, true, nil
			}
			if len(buf) > 0 {
				return buf, true, false, io.EOF
			}
			return nil, false, false, io.EOF
		}
		if readErr != nil {
			return nil, false, false, readErr
		}
		break
	}
	if oversized {
		return nil, false, true, nil
	}
	line = bytes.TrimSuffix(bytes.TrimSuffix(buf, []byte("\n")), []byte("\r"))
	return line, false, false, nil
}

func (s *Store) readIndex() map[string]*IndexEntry {
	data, err := os.ReadFile(filepath.Join(s.CodexDir, "session_index.jsonl"))
	if err != nil {
		return nil
	}
	out := map[string]*IndexEntry{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry IndexEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.ID == "" {
			continue
		}
		copy := entry
		out[entry.ID] = &copy
	}
	return out
}

func readMetadata(path string) *SessionMeta {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		entry, err := decodeEntry([]byte(line))
		if err != nil {
			return nil
		}
		if entry.SessionMeta != nil {
			return entry.SessionMeta
		}
		return nil
	}
	return nil
}

func sessionIDFromPath(path string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".jsonl"), ".json")
	if strings.HasPrefix(base, "rollout-") {
		parts := strings.Split(base, "-")
		if len(parts) >= 7 {
			return strings.Join(parts[len(parts)-5:], "-")
		}
	}
	return base
}
