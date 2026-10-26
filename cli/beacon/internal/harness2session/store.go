package harness2session

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
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

const (
	// MaxTrajectoryBytes bounds the file a sweep will read. A rollout's stream is megabytes at
	// most; a file far past that is not a trajectory worth holding in memory.
	MaxTrajectoryBytes = 256 << 20
	// MaxLineBytes bounds one event. An OpenCode tool part carries its whole output, so the limit
	// is generous, and a longer line is skipped as malformed rather than read.
	MaxLineBytes = 16 << 20
	// maxWalkDepth bounds how deep under a runs root the walk looks. The deepest layout Harness²
	// writes (WorkBuddy's trials) is under ten levels.
	maxWalkDepth = 12
	// sniffLines is how many lines the format check reads before giving up on a file. An OpenCode
	// stream captured from stdout can open with a few lines of banner before its first event.
	sniffLines = 64
	// markerName is the completion marker Harness² writes beside a finished rollout
	// (harness2/core/runmark.py).
	markerName = ".adapt_complete"
)

// skipDirs are directories under a runs root that hold no trajectories but can hold a great deal
// else: the task inputs and deliverables Harness² copies around, the executor's own private state,
// and anything a Python or Node toolchain leaves behind. Not walking them keeps a sweep proportional
// to the number of rollouts rather than to the size of the benchmark data.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true, ".venv": true, "venv": true,
	".ocdata": true, ".ocstate": true, "task_folder": true, "model_output": true, "documents": true,
	"dependencies": true,
}

// Store is the set of runs roots a sweep reads.
type Store struct {
	Roots []string
}

// DefaultRunsDirs is where Harness² writes when nothing says otherwise: $HARNESS2_RUNS_DIR when it
// is set, as Harness² itself reads it, and otherwise the runs directory of a checkout at
// ~/harness2. Harness² defaults to runs/ inside its own checkout, which can be anywhere, so a
// checkout elsewhere is named with --runs-dir.
func DefaultRunsDirs() []string {
	if dir := strings.TrimSpace(os.Getenv("HARNESS2_RUNS_DIR")); dir != "" {
		return []string{expandHome(dir)}
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	return []string{filepath.Join(home, "harness2", "runs")}
}

// NewStore returns a store over roots, or over DefaultRunsDirs when none are given. Roots are made
// absolute and deduplicated, so the same directory named two ways is read once.
func NewStore(roots []string) (*Store, error) {
	if len(roots) == 0 {
		roots = DefaultRunsDirs()
	}
	seen := map[string]bool{}
	store := &Store{}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(expandHome(root))
		if err != nil {
			return nil, fmt.Errorf("Harness² runs directory %s: %w", root, err)
		}
		abs = filepath.Clean(abs)
		if seen[abs] {
			continue
		}
		seen[abs] = true
		store.Roots = append(store.Roots, abs)
	}
	return store, nil
}

