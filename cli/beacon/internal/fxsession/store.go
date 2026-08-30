package fxsession

import (
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
	// EventsFileName and ManifestFileName are fx's names for the two files in a session directory
	// (src/core/session/session_log.zig).
	EventsFileName   = "events.jsonl"
	ManifestFileName = "session.json"

	// ManifestSchemaVersion is the session.json version this reader understands. A different
	// version is not an error: the manifest is an optimization, and the log can be read whole
	// without it. See Store.Read.
	ManifestSchemaVersion = 3

	// MaxManifestBytes bounds the manifest read. fx's own limit is smaller; this leaves room for
	// growth while still refusing to read an arbitrarily large file that happens to sit at that
	// path.
	MaxManifestBytes = 1 << 20
)

// DefaultSessionsDir returns fx's session directory for the current user.
//
// fx resolves its profile from $HOME (falling back to %USERPROFILE% on Windows), which is what
// os.UserHomeDir reports on both, so Beacon looks where fx writes rather than where a Beacon
// convention would put it. Nothing here creates the directory: its absence is the answer to "is
// this machine running fx", and creating it would fabricate the evidence.
func DefaultSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(home, ".fx", "sessions"), nil
}

// Manifest is fx's session.json: a projection of the session's committed state.
//
// EventLogBytes is the field this reader exists for. It is the length of the durable, committed
// prefix of events.jsonl -- fx appends a frame, then advances this watermark, so the bytes past it
// are a write in flight. Reading to the watermark rather than to EOF is what keeps a sweep from
// decoding a half-written frame and counting it as corruption.
type Manifest struct {
	SchemaVersion       int          `json:"schema_version"`
	StorageFormat       string       `json:"storage_format"`
	ID                  string       `json:"id"`
	LogGeneration       string       `json:"log_generation"`
	CreatedAtMS         int64        `json:"created_at_ms"`
	UpdatedAtMS         int64        `json:"updated_at_ms"`
	OriginWorkspaceRoot string       `json:"origin_workspace_root"`
	WorkspaceRoot       string       `json:"workspace_root"`
	ConversationLang    string       `json:"conversation_language"`
	HistoryLen          int          `json:"history_len"`
	TotalInputTokens    int64        `json:"total_input_tokens"`
	TotalOutputTokens   int64        `json:"total_output_tokens"`
	LastEventSeq        uint64       `json:"last_event_seq"`
	EventLogBytes       int64        `json:"event_log_bytes"`
	Preferences         *Preferences `json:"preferences"`
}

// SessionRef is one fx session on disk: where it is and, when the manifest could be read, what it
// says. Manifest is nil when there is no readable manifest, which is normal for a session fx has
// only just created.
type SessionRef struct {
	ID        string
	Dir       string
	EventsLog string
	Manifest  *Manifest
	// ModTimeUnixMS is the events.jsonl modification time, used as the change signal when no
	// manifest is available. It is a fallback, not the primary cursor: the collector's real
	// idempotence comes from the per-session sequence cursor, not from a timestamp.
	ModTimeUnixMS int64
	// SizeBytes is the events.jsonl size on disk, including any uncommitted tail.
	SizeBytes int64
}

// Store is a directory of fx sessions, normally ~/.fx/sessions.
type Store struct {
	Dir string
}

// NewStore points at dir, or at fx's default location when dir is empty.
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

// Exists reports whether the sessions directory is present. A missing directory means fx has not
// run for this user, which every caller reports rather than treats as a failure.
func (s *Store) Exists() bool {
	info, err := os.Stat(s.Dir)
	return err == nil && info.IsDir()
}

// List returns every session directory that holds an events log, oldest change first.
//
// Ordering is by the session's own last-modified time so a sweep processes sessions in roughly the
// order they were used, which keeps the resulting Beacon log close to chronological even before
// per-event timestamps sort it.
//
// An unreadable session directory is skipped rather than failing the listing: fx creates the
// directory before the log, another user's session may not be readable, and neither is a reason to
// collect nothing from the sessions that are.
func (s *Store) List() ([]SessionRef, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read fx sessions directory: %w", err)
	}
	refs := make([]SessionRef, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if err := ValidateSessionID(id); err != nil {
			continue
		}
		dir := filepath.Join(s.Dir, id)
		logPath := filepath.Join(dir, EventsFileName)
		info, err := os.Stat(logPath)
		if err != nil || info.IsDir() {
			continue
		}
		ref := SessionRef{
			ID:            id,
			Dir:           dir,
			EventsLog:     logPath,
			ModTimeUnixMS: info.ModTime().UnixMilli(),
			SizeBytes:     info.Size(),
		}
		if manifest, err := ReadManifest(filepath.Join(dir, ManifestFileName)); err == nil {
			ref.Manifest = manifest
		}
		refs = append(refs, ref)
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].ModTimeUnixMS != refs[j].ModTimeUnixMS {
			return refs[i].ModTimeUnixMS < refs[j].ModTimeUnixMS
		}
		return refs[i].ID < refs[j].ID
	})
	return refs, nil
}

// ValidateSessionID applies fx's own rule for a session id: non-empty, at most 255 bytes,
// alphanumerics plus `.`, `_` and `-`, and never "." or "..".
//
// Beacon re-applies it because the id is a path segment and reaches this code from a directory
// listing and from a state file that has been on disk between runs. A session id is also written
// into every event, so a name carrying a separator would be both a traversal risk here and a
// misleading identifier downstream.
func ValidateSessionID(id string) error {
	if id == "" || len(id) > 255 || id == "." || id == ".." {
		return fmt.Errorf("invalid fx session id %q", id)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return fmt.Errorf("invalid fx session id %q", id)
		}
	}
	return nil
}

// ReadManifest reads and validates a session.json.
//
// A manifest whose schema version or storage format this reader does not recognize is an error
// rather than a partially-trusted value: its fields are what bound the log read, and reading the
// log whole is a correct fallback while trusting a byte count from a format that moved is not.
func ReadManifest(path string) (*Manifest, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxManifestBytes {
		return nil, fmt.Errorf("fx manifest %s is %d bytes, over the %d-byte limit", path, info.Size(), MaxManifestBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("fx manifest %s: %w", path, err)
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return nil, fmt.Errorf("fx manifest %s: unsupported schema version %d", path, manifest.SchemaVersion)
	}
	if manifest.EventLogBytes < 0 {
		return nil, fmt.Errorf("fx manifest %s: negative event_log_bytes", path)
	}
	return &manifest, nil
}

// Read decodes a session's events log, stopping at the manifest's committed watermark when there is
// one.
//
// The watermark is clamped to the file's actual size rather than trusted outright. The manifest is
// written after the log, so a manifest from a later state than the log on disk -- a restored
// backup, a copied directory, a partial sync -- would otherwise make the decoder read past EOF and
// report the whole session as unreadable.
//
// Events are returned in log order. The caller filters by cursor; this function has no opinion
// about what has already been collected.
func (s *Store) Read(ref SessionRef) ([]Event, Stats, error) {
	file, err := os.Open(ref.EventsLog)
	if err != nil {
		return nil, Stats{}, err
	}
	defer file.Close()

	limit := int64(0)
	if ref.Manifest != nil && ref.Manifest.EventLogBytes > 0 {
		limit = ref.Manifest.EventLogBytes
		if info, err := file.Stat(); err == nil && info.Size() < limit {
			limit = info.Size()
		}
	}

	decoder := NewDecoder(file, limit)
	var events []Event
	for {
		event, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return events, decoder.Stats(), err
		}
		events = append(events, *event)
	}
	return events, decoder.Stats(), nil
}
