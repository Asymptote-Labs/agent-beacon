package factorysession

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
	MaxLineBytes     = 4 << 20
	MaxSettingsBytes = 1 << 20
)

type Store struct {
	Dir string
}

type SessionRef struct {
	ID             string
	ProjectDirName string
	Path           string
	SettingsPath   string
	CWD            string
	Title          string
	Model          string
	Settings       *Settings
	ModTimeUnixMS  int64
	SettingsUnixMS int64
	SizeBytes      int64
}

type ReadStats struct {
	Lines     int
	Malformed int
	Partial   bool
}

func DefaultSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(home, ".factory", "sessions"), nil
}

func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) != "" {
		return &Store{Dir: dir}, nil
	}
	def, err := DefaultSessionsDir()
	if err != nil {
		return nil, err
	}
	return &Store{Dir: def}, nil
}

func (s *Store) Exists() bool {
	info, err := sessionfs.Stat(s.Dir)
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]SessionRef, error) {
	projects, err := sessionfs.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Factory sessions directory: %w", err)
	}
	var refs []SessionRef
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		projectDir := filepath.Join(s.Dir, project.Name())
		files, err := sessionfs.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(file.Name(), ".jsonl")
			if err := validateSessionID(id); err != nil {
				continue
			}
			path := filepath.Join(projectDir, file.Name())
			info, err := sessionfs.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			ref := SessionRef{
				ID:             id,
				ProjectDirName: project.Name(),
				Path:           path,
				SettingsPath:   filepath.Join(projectDir, id+".settings.json"),
				CWD:            decodeProjectDirName(project.Name()),
				ModTimeUnixMS:  info.ModTime().UnixMilli(),
				SizeBytes:      info.Size(),
			}
			if header, err := ReadHeader(path); err == nil && header != nil {
				if header.CWD != "" {
					ref.CWD = header.CWD
				}
				ref.Title = header.Title
			}
			if settings, mtime, err := ReadSettings(ref.SettingsPath); err == nil {
				ref.Settings = settings
				ref.SettingsUnixMS = mtime
				ref.Model = settings.Model
			}
			refs = append(refs, ref)
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].ModTimeUnixMS != refs[j].ModTimeUnixMS {
			return refs[i].ModTimeUnixMS < refs[j].ModTimeUnixMS
		}
		return refs[i].ID < refs[j].ID
	})
	return refs, nil
}

func ReadHeader(path string) (*Record, error) {
	file, err := sessionfs.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	line, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 {
		return nil, nil
	}
	var record Record
	if err := json.Unmarshal(line, &record); err != nil {
		return nil, err
	}
	if record.Type != RecordSessionStart {
		return nil, nil
	}
	return &record, nil
}

func ReadSettings(path string) (*Settings, int64, error) {
	info, err := sessionfs.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	if info.Size() > MaxSettingsBytes {
		return nil, 0, fmt.Errorf("Factory settings %s is %d bytes, over the %d-byte limit", path, info.Size(), MaxSettingsBytes)
	}
	data, err := sessionfs.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, 0, err
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, 0, err
	}
	settings.Raw = raw
	return &settings, info.ModTime().UnixMilli(), nil
}

func (s *Store) Read(ref SessionRef) ([]Record, ReadStats, error) {
	file, err := sessionfs.Open(ref.Path)
	if err != nil {
		return nil, ReadStats{}, err
	}
	defer file.Close()
	br := bufio.NewReaderSize(file, 64*1024)
	var records []Record
	stats := ReadStats{}
	for {
		raw, status, readErr := readLine(br)
		switch status {
		case lineEOF:
			return records, stats, nil
		case linePartial:
			// An unterminated tail is a record Factory is still writing; withhold it, oversized or
			// not, so the next sweep reads it complete.
			stats.Partial = true
			return records, stats, nil
		case lineError:
			stats.Partial = true
			return records, stats, readErr
		case lineOversized:
			// Skipped, but still a line, so every line after it keeps its number and is read.
			stats.Lines++
			stats.Malformed++
			continue
		}
		stats.Lines++
		text := strings.TrimSpace(string(raw))
		if text == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(text), &record); err != nil {
			stats.Malformed++
			continue
		}
		record.Line = stats.Lines
		record.Raw = json.RawMessage(text)
		records = append(records, record)
	}
}

type lineStatus int

const (
	lineOK        lineStatus = iota
	lineOversized            // terminated, longer than MaxLineBytes, consumed and discarded
	linePartial              // an unterminated tail, of any length
	lineEOF                  // clean end of the file
	lineError                // I/O error
)

// readLine reads one line from br. A line longer than MaxLineBytes (a pasted image or a large tool
// result) is consumed without being held in memory and reported as oversized, so the lines after it
// are still read; bufio.Scanner stopped at it for good. An unterminated tail is reported as partial.
func readLine(br *bufio.Reader) ([]byte, lineStatus, error) {
	var buf []byte
	oversized := false
	for {
		chunk, err := br.ReadSlice('\n')
		if oversized || len(buf)+len(chunk) > MaxLineBytes {
			oversized, buf = true, nil
		} else {
			buf = append(buf, chunk...)
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if oversized || len(buf) > 0 {
				return nil, linePartial, nil
			}
			return nil, lineEOF, nil
		case err != nil:
			return nil, lineError, err
		}
		if oversized {
			return nil, lineOversized, nil
		}
		return bytes.TrimSuffix(bytes.TrimSuffix(buf, []byte("\n")), []byte("\r")), lineOK, nil
	}
}

func validateSessionID(id string) error {
	if id == "" || id == "." || id == ".." || len(id) > 255 {
		return fmt.Errorf("invalid Factory session id %q", id)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return fmt.Errorf("invalid Factory session id %q", id)
		}
	}
	return nil
}

func decodeProjectDirName(name string) string {
	if strings.HasPrefix(name, "-") {
		return "/" + strings.ReplaceAll(strings.TrimPrefix(name, "-"), "-", "/")
	}
	return name
}
