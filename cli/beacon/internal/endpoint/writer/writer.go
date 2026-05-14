package writer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

const (
	SystemLogPath = "/var/log/beacon-agent/runtime.jsonl"
	UserLogPath   = ".beacon/endpoint/logs/runtime.jsonl"
	MaxEventBytes = 64 * 1024
	RotateBytes   = 10 * 1024 * 1024
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)authorization\s*[:=]\s*bearer\s+[^"',\s]+`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|authorization)\s*[:=]\s*["']?[^"',\s]+`),
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),
}

type Options struct {
	Path       string
	UserMode   bool
	MaxBytes   int
	RotateSize int64
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
	if opts.RotateSize == 0 {
		opts.RotateSize = RotateBytes
	}
	event = SanitizeEvent(event, opts.MaxBytes)
	if err := event.Validate(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0755); err != nil {
		return "", err
	}
	if err := rotateIfNeeded(opts.Path, opts.RotateSize); err != nil {
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
	f, err := os.OpenFile(opts.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "", err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return "", err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
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

func rotateIfNeeded(path string, maxSize int64) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() <= maxSize {
		return nil
	}
	rotated := path + ".1"
	_ = os.Remove(rotated)
	return os.Rename(path, rotated)
}
