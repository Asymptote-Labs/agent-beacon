package writer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

const (
	SystemLogPath         = "/var/log/beacon-agent/runtime.jsonl"
	UserLogPath           = ".beacon/endpoint/logs/runtime.jsonl"
	MaxEventBytes         = 64 * 1024
	DefaultRotateBytes    = 10 * 1024 * 1024
	DefaultRotateArchives = 5
	RotateBytes           = DefaultRotateBytes
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)authorization\s*[:=]\s*bearer\s+[^"',\s]+`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|authorization)\s*[:=]\s*["']?[^"',\s]+`),
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),
}

type Options struct {
	Path           string
	UserMode       bool
	MaxBytes       int
	RotateSize     int64
	RotateArchives int
}

func DefaultPath(userMode bool) string {
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", UserLogPath)
		}
		return filepath.Join(home, UserLogPath)
	}
	return SystemLogPath
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
		event.Message = truncate(event.Message, 1024)
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
	event.Message = redact(truncate(event.Message, 4096))
	if event.Tool != nil {
		event.Tool.Command = redact(truncate(event.Tool.Command, 4096))
		event.Tool.Path = truncate(event.Tool.Path, 2048)
	}
	if event.Command != nil {
		event.Command.Command = redact(truncate(event.Command.Command, 4096))
	}
	if event.Approval != nil {
		event.Approval.Reason = redact(truncate(event.Approval.Reason, 4096))
	}
	if event.Policy != nil {
		event.Policy.Reason = redact(truncate(event.Policy.Reason, 4096))
	}
	if event.Prompt != nil {
		event.Prompt.Text = redact(truncate(event.Prompt.Text, 4096))
	}
	if event.Raw != nil {
		event.Raw = sanitizeMap(event.Raw)
	}
	if data, err := json.Marshal(event); err == nil && len(data) > maxBytes {
		event.Truncated = true
	}
	return event
}

func sanitizeMap(input map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(input))
	for k, v := range input {
		switch typed := v.(type) {
		case string:
			out[k] = redact(truncate(typed, 2048))
		case map[string]interface{}:
			out[k] = sanitizeMap(typed)
		case []interface{}:
			out[k] = sanitizeSlice(typed)
		default:
			out[k] = typed
		}
	}
	return out
}

func sanitizeSlice(input []interface{}) []interface{} {
	out := make([]interface{}, len(input))
	for i, v := range input {
		switch typed := v.(type) {
		case string:
			out[i] = redact(truncate(typed, 2048))
		case map[string]interface{}:
			out[i] = sanitizeMap(typed)
		case []interface{}:
			out[i] = sanitizeSlice(typed)
		default:
			out[i] = typed
		}
	}
	return out
}

func redact(value string) string {
	for _, pattern := range secretPatterns {
		value = pattern.ReplaceAllStringFunc(value, func(match string) string {
			if strings.Contains(match, "=") {
				return match[:strings.Index(match, "=")+1] + "[REDACTED]"
			}
			if strings.Contains(match, ":") {
				return match[:strings.Index(match, ":")+1] + "[REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return value
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit < 32 {
		return value[:limit]
	}
	return value[:limit-15] + "...[truncated]"
}

func appendJSONL(path string, line []byte, rotateBytes int64, rotateArchives int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	defer lock.Close()
	if err := rotateIfNeeded(path, rotateBytes, rotateArchives, int64(len(line))); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
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
