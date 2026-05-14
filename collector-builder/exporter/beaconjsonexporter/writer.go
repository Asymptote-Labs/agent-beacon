package beaconjsonexporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)authorization\s*[:=]\s*bearer\s+[^"',\s]+`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|authorization)\s*[:=]\s*["']?[^"',\s]+`),
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),
}

type jsonlWriter struct {
	path          string
	maxEventBytes int
	rotateBytes   int64
	redactSecrets bool
}

func (w jsonlWriter) append(event beaconEvent) error {
	event = w.sanitize(event)
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(data) > w.maxEventBytes {
		event.Raw = nil
		event.Message = truncate(event.Message, 1024)
		event.Truncated = true
		data, err = json.Marshal(event)
		if err != nil {
			return err
		}
	}
	if len(data) > w.maxEventBytes {
		return fmt.Errorf("event exceeds maximum size after truncation: %d bytes", len(data))
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0755); err != nil {
		return err
	}
	if err := rotateIfNeeded(w.path, w.rotateBytes); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (w jsonlWriter) sanitize(event beaconEvent) beaconEvent {
	event.Message = w.cleanString(event.Message, 4096)
	if event.Tool != nil {
		event.Tool.Command = w.cleanString(event.Tool.Command, 4096)
		event.Tool.Path = truncate(event.Tool.Path, 2048)
	}
	if event.Command != nil {
		event.Command.Command = w.cleanString(event.Command.Command, 4096)
	}
	if event.Approval != nil {
		event.Approval.Reason = w.cleanString(event.Approval.Reason, 4096)
	}
	if event.Prompt != nil {
		event.Prompt.Text = w.cleanString(event.Prompt.Text, 4096)
	}
	if event.Raw != nil {
		event.Raw = w.sanitizeMap(event.Raw)
	}
	if data, err := json.Marshal(event); err == nil && len(data) > w.maxEventBytes {
		event.Truncated = true
	}
	return event
}

func (w jsonlWriter) sanitizeMap(input map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(input))
	for k, v := range input {
		switch typed := v.(type) {
		case string:
			out[k] = w.cleanString(typed, 2048)
		case map[string]interface{}:
			out[k] = w.sanitizeMap(typed)
		case []interface{}:
			out[k] = w.sanitizeSlice(typed)
		default:
			out[k] = typed
		}
	}
	return out
}

func (w jsonlWriter) sanitizeSlice(input []interface{}) []interface{} {
	out := make([]interface{}, len(input))
	for i, v := range input {
		switch typed := v.(type) {
		case string:
			out[i] = w.cleanString(typed, 2048)
		case map[string]interface{}:
			out[i] = w.sanitizeMap(typed)
		case []interface{}:
			out[i] = w.sanitizeSlice(typed)
		default:
			out[i] = typed
		}
	}
	return out
}

func (w jsonlWriter) cleanString(value string, limit int) string {
	value = truncate(value, limit)
	if w.redactSecrets {
		value = redact(value)
	}
	return value
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
	if maxSize == 0 {
		return nil
	}
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
