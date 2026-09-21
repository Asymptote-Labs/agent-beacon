package copilotsession

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

	"gopkg.in/yaml.v3"
)

type Store struct {
	CopilotDir      string
	SessionStateDir string
}

func DefaultCopilotDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(home, ".copilot"), nil
}

func NewStore(copilotDir string) (*Store, error) {
	root := strings.TrimSpace(copilotDir)
	if root == "" {
		def, err := DefaultCopilotDir()
		if err != nil {
			return nil, err
		}
		root = def
	}
	return &Store{CopilotDir: root, SessionStateDir: filepath.Join(root, "session-state")}, nil
}

func (s *Store) Exists() bool {
	info, err := os.Stat(s.SessionStateDir)
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]SessionRef, error) {
	var refs []SessionRef
	entries, err := os.ReadDir(s.SessionStateDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read GitHub Copilot CLI session directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.SessionStateDir, entry.Name())
		path := filepath.Join(dir, EventsFile)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		ref := SessionRef{
			ID:            entry.Name(),
			Path:          path,
			Dir:           dir,
			CopilotDir:    s.CopilotDir,
			ModTimeUnixMS: info.ModTime().UnixMilli(),
			SizeBytes:     info.Size(),
		}
		if meta := readWorkspaceMeta(filepath.Join(dir, WorkspaceFile)); meta != nil {
			ref.Meta = meta
			if strings.TrimSpace(meta.ID) != "" {
				ref.ID = meta.ID
			}
		}
		refs = append(refs, ref)
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
				stats.FirstError = fmt.Errorf("GitHub Copilot CLI session line exceeds %d bytes", MaxLineBytes)
			}
			continue
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		record, err := decodeRecord(line, lineNo)
		if err != nil {
			stats.Malformed++
			if stats.FirstError == nil {
				stats.FirstError = err
			}
			continue
		}
		stats.Decoded++
		records = append(records, *record)
	}
	return records, stats, nil
}

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
	return bytes.TrimSuffix(bytes.TrimSuffix(buf, []byte("\n")), []byte("\r")), false, false, nil
}

func decodeRecord(line []byte, lineNo int) (*Record, error) {
	var record Record
	if err := json.Unmarshal(line, &record); err != nil {
		return nil, fmt.Errorf("GitHub Copilot CLI event line %d: %w", lineNo, err)
	}
	if strings.TrimSpace(record.Type) == "" {
		return nil, fmt.Errorf("GitHub Copilot CLI event line %d: missing type", lineNo)
	}
	record.Line = lineNo
	record.Raw = append(record.Raw[:0], line...)
	if record.Data == nil {
		record.Data = map[string]interface{}{}
	}
	return &record, nil
}

func readWorkspaceMeta(path string) *WorkspaceMeta {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var meta WorkspaceMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil
	}
	if meta.ID == "" && meta.CWD == "" && meta.GitRoot == "" && meta.Repository == "" && meta.Branch == "" && meta.Name == "" {
		return nil
	}
	return &meta
}
