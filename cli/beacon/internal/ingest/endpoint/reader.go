package endpoint

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/ingest"
)

func (s Source) Batches(state ingest.State, maxEvents int, maxBytes int) ([]ingest.Batch, error) {
	if maxEvents <= 0 {
		maxEvents = ingest.DefaultBatchEvents
	}
	if maxBytes <= 0 {
		maxBytes = ingest.DefaultBatchBytes
	}
	files, err := logFiles(s.logPath)
	if err != nil {
		return nil, err
	}
	var batches []ingest.Batch
	for _, file := range files {
		fileBatches, err := readFileBatches(file, s.logPath, state, maxEvents, maxBytes)
		if err != nil {
			return nil, err
		}
		batches = append(batches, fileBatches...)
	}
	return batches, nil
}

func readFileBatches(path string, activePath string, state ingest.State, maxEvents int, maxBytes int) ([]ingest.Batch, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	fileID := fileIdentity(file, info)
	startOffset := startOffsetForFile(path, activePath, state, info.Size(), fileID)

	if startOffset > 0 {
		if _, err := file.Seek(startOffset, 0); err != nil {
			return nil, err
		}
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxBytes)
	var (
		batches        []ingest.Batch
		events         []json.RawMessage
		batchSize      int
		batchRejected  int
		batchEndOffset int64
		batchLine      int
		line           int
		offset         = startOffset
	)
	flush := func() {
		if len(events) == 0 && batchRejected == 0 {
			return
		}
		batches = append(batches, ingest.Batch{
			Cursor: ingest.Cursor{
				LogPath: path,
				Offset:  batchEndOffset,
				Line:    batchLine,
				Archive: archiveName(activePath, path),
				FileID:  fileID,
			},
			Events:   events,
			Rejected: batchRejected,
		})
		events = nil
		batchSize = 0
		batchRejected = 0
	}

	for scanner.Scan() {
		line++
		raw := append([]byte(nil), scanner.Bytes()...)
		nextOffset := offset + int64(len(raw)) + 1
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			batchEndOffset = nextOffset
			batchLine = line
			offset = nextOffset
			continue
		}
		if len(events) > 0 && (len(events) >= maxEvents || batchSize+len(trimmed) > maxBytes) {
			flush()
		}
		if !json.Valid(trimmed) {
			batchRejected++
			batchEndOffset = nextOffset
			batchLine = line
			offset = nextOffset
			continue
		}
		events = append(events, json.RawMessage(trimmed))
		batchSize += len(trimmed)
		batchEndOffset = nextOffset
		batchLine = line
		offset = nextOffset
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	return batches, nil
}

func logFiles(path string) ([]string, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{path}, nil
		}
		return nil, err
	}
	var archives []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if _, ok := archiveIndex(base, name); ok {
			archives = append(archives, filepath.Join(dir, name))
		}
	}
	sort.Slice(archives, func(i, j int) bool {
		left, leftOK := archiveIndex(base, filepath.Base(archives[i]))
		right, rightOK := archiveIndex(base, filepath.Base(archives[j]))
		if leftOK && rightOK {
			return left > right
		}
		return archives[i] < archives[j]
	})
	return append(archives, path), nil
}

func archiveIndex(base string, name string) (int, bool) {
	suffix, ok := strings.CutPrefix(name, base+".")
	if !ok {
		return 0, false
	}
	index, err := strconv.Atoi(suffix)
	if err != nil {
		return 0, false
	}
	return index, true
}

func archiveName(activePath string, path string) string {
	if activePath == path {
		return ""
	}
	return filepath.Base(path)
}

func startOffsetForFile(path string, activePath string, state ingest.State, fileSize int64, fileID string) int64 {
	if fileID != "" && len(state.FileIDs) > 0 {
		if state.FileIDs[path] == fileID {
			return boundedOffset(state.FileOffsets[path], fileSize)
		}
		for savedPath, savedID := range state.FileIDs {
			if savedID == fileID {
				return boundedOffset(state.FileOffsets[savedPath], fileSize)
			}
		}
		if _, knownPath := state.FileIDs[path]; knownPath {
			return 0
		}
	}

	if offset := boundedOffset(state.FileOffsets[path], fileSize); offset > 0 {
		return offset
	}
	if path == activePath+".1" && state.FileIDs[activePath] == "" {
		return boundedOffset(state.FileOffsets[activePath], fileSize)
	}
	return 0
}

func boundedOffset(offset int64, fileSize int64) int64 {
	if offset <= 0 || offset > fileSize {
		return 0
	}
	return offset
}
