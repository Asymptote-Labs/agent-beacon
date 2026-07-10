package beaconjsonexporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

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
		event.Message = asymptoteobserve.TruncateString(event.Message, 1024)
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
	event.Message = w.cleanString(event.Message, asymptoteobserve.DefaultStringLimit)
	if event.Tool != nil {
		event.Tool.Command = w.cleanString(event.Tool.Command, asymptoteobserve.DefaultStringLimit)
		event.Tool.Path = asymptoteobserve.TruncateString(event.Tool.Path, asymptoteobserve.DefaultRawStringLimit)
	}
	if event.Command != nil {
		event.Command.Command = w.cleanString(event.Command.Command, asymptoteobserve.DefaultStringLimit)
	}
	if event.Approval != nil {
		event.Approval.Reason = w.cleanString(event.Approval.Reason, asymptoteobserve.DefaultStringLimit)
	}
	if event.Policy != nil {
		event.Policy.Reason = w.cleanString(event.Policy.Reason, asymptoteobserve.DefaultStringLimit)
	}
	if event.Prompt != nil {
		event.Prompt.Text = w.cleanString(event.Prompt.Text, asymptoteobserve.DefaultStringLimit)
	}
	if event.MCP != nil {
		event.MCP = sanitizeTyped(event.MCP, w.sanitizeMap)
	}
	if event.GenAI != nil {
		event.GenAI = sanitizeTyped(event.GenAI, w.sanitizeMap)
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
	return asymptoteobserve.SanitizeMap(input, asymptoteobserve.PrivacyOptions{
		RedactSecrets: w.redactSecrets,
		StringLimit:   asymptoteobserve.DefaultRawStringLimit,
	})
}

func (w jsonlWriter) sanitizeSlice(input []interface{}) []interface{} {
	return asymptoteobserve.SanitizeSlice(input, asymptoteobserve.PrivacyOptions{
		RedactSecrets: w.redactSecrets,
		StringLimit:   asymptoteobserve.DefaultRawStringLimit,
	})
}

func (w jsonlWriter) cleanString(value string, limit int) string {
	return asymptoteobserve.CleanString(value, limit, w.redactSecrets)
}

func sanitizeTyped[T any](input *T, sanitize func(map[string]interface{}) map[string]interface{}) *T {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		out := *input
		return &out
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		out := *input
		return &out
	}
	raw = sanitize(raw)
	data, err = json.Marshal(raw)
	if err != nil {
		out := *input
		return &out
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		fallback := *input
		return &fallback
	}
	return &out
}

// The collector usually runs as root, while agent hooks append to the same
// runtime log as the console user, so shared runtime files must stay
// writable by both regardless of which process creates them first.
const runtimeFileMode = 0666

// Keep this rotation contract mirrored with the endpoint CLI and hook writer.
func appendJSONL(path string, line []byte, rotateBytes int64, rotateArchives int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	lock, err := openRuntimeFile(path+".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	defer lock.Close()
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
