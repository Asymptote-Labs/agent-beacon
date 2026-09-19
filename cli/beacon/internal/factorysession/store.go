package factorysession

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
	info, err := os.Stat(s.Dir)
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]SessionRef, error) {
	projects, err := os.ReadDir(s.Dir)
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
		files, err := os.ReadDir(projectDir)
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
			info, err := os.Stat(path)
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
	file, err := os.Open(path)
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
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	if info.Size() > MaxSettingsBytes {
		return nil, 0, fmt.Errorf("Factory settings %s is %d bytes, over the %d-byte limit", path, info.Size(), MaxSettingsBytes)
	}
	data, err := os.ReadFile(path)
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
	file, err := os.Open(ref.Path)
	if err != nil {
		return nil, ReadStats{}, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxLineBytes)
	var records []Record
	stats := ReadStats{}
	for scanner.Scan() {
		stats.Lines++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(text), &record); err != nil {
			stats.Malformed++
			continue
		}
		record.Line = stats.Lines
		record.Raw = json.RawMessage(append([]byte(nil), text...))
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		stats.Partial = true
		return records, stats, nil
	}
	return records, stats, nil
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
