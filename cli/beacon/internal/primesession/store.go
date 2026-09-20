package primesession

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
)

const (
	maxSessionFiles = 15000
	maxLineBytes    = 10 * 1024 * 1024

	primeAgentDirEnv = "PRIME_AGENT_CODING_AGENT_DIR"
)

// Store reads Prime Agent's durable session store.
type Store struct {
	SessionsDir  string
	ArtifactsDir string
}

// SessionRef is one Prime Agent JSONL transcript.
type SessionRef struct {
	ID        string
	Path      string
	Kind      string
	ModTimeMS int64
	SizeBytes int64
	Header    map[string]interface{}
}

// Entry is one decoded line from a Prime Agent transcript.
type Entry struct {
	Line int
	Data map[string]interface{}
}

type ReadStats struct {
	Malformed int
	MaxLine   int
}

func NewStore(sessionsDir, artifactsDir string) (*Store, error) {
	if strings.TrimSpace(sessionsDir) == "" {
		def, err := DefaultSessionsDir()
		if err != nil {
			return nil, err
		}
		sessionsDir = def
	}
	if strings.TrimSpace(artifactsDir) == "" {
		def, err := DefaultArtifactsDir()
		if err != nil {
			return nil, err
		}
		artifactsDir = def
	}
	return &Store{SessionsDir: sessionsDir, ArtifactsDir: artifactsDir}, nil
}

func DefaultSessionsDir() (string, error) {
	agentDir, err := defaultAgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(agentDir, "sessions"), nil
}

func DefaultArtifactsDir() (string, error) {
	agentDir, err := defaultAgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(agentDir, "session-artifacts"), nil
}

func defaultAgentDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if override := strings.TrimSpace(os.Getenv(primeAgentDirEnv)); override != "" {
		return expandTilde(override, home), nil
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(home, ".prime", "agent"), nil
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
	return dirExists(s.SessionsDir) || dirExists(s.ArtifactsDir)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]SessionRef, error) {
	var refs []SessionRef
	for _, root := range []struct {
		path string
		kind string
	}{
		{s.SessionsDir, "session"},
		{s.ArtifactsDir, "artifact"},
	} {
		items, err := listJSONL(root.path)
		if err != nil {
			return nil, err
		}
		for _, path := range items {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			header := readHeader(path)
			id := stringValue(header["id"])
			if id == "" {
				id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			refs = append(refs, SessionRef{
				ID:        id,
				Path:      path,
				Kind:      root.kind,
				ModTimeMS: info.ModTime().UnixMilli(),
				SizeBytes: info.Size(),
				Header:    header,
			})
		}
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
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var entries []Entry
	stats := ReadStats{}
	for scanner.Scan() {
		stats.MaxLine++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item map[string]interface{}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			stats.Malformed++
			continue
		}
		entries = append(entries, Entry{Line: stats.MaxLine, Data: item})
	}
	if err := scanner.Err(); err != nil {
		return entries, stats, err
	}
	return entries, stats, nil
}
