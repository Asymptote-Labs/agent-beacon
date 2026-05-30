package beaconjsonexporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptotetrace"
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
		event.Message = asymptotetrace.TruncateString(event.Message, 1024)
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
	event.Message = w.cleanString(event.Message, asymptotetrace.DefaultStringLimit)
	if event.Tool != nil {
		event.Tool.Command = w.cleanString(event.Tool.Command, asymptotetrace.DefaultStringLimit)
		event.Tool.Path = asymptotetrace.TruncateString(event.Tool.Path, asymptotetrace.DefaultRawStringLimit)
	}
	if event.Command != nil {
		event.Command.Command = w.cleanString(event.Command.Command, asymptotetrace.DefaultStringLimit)
	}
	if event.Approval != nil {
		event.Approval.Reason = w.cleanString(event.Approval.Reason, asymptotetrace.DefaultStringLimit)
	}
	if event.Policy != nil {
		event.Policy.Reason = w.cleanString(event.Policy.Reason, asymptotetrace.DefaultStringLimit)
	}
	if event.Prompt != nil {
		event.Prompt.Text = w.cleanString(event.Prompt.Text, asymptotetrace.DefaultStringLimit)
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
	return asymptotetrace.SanitizeMap(input, asymptotetrace.PrivacyOptions{
		RedactSecrets: w.redactSecrets,
		StringLimit:   asymptotetrace.DefaultRawStringLimit,
	})
}

func (w jsonlWriter) sanitizeSlice(input []interface{}) []interface{} {
	return asymptotetrace.SanitizeSlice(input, asymptotetrace.PrivacyOptions{
		RedactSecrets: w.redactSecrets,
		StringLimit:   asymptotetrace.DefaultRawStringLimit,
	})
}

func (w jsonlWriter) cleanString(value string, limit int) string {
	return asymptotetrace.CleanString(value, limit, w.redactSecrets)
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