// Exists reports whether any root is a directory.
func (s *Store) Exists() bool {
	for _, root := range s.Roots {
		if info, err := sessionfs.Stat(root); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// List returns every trajectory under the store's roots, oldest first. A root that does not exist
// contributes nothing. One that cannot be read -- unreadable, or not a directory -- contributes
// nothing either, and is reported in the returned error while the other roots are still listed: a
// remembered --runs-dir that later broke must not stop every scheduled sync from reading the rest.
func (s *Store) List() ([]TrajectoryRef, error) {
	refs, errs := s.list()
	return refs, errors.Join(errs...)
}

// list is List with one error per root that could not be read.
func (s *Store) list() ([]TrajectoryRef, []error) {
	var refs []TrajectoryRef
	var errs []error
	seen := map[string]bool{}
	for _, root := range s.Roots {
		found, err := listRoot(root)
		if err != nil {
			errs = append(errs, err)
		}
		for _, ref := range found {
			// Nested roots (a runs directory and one of its benchmark directories) would otherwise
			// list the same file twice, under two cursors, and write its events twice.
			if seen[ref.Path] {
				continue
			}
			seen[ref.Path] = true
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ModTimeUnixMS != refs[j].ModTimeUnixMS {
			return refs[i].ModTimeUnixMS < refs[j].ModTimeUnixMS
		}
		return refs[i].Path < refs[j].Path
	})
	return refs, errs
}

func listRoot(root string) ([]TrajectoryRef, error) {
	info, err := sessionfs.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("Harness² runs directory %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("Harness² runs directory %s is not a directory", root)
	}
	// A runs directory is often a symlink to a larger disk. Walking the link itself would visit only
	// the link -- filepath.WalkDir does not descend into a symlinked root -- and find nothing, so the
	// walk starts from the directory it names. A confined read walks through the home's os.Root,
	// which descends on its own and must not be handed a path the link resolves to outside the home.
	if !sessionfs.Confined(root) {
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
	}
	var refs []TrajectoryRef
	err = sessionfs.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			// One unreadable directory -- a container left it root-owned, a refused path under
			// confinement -- loses only what is inside it.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if skipDirs[d.Name()] || strings.Count(filepath.ToSlash(rel), "/") >= maxWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !candidateName(d.Name()) {
			return nil
		}
		fi, err := d.Info()
		if err != nil || fi.Size() == 0 || fi.Size() > MaxTrajectoryBytes {
			return nil
		}
		format, err := sniffFormat(path)
		if err != nil || format == "" {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		refs = append(refs, TrajectoryRef{
			Path:          path,
			Root:          root,
			Rel:           slashRel,
			Format:        format,
			SizeBytes:     fi.Size(),
			ModTimeUnixMS: fi.ModTime().UnixMilli(),
			Context:       runContext(root, path, slashRel),
		})
		return nil
	})
	if err != nil {
		return refs, fmt.Errorf("Harness² runs directory %s: %w", root, err)
	}
	return refs, nil
}

// candidateName is the cheap filter ahead of reading a file: JSONL, plus the one name WorkBuddy
// gives OpenCode's captured stdout.
func candidateName(name string) bool {
	return strings.HasSuffix(name, ".jsonl") || name == "oc-output.txt"
}

var openCodeEventTypes = map[string]bool{
	"step_start": true, "step_finish": true, "text": true, "reasoning": true, "tool_use": true, "error": true,
}

var codexExecEventTypes = map[string]bool{
	"thread.started": true, "turn.started": true, "turn.completed": true, "turn.failed": true,
	"item.started": true, "item.updated": true, "item.completed": true,
}

// sniffFormat reads the first event of a file and says which stream it is, or "" for a file that is
// not a trajectory. The first JSON object decides: lines before it that are not JSON are skipped,
// since captured stdout can open with a banner, but a file whose first object is something else --
// a result record, a judge's output -- is not read further.
func sniffFormat(path string) (Format, error) {
	f, err := sessionfs.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64*1024)
	for i := 0; i < sniffLines; i++ {
		line, partial, oversized, err := readLine(br)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		// A line without its newline is still being written; it decides nothing until it is whole.
		if !oversized && !partial {
			if format, decided := classifyFirstLine(line); decided {
				return format, nil
			}
		}
		if errors.Is(err, io.EOF) || partial {
			return "", nil
		}
	}
	return "", nil
}

