package codexusage

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	SourceCodexSessionJSONL = "codex_session_jsonl"
	defaultMaxLineBytes     = 64 * 1024 * 1024
)

type UsageEvent struct {
	SessionID       string
	WorkingDir      string
	Model           string
	TurnID          string
	Timestamp       time.Time
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	ReasoningTokens int64
	SourcePath      string
	DedupKey        string
}

type ParseOptions struct {
	MaxLineBytes int
}

type ReconcileOptions struct {
	Roots         []string
	StatePath     string
	ModifiedSince time.Time
}

type ReconcileResult struct {
	Events  []UsageEvent
	Scanned int
}

type WriteFunc func(UsageEvent) error

type rawLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	ID           string `json:"id"`
	CWD          string `json:"cwd"`
	ForkedFromID string `json:"forked_from_id"`
	Timestamp    string `json:"timestamp"`
}

type turnContextPayload struct {
	Model  string `json:"model"`
	TurnID string `json:"turn_id"`
}

type eventMsgPayload struct {
	Type string `json:"type"`
	Info struct {
		LastTokenUsage json.RawMessage `json:"last_token_usage"`
	} `json:"info"`
}

type tokenUsagePayload struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	ReasoningTokens   int64 `json:"reasoning_output_tokens"`
}

type forkGate struct {
	active    bool
	createdMs int64
}

func (g *forkGate) arm(meta sessionMetaPayload, envelope time.Time) {
	if meta.ForkedFromID == "" {
		return
	}
	ms := uuidV7Millis(meta.ID)
	if ms == 0 {
		if ts := parseOptionalTimestamp(meta.Timestamp); !ts.IsZero() {
			ms = ts.UnixMilli()
		}
	}
	if ms == 0 && !envelope.IsZero() {
		ms = envelope.UnixMilli()
	}
	if ms == 0 {
		return
	}
	g.active = true
	g.createdMs = ms
}

func (g *forkGate) suppresses(lineType string, turnID string) bool {
	if !g.active {
		return false
	}
	if lineType != "turn_context" {
		return true
	}
	if turnID == "" {
		return true
	}
	if ms := uuidV7Millis(turnID); ms != 0 && ms < g.createdMs {
		return true
	}
	g.active = false
	return false
}

