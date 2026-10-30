package dshsession

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

type Store struct {
	DSHHome     string
	SessionsDir string
}

func DefaultDSHHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("DSH_HOME")); value != "" {
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(home, ".dsh"), nil
}

func NewStore(dshHome string) (*Store, error) {
	root := strings.TrimSpace(dshHome)
	if root == "" {
		def, err := DefaultDSHHome()
		if err != nil {
			return nil, err
		}
		root = def
	}
	return &Store{DSHHome: root, SessionsDir: filepath.Join(root, "sessions")}, nil
}

func (s *Store) Exists() bool {
	info, err := sessionfs.Stat(s.SessionsDir)
	return err == nil && info.IsDir()
}

func (s *Store) List() ([]SessionRef, error) {
	type candidate struct {
		dir        string
		path       string
		compressed bool
	}
	byDir := map[string]candidate{}
	dirs := 0
	files := 0
	err := sessionfs.WalkDir(s.SessionsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			dirs++
			if dirs > MaxDirectories {
				return filepath.SkipDir
			}
			return nil
		}
		files++
		if files > MaxFiles {
			return filepath.SkipAll
		}
		compressed, ok := classifySessionFile(d.Name())
		if !ok {
			return nil
		}
		dir := filepath.Dir(path)
		prior, exists := byDir[dir]
		// One directory can hold both spellings; the compressed frame is the one DSH
		// writes, and it wins whichever order the walk visits them in.
		if !exists || compressed || !prior.compressed {
			byDir[dir] = candidate{dir: dir, path: path, compressed: compressed}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read DeepSeek sessions directory: %w", err)
	}
	refs := make([]SessionRef, 0, len(byDir))
	for _, item := range byDir {
		info, err := sessionfs.Stat(item.path)
		if err != nil || info.IsDir() {
			continue
		}
		ref := SessionRef{
			ID:            filepath.Base(item.dir),
			Path:          item.path,
			Dir:           item.dir,
			DSHHome:       s.DSHHome,
			ModTimeUnixMS: info.ModTime().UnixMilli(),
			SizeBytes:     info.Size(),
			Compressed:    item.compressed,
		}
		if meta := s.readMeta(ref); meta != nil {
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
	var reader io.Reader
	var stats Stats
	data, err := sessionfs.ReadFile(ref.Path)
	if err != nil {
		return nil, stats, err
	}
	if ref.Compressed {
		plain, partial, err := decodeZstdFrames(data)
		if err != nil {
			return nil, stats, err
		}
		stats.PartialFrame = partial
		stats.PartialTail = partial
		reader = bytes.NewReader(plain)
	} else {
		reader = bytes.NewReader(data)
	}
	records, lineStats, err := decodeRecords(reader)
	lineStats.PartialFrame = stats.PartialFrame
	if lineStats.PartialFrame {
		lineStats.PartialTail = true
	}
	return records, lineStats, err
}

func (s *Store) readMeta(ref SessionRef) *SessionMeta {
	records, _, err := s.Read(ref)
	if err != nil {
		return nil
	}
	meta := &SessionMeta{}
	for _, record := range records {
		switch record.Kind {
		case "session":
			meta.ID = firstString(record.Data, "id", "sessionId", "session_id")
			meta.CWD = firstString(record.Data, "cwd", "workingDirectory", "working_directory")
			meta.ParentSessionID = firstString(record.Data, "parentSession", "parent_session", "parentSessionId")
			meta.Origin = firstString(record.Data, "origin")
		case "session/title":
			meta.Title = firstString(record.Data, "title")
		case "user/message":
			if meta.FirstPrompt == "" && userMessageFromPerson(record.Data) {
				meta.FirstPrompt = strings.TrimSpace(userMessageText(record.Data))
			}
		case "request/header":
			meta.Model = requestHeaderModel(record.Data)
		}
		if meta.ID != "" && meta.CWD != "" && meta.Title != "" && meta.Model != "" {
			break
		}
	}
	if meta.ID == "" && meta.CWD == "" && meta.ParentSessionID == "" && meta.Origin == "" && meta.Title == "" && meta.FirstPrompt == "" && meta.Model == "" {
		return nil
	}
	return meta
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
				stats.FirstError = fmt.Errorf("dsh session line exceeds %d bytes", MaxLineBytes)
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

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// MaxDecodedBytes caps what one compressed session file may decompress to, across all its frames.
// A zstd frame can expand by orders of magnitude, so without a cap a few kilobytes on disk could
// claim gigabytes of memory; a session past it is refused rather than read in part.
const MaxDecodedBytes = 256 << 20

// maxDecodedBytes is MaxDecodedBytes, a variable only so tests can make it small.
var maxDecodedBytes uint64 = MaxDecodedBytes

// errDecodedTooLarge reports a session file that decompresses past maxDecodedBytes.
var errDecodedTooLarge = errors.New("DeepSeek session decompresses past the size limit")

func decodeZstdFrames(data []byte) ([]byte, bool, error) {
	limit := maxDecodedBytes
	// WithDecoderMaxMemory bounds each frame, so a frame stops decoding at the limit instead of
	// after allocating all of its declared or actual size; the running total below bounds the file.
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(limit))
	if err != nil {
		return nil, false, err
	}
	defer decoder.Close()
	var out bytes.Buffer
	partial := false
	offset := 0
	for offset < len(data) {
		next := bytes.Index(data[offset:], zstdMagic)
		if next < 0 {
			partial = strings.TrimSpace(string(data[offset:])) != ""
			break
		}
		start := offset + next
		follow := bytes.Index(data[start+len(zstdMagic):], zstdMagic)
		end := len(data)
		if follow >= 0 {
			end = start + len(zstdMagic) + follow
		}
		decoded, err := decoder.DecodeAll(data[start:end], nil)
		if err != nil {
			// Checked before the partial-tail case, so an oversized last frame is refused rather
			// than mistaken for one DSH is still writing.
			if errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded) || errors.Is(err, zstd.ErrFrameSizeExceeded) {
				return nil, false, fmt.Errorf("%w (%d bytes)", errDecodedTooLarge, limit)
			}
			if follow < 0 {
				partial = true
				break
			}
			return nil, partial, fmt.Errorf("decode DeepSeek zstd frame: %w", err)
		}
		if uint64(out.Len())+uint64(len(decoded)) > limit {
			return nil, false, fmt.Errorf("%w (%d bytes)", errDecodedTooLarge, limit)
		}
		out.Write(decoded)
		offset = end
	}
	return out.Bytes(), partial, nil
}
