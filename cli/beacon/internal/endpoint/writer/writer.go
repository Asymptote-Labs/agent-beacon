package writer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

const (
	UserLogPath           = ".beacon/endpoint/logs/runtime.jsonl"
	MaxEventBytes         = 64 * 1024
	DefaultRotateBytes    = 10 * 1024 * 1024
	DefaultRotateArchives = 5
	RotateBytes           = DefaultRotateBytes
	runtimeFileMode       = 0666
)

type Options struct {
	Path           string
	UserMode       bool
	MaxBytes       int
	RotateSize     int64
	RotateArchives int
}

// SystemLogPath is the system-mode runtime log, resolved per platform.
//
// Delegated rather than duplicated: this was one of fourteen copies of the same literal, and the
// copies could not be given a Windows value independently without disagreeing.
func SystemLogPath() string { return endpointconfig.SystemLogPath() }

func DefaultPath(userMode bool) string {
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", UserLogPath)
		}
		return filepath.Join(home, UserLogPath)
	}
	return SystemLogPath()
}

func AppendEvent(event schema.Event, opts Options) (string, error) {
	if opts.Path == "" {
		opts.Path = DefaultPath(opts.UserMode)
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = MaxEventBytes
	}
	if opts.RotateSize <= 0 {
		opts.RotateSize = DefaultRotateBytes
	}
	if opts.RotateArchives < 1 {
		opts.RotateArchives = DefaultRotateArchives
	}
	event = SanitizeEvent(event, opts.MaxBytes)
	if err := event.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	if len(data) > opts.MaxBytes {
		event.Raw = nil
		event.Message = asymptoteobserve.TruncateString(event.Message, 1024)
		event.Truncated = true
		data, err = json.Marshal(event)
		if err != nil {
			return "", err
		}
	}
	if len(data) > opts.MaxBytes {
		return "", fmt.Errorf("event exceeds maximum size after truncation: %d bytes", len(data))
	}
	if err := appendJSONL(opts.Path, append(data, '\n'), opts.RotateSize, opts.RotateArchives); err != nil {
		return "", err
	}
	return opts.Path, nil
}

// EnsureSystemLogWritable makes a machine-wide log directory writable by the people whose agent
// sessions this endpoint captures.
//
// A no-op on POSIX, where the file mode carries the same guarantee. On Windows it is the step
// without which a system-mode install produces a healthy collector that records nothing the agent
// did: %ProgramData% denies ordinary users write access, hooks run as the console user, and
// nothing reports an error because status and doctor both describe the collector rather than the
// hooks exporting to it.
func EnsureSystemLogWritable(dir string) error {
	return grantInteractiveUsersWrite(dir)
}

// SystemLogWritableByUsers reports whether that grant is in place, for doctor.
func SystemLogWritableByUsers(dir string) (bool, error) {
	return interactiveUsersCanWrite(dir)
}

// EnsureRuntimeFile creates the shared runtime log with the shared writable
// mode without appending an event, for doctor --fix and setup flows that
// pre-create the log so user-run hooks can append to it later.
func EnsureRuntimeFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := openRuntimeFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return err
	}
	return f.Close()
}

func LastLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var last string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxEventBytes)
	for scanner.Scan() {
		last = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return last, nil
}

func SanitizeEvent(event schema.Event, maxBytes int) schema.Event {
	return asymptoteobserve.SanitizeEvent(event, maxBytes)
}

func appendJSONL(path string, line []byte, rotateBytes int64, rotateArchives int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	lock, err := openRuntimeFile(path+".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	held, err := filelock.Exclusive(lock)
	if err != nil {
		_ = lock.Close()
		return err
	}
	// Unlock before closing. The previous form deferred the unlock first and the close second,
	// which LIFO ordering ran in the opposite order -- it worked only because closing a descriptor
	// releases the lock implicitly, leaving the explicit unlock to fail on a closed handle and have
	// its error discarded.
	defer lock.Close()
	defer held.Release()
	if asymptoteobserve.IsDuplicateEndpointEvent(path, line, asymptoteobserve.EndpointDuplicateWindow) {
		return nil
	}
	if err := rotateIfNeeded(path, rotateBytes, rotateArchives, int64(len(line))); err != nil {
		return err
	}
	f, err := openRuntimeFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

func openRuntimeFile(path string, flag int) (*os.File, error) {
	existed := true
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		existed = false
	}
	f, err := os.OpenFile(path, flag, runtimeFileMode)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, runtimeFileMode); err != nil && !existed {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func rotateIfNeeded(path string, maxSize int64, archives int, nextWriteBytes int64) error {
	if maxSize <= 0 {
		return nil
	}
	if archives < 1 {
		archives = DefaultRotateArchives
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() == 0 || info.Size()+nextWriteBytes <= maxSize {
		return nil
	}
	if err := removeOverflowArchives(path, archives); err != nil {
		return err
	}
	for i := archives - 1; i >= 1; i-- {
		from := path + fmt.Sprintf(".%d", i)
		to := path + fmt.Sprintf(".%d", i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	rotated := path + ".1"
	return os.Rename(path, rotated)
}

func removeOverflowArchives(path string, archives int) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	prefix := base + "."
	for _, entry := range entries {
		name := entry.Name()
		suffix, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		index, err := strconv.Atoi(suffix)
		if err != nil || index < archives {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