func ParseFile(path string, opts ParseOptions) ([]UsageEvent, error) {
	maxLineBytes := opts.MaxLineBytes
	if maxLineBytes <= 0 {
		maxLineBytes = defaultMaxLineBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var (
		events       []UsageEvent
		sessionID    string
		cwd          string
		model        string
		turnID       string
		lastUsageRaw string
		pending      *UsageEvent
		gate         forkGate
	)
	flushPending := func() {
		if pending == nil {
			return
		}
		events = append(events, *pending)
		pending = nil
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw rawLine
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		ts := parseTimestamp(raw.Timestamp)
		switch raw.Type {
		case "session_meta":
			flushPending()
			if gate.active {
				continue
			}
			var meta sessionMetaPayload
			if err := json.Unmarshal(raw.Payload, &meta); err != nil {
				continue
			}
			if meta.ID != "" {
				sessionID = meta.ID
			}
			if meta.CWD != "" {
				cwd = meta.CWD
			}
			gate.arm(meta, ts)
		case "turn_context":
			flushPending()
			var payload turnContextPayload
			if err := json.Unmarshal(raw.Payload, &payload); err != nil {
				continue
			}
			if gate.suppresses(raw.Type, payload.TurnID) {
				continue
			}
			model = strings.TrimSpace(payload.Model)
			turnID = strings.TrimSpace(payload.TurnID)
			lastUsageRaw = ""
		case "response_item":
			if gate.suppresses(raw.Type, "") {
				continue
			}
		case "event_msg":
			if gate.suppresses(raw.Type, "") {
				continue
			}
			var payload eventMsgPayload
			if err := json.Unmarshal(raw.Payload, &payload); err != nil {
				continue
			}
			if payload.Type != "token_count" || len(payload.Info.LastTokenUsage) == 0 {
				continue
			}
			usageRaw := compactJSON(payload.Info.LastTokenUsage)
			if usageRaw == "" || usageRaw == lastUsageRaw {
				continue
			}
			lastUsageRaw = usageRaw
			var usage tokenUsagePayload
			if err := json.Unmarshal(payload.Info.LastTokenUsage, &usage); err != nil {
				continue
			}
			input := usage.InputTokens - usage.CachedInputTokens
			if input < 0 {
				input = 0
			}
			if input == 0 && usage.OutputTokens == 0 && usage.CachedInputTokens == 0 && usage.ReasoningTokens == 0 {
				continue
			}
			if sessionID == "" {
				sessionID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			event := UsageEvent{
				SessionID:       sessionID,
				WorkingDir:      cwd,
				Model:           model,
				TurnID:          turnID,
				Timestamp:       ts,
				InputTokens:     input,
				OutputTokens:    usage.OutputTokens,
				CacheReadTokens: usage.CachedInputTokens,
				ReasoningTokens: usage.ReasoningTokens,
				SourcePath:      path,
			}
			event.DedupKey = DedupKey(event, usageRaw)
			pending = &event
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flushPending()
	return events, nil
}

func Reconcile(opts ReconcileOptions) (ReconcileResult, error) {
	files, err := DiscoverFiles(opts.Roots)
	if err != nil {
		return ReconcileResult{}, err
	}
	seen, err := LoadState(opts.StatePath)
	if err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{Scanned: len(files)}
	for _, path := range files {
		if !opts.ModifiedSince.IsZero() {
			info, err := os.Stat(path)
			if err != nil || info.ModTime().Before(opts.ModifiedSince) {
				continue
			}
		}
		events, err := ParseFile(path, ParseOptions{})
		if err != nil {
			continue
		}
		for _, event := range events {
			if event.DedupKey == "" || seen[event.DedupKey] {
				continue
			}
			seen[event.DedupKey] = true
			result.Events = append(result.Events, event)
		}
	}
	return result, nil
}

func ReconcileAndWrite(opts ReconcileOptions, write WriteFunc) (ReconcileResult, error) {
	if write == nil {
		return ReconcileResult{}, fmt.Errorf("nil Codex usage writer")
	}
	statePath := resolveStatePath(opts.StatePath)
	unlock, err := lockState(statePath)
	if err != nil {
		return ReconcileResult{}, err
	}
	defer unlock()

	files, err := DiscoverFiles(opts.Roots)
	if err != nil {
		return ReconcileResult{}, err
	}
	seen, err := loadStateFile(statePath)
	if err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{Scanned: len(files)}
	for _, path := range files {
		if !opts.ModifiedSince.IsZero() {
			info, err := os.Stat(path)
			if err != nil || info.ModTime().Before(opts.ModifiedSince) {
				continue
			}
		}
		events, err := ParseFile(path, ParseOptions{})
		if err != nil {
			continue
		}
		for _, event := range events {
			if event.DedupKey == "" || seen[event.DedupKey] {
				continue
			}
			seen[event.DedupKey] = true
			if err := saveStateFile(statePath, seen); err != nil {
				return result, err
			}
			if err := write(event); err != nil {
				delete(seen, event.DedupKey)
				_ = saveStateFile(statePath, seen)
				return result, err
			}
			result.Events = append(result.Events, event)
		}
	}
	return result, nil
}

func MarkEventsSeen(events []UsageEvent, statePath string) error {
	if len(events) == 0 {
		return nil
	}
	seen, err := LoadState(statePath)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.DedupKey != "" {
			seen[event.DedupKey] = true
		}
	}
	return SaveState(statePath, seen)
}

func MarkEventSeen(event UsageEvent, statePath string) error {
	if event.DedupKey == "" {
		return nil
	}
	return MarkEventsSeen([]UsageEvent{event}, statePath)
}

func DiscoverFiles(roots []string) ([]string, error) {
	if len(roots) == 0 {
		defaults, err := DefaultRoots()
		if err != nil {
			return nil, err
		}
		roots = defaults
	}
	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".jsonl") && !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
			return nil
		}); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func DefaultRoots() ([]string, error) {
	if override := strings.TrimSpace(os.Getenv("BEACON_CODEX_SESSIONS_DIR")); override != "" {
		return splitList(override), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return []string{
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".codex", "archived_sessions"),
	}, nil
}

func DefaultStatePath() string {
	if path := strings.TrimSpace(os.Getenv("BEACON_CODEX_USAGE_STATE")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".beacon", "endpoint", "state", "codex_usage_seen.json")
	}
	return filepath.Join(home, ".beacon", "endpoint", "state", "codex_usage_seen.json")
}

func LoadState(path string) (map[string]bool, error) {
	return loadStateFile(resolveStatePath(path))
}

func loadStateFile(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	var seen map[string]bool
	if err := json.Unmarshal(data, &seen); err != nil {
		return nil, err
	}
	if seen == nil {
		seen = map[string]bool{}
	}
	return seen, nil
}

func SaveState(path string, seen map[string]bool) error {
	return saveStateFile(resolveStatePath(path), seen)
}

func saveStateFile(path string, seen map[string]bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(seen, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func resolveStatePath(path string) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	return DefaultStatePath()
}

func SourcePathHash(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(sum[:])
}

func DedupKey(event UsageEvent, usageRaw string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%d|%d|%d|%d|%s",
		SourceCodexSessionJSONL,
		filepath.Clean(event.SourcePath),
		event.SessionID,
		event.TurnID,
		event.Model,
		event.InputTokens,
		event.OutputTokens,
		event.CacheReadTokens,
		event.ReasoningTokens,
		usageRaw,
	)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func parseTimestamp(raw string) time.Time {
	if ts := parseOptionalTimestamp(raw); !ts.IsZero() {
		return ts
	}
	return time.Now().UTC()
}

func parseOptionalTimestamp(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts.UTC()
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC()
	}
	return time.Time{}
}

func compactJSON(raw json.RawMessage) string {
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func uuidV7Millis(id string) int64 {
	cleaned := strings.ReplaceAll(id, "-", "")
	if len(cleaned) != 32 || cleaned[12] != '7' {
		return 0
	}
	var value int64
	for _, ch := range cleaned[:12] {
		value <<= 4
		switch {
		case ch >= '0' && ch <= '9':
			value += int64(ch - '0')
		case ch >= 'a' && ch <= 'f':
			value += int64(ch-'a') + 10
		case ch >= 'A' && ch <= 'F':
			value += int64(ch-'A') + 10
		default:
			return 0
		}
	}
	return value
}

func splitList(value string) []string {
	separator := os.PathListSeparator
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == separator })
	var out []string
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
