package beaconjsonexporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	path           string
	maxEventBytes  int
	rotateBytes    int64
	rotateArchives int
	redactSecrets  bool
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
	return appendJSONL(w.path, append(data, '\n'), w.rotateBytes, w.rotateArchives)
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

// Keep this rotation contract mirrored with the endpoint CLI and hook writer.
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
		archives = defaultRotateArchives
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
	return os.Rename(path, path+".1")
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