func classifyFirstLine(line []byte) (Format, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return "", false
	}
	var head struct {
		Type      string          `json:"type"`
		SessionID json.RawMessage `json:"sessionID"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		// A line that opens like JSON and does not parse is a fragment of something else. The
		// stream formats both write one whole object per line, so this is not one of them.
		return "", true
	}
	switch {
	case openCodeEventTypes[head.Type] && len(head.SessionID) > 0:
		return FormatOpenCode, true
	case codexExecEventTypes[head.Type]:
		return FormatCodexExec, true
	case head.Type == "error":
		// Codex writes a bare {"type":"error"} for a stream error; with no session id it is not
		// OpenCode's, and a stream that fails before its thread starts has nothing else to map.
		return FormatCodexExec, true
	default:
		return "", true
	}
}

// Read decodes every complete line of a trajectory.
func (s *Store) Read(ref TrajectoryRef) (Trajectory, Stats, error) {
	f, err := sessionfs.Open(ref.Path)
	if err != nil {
		return Trajectory{Ref: ref}, Stats{}, err
	}
	defer f.Close()
	return decodeTrajectory(ref, f)
}

func decodeTrajectory(ref TrajectoryRef, r io.Reader) (Trajectory, Stats, error) {
	out := Trajectory{Ref: ref}
	var stats Stats
	br := bufio.NewReaderSize(r, 64*1024)
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
			return out, stats, fmt.Errorf("read %s: %w", ref.Path, err)
		}
		lineNo++
		out.LineCount = lineNo
		if oversized {
			stats.Malformed++
			continue
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		object, ok := decodeObject(trimmed)
		if !ok {
			stats.Malformed++
			continue
		}
		out.Lines = append(out.Lines, Line{Number: lineNo, Object: object})
	}
	return out, stats, nil
}

// readLine returns one complete line without its trailing newline. A trailing fragment with no
// newline is returned with partial set and io.EOF, so the caller leaves it for a later read; a line
// longer than MaxLineBytes is consumed and reported as oversized so a corrupt file cannot dictate
// memory use.
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

var (
	attemptPattern = regexp.MustCompile(`_attempt(\d+)_`)
	wbSubsets      = map[string]bool{"office": true, "web": true, "code": true}
	benchmarks     = map[string]bool{"jb": true, "lab": true, "wb": true}
)

// runContext reads what the layouts in the package comment say about a rollout from where its file
// sits, and the run id and harness fingerprint from the completion marker beside it.
func runContext(root, path, rel string) RunContext {
	var ctx RunContext
	segments := strings.Split(rel, "/")
	dirs := segments[:len(segments)-1]
	if len(dirs) > 0 && benchmarks[dirs[0]] {
		ctx.Benchmark = dirs[0]
	}
	switch ctx.Benchmark {
	case "jb":
		if i := indexOf(dirs, "model_traj"); i >= 2 {
			ctx.TaskID = dirs[i-2] + "/" + dirs[i-1]
			if i+1 < len(dirs) {
				ctx.Label = dirs[i+1]
			}
		}
		if m := attemptPattern.FindStringSubmatch(segments[len(segments)-1]); m != nil {
			ctx.Attempt, _ = strconv.Atoi(m[1])
		}
	case "lab":
		if len(dirs) >= 4 && dirs[1] == "results" {
			ctx.RunID = dirs[3]
		}
	case "wb":
		if len(dirs) > 1 && wbSubsets[dirs[1]] {
			ctx.Subset = dirs[1]
		}
		if i := lastIndexOf(dirs, "agent"); i >= 1 {
			ctx.Trial = dirs[i-1]
		}
	}
	// The marker sits in the run directory: the file's own directory for LAB, its parent for a
	// WorkBuddy trial's agent/ stream. Looking no higher keeps one run's marker from being read as
	// another's.
	dir := filepath.Dir(path)
	for i := 0; i < 2 && strings.HasPrefix(dir, root) && dir != root; i++ {
		if marker, ok := readMarker(filepath.Join(dir, markerName)); ok {
			if ctx.RunID == "" {
				ctx.RunID = marker.RunID
			}
			ctx.HarnessFingerprint = marker.Harness
			break
		}
		dir = filepath.Dir(dir)
	}
	return ctx
}

type completionMarker struct {
	RunID   string `json:"run_id"`
	Harness string `json:"harness"`
}

func readMarker(path string) (completionMarker, bool) {
	data, err := sessionfs.ReadFile(path)
	if err != nil || len(data) > 64*1024 {
		return completionMarker{}, false
	}
	var marker completionMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return completionMarker{}, false
	}
	return marker, marker.RunID != "" || marker.Harness != ""
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}

func lastIndexOf(values []string, want string) int {
	for i := len(values) - 1; i >= 0; i-- {
		if values[i] == want {
			return i
		}
	}
	return -1
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}
