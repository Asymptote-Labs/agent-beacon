package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const (
	defaultEndpointRotateBytes    = 10 * 1024 * 1024
	defaultEndpointRotateArchives = 5
)

// Logger provides structured JSON logging for hook events
type Logger struct {
	hookName  string
	logFile   string
	sessionID string
	platform  string
}

// NewLoggerForPlatform creates a logger for a specific hook and platform
func NewLoggerForPlatform(hookName, platform string) *Logger {
	config.EnsureStateDir(platform)
	return &Logger{hookName: hookName, logFile: config.GetLogFile(platform), platform: platform}
}

// NewSessionLogger creates a logger that writes to a per-session log file.
// Log file path: ~/.beacon/{platform}/logs/{session_id}.log
func NewSessionLogger(hookName, platform, sessionID string) *Logger {
	config.EnsureSessionLogDir(platform)
	return &Logger{
		hookName:  hookName,
		logFile:   config.GetSessionLogFile(platform, sessionID),
		sessionID: sessionID,
		platform:  platform,
	}
}

func (l *Logger) formatEntry(level, message string, fields ...interface{}) map[string]interface{} {
	entry := map[string]interface{}{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"hook":      l.hookName,
		"level":     level,
		"message":   message,
	}

	if l.sessionID != "" {
		entry["session_id"] = l.sessionID
	}

	// Add additional fields
	for i := 0; i < len(fields)-1; i += 2 {
		if key, ok := fields[i].(string); ok {
			entry[key] = fields[i+1]
		}
	}

	return entry
}

func (l *Logger) write(entry map[string]interface{}) {
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	f, err := os.OpenFile(l.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "logging: failed to close log file %s: %v\n", l.logFile, cerr)
		}
	}()

	if _, err := f.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "logging: failed to write log entry to %s: %v\n", l.logFile, err)
		return
	}
	if _, err := f.WriteString("\n"); err != nil {
		fmt.Fprintf(os.Stderr, "logging: failed to write newline to %s: %v\n", l.logFile, err)
		return
	}
}

func (l *Logger) EndpointEvent(action, category, severity, message string, fields map[string]interface{}) error {
	path := endpointLogPath()
	if path == "" {
		return nil
	}
	if severity == "" {
		severity = "info"
	}
	event := l.baseEndpointEvent(action, category, severity, message)
	for key, value := range fields {
		if value == nil {
			continue
		}
		event[key] = value
	}
	if err := writeEndpointJSON(path, event); err != nil {
		fmt.Fprintf(os.Stderr, "logging: failed to write endpoint event to %s: %v\n", path, err)
		return err
	}
	return nil
}

func (l *Logger) baseEndpointEvent(action, category, severity, message string) map[string]interface{} {
	hostname, _ := os.Hostname()
	user := map[string]interface{}{
		"name": os.Getenv("USER"),
	}
	if uid := firstEnv("BEACON_CLOUD_USER_ID_HASH", "BEACON_CLOUD_USER_ID"); uid != "" {
		user["uid"] = uid
	}
	event := map[string]interface{}{
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"vendor":         "beacon",
		"product":        "endpoint-agent",
		"schema_version": "1.0",
		"event": map[string]interface{}{
			"kind":     "agent_runtime",
			"action":   action,
			"category": category,
		},
		"severity": severity,
		"endpoint": map[string]interface{}{
			"hostname": hostname,
			"os":       runtime.GOOS,
		},
		"user": user,
		"harness": map[string]interface{}{
			"name": l.platform,
		},
		"message": asymptoteobserve.CleanString(message, asymptoteobserve.DefaultStringLimit, true),
	}
	if origin := firstEnv("BEACON_ORIGIN"); origin != "" {
		event["origin"] = origin
	}
	if run := cloudRunFields(); len(run) > 0 {
		event["run"] = run
	}
	if l.sessionID != "" {
		event["session"] = map[string]interface{}{"id": l.sessionID}
	}
	return event
}

func cloudRunFields() map[string]interface{} {
	run := map[string]interface{}{}
	if provider := firstEnv("BEACON_RUN_PROVIDER"); provider != "" {
		run["provider"] = provider
	}
	if runID := firstEnv("BEACON_RUN_ID", "CLAUDE_CODE_REMOTE_SESSION_ID"); runID != "" {
		run["run_id"] = runID
	}
	if attempt := firstEnv("BEACON_RUN_ATTEMPT"); attempt != "" {
		run["run_attempt"] = attempt
	}
	if workflow := firstEnv("BEACON_RUN_WORKFLOW"); workflow != "" {
		run["workflow"] = workflow
	}
	if job := firstEnv("BEACON_RUN_JOB"); job != "" {
		run["job"] = job
	}
	if eventName := firstEnv("BEACON_RUN_EVENT_NAME"); eventName != "" {
		run["event_name"] = eventName
	}
	if commit := firstEnv("BEACON_RUN_COMMIT"); commit != "" {
		run["commit"] = commit
	}
	if repository := firstEnv("BEACON_RUN_REPOSITORY"); repository != "" {
		run["repository"] = repository
	}
	if branch := firstEnv("BEACON_RUN_BRANCH"); branch != "" {
		run["branch"] = branch
	}
	if pr := firstEnv("BEACON_RUN_PR"); pr != "" {
		run["pr"] = pr
	}
	if prNumber := firstEnv("BEACON_RUN_PR_NUMBER"); prNumber != "" {
		run["pr_number"] = prNumber
	}
	if actor := firstEnv("BEACON_RUN_ACTOR"); actor != "" {
		run["actor"] = actor
	}
	if ephemeral := firstEnv("BEACON_RUN_EPHEMERAL"); ephemeral != "" {
		if value, err := strconv.ParseBool(ephemeral); err == nil {
			run["ephemeral"] = value
		}
	}
	return run
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func writeEndpointJSON(path string, event map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(sanitizeEndpointMap(event))
	if err != nil {
		return err
	}
	if len(data) > 64*1024 {
		event["field_truncated"] = true
		event["raw"] = nil
		event["message"] = truncateEndpoint(fmt.Sprint(event["message"]), 1024)
		data, err = json.Marshal(sanitizeEndpointMap(event))
		if err != nil {
			return err
		}
	}
	return appendEndpointJSONL(path, append(data, '\n'), defaultEndpointRotateBytes, defaultEndpointRotateArchives)
}

func endpointLogPath() string {
	if path := firstEnv("BEACON_ENDPOINT_LOG", "BEACON_CLOUD_LOG_PATH", "BEACON_LOG_PATH", "BEACON_RUNTIME_LOG"); path != "" {
		return path
	}
	if os.Getenv("BEACON_ENDPOINT_MODE") == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
}

func redactEndpointString(value string) string {
	return asymptoteobserve.RedactString(value)
}

func sanitizeEndpointMap(input map[string]interface{}) map[string]interface{} {
	return asymptoteobserve.SanitizeMap(input, asymptoteobserve.PrivacyOptions{
		RedactSecrets: true,
		StringLimit:   asymptoteobserve.DefaultStringLimit,
	})
}

func sanitizeEndpointSlice(input []interface{}) []interface{} {
	return asymptoteobserve.SanitizeSlice(input, asymptoteobserve.PrivacyOptions{
		RedactSecrets: true,
		StringLimit:   asymptoteobserve.DefaultStringLimit,
	})
}

func truncateEndpoint(value string, limit int) string {
	return asymptoteobserve.TruncateString(value, limit)
}

// Keep this rotation contract mirrored with the endpoint CLI and beaconjson exporter.
func appendEndpointJSONL(path string, line []byte, rotateBytes int64, rotateArchives int) error {
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
	if asymptoteobserve.IsDuplicateEndpointEvent(path, line, asymptoteobserve.EndpointDuplicateWindow) {
		return nil
	}
	if err := rotateEndpointLogIfNeeded(path, rotateBytes, rotateArchives, int64(len(line))); err != nil {
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

func rotateEndpointLogIfNeeded(path string, maxSize int64, archives int, nextWriteBytes int64) error {
	if maxSize <= 0 {
		return nil
	}
	if archives < 1 {
		archives = defaultEndpointRotateArchives
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
	if err := removeOverflowEndpointArchives(path, archives); err != nil {
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

func removeOverflowEndpointArchives(path string, archives int) error {
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

// Debug logs a debug message
func (l *Logger) Debug(message string, fields ...interface{}) {
	entry := l.formatEntry("debug", message, fields...)
	l.write(entry)
}

// Info logs an info message
func (l *Logger) Info(message string, fields ...interface{}) {
	entry := l.formatEntry("info", message, fields...)
	l.write(entry)
}

// Warn logs a warning message
func (l *Logger) Warn(message string, fields ...interface{}) {
	entry := l.formatEntry("warn", message, fields...)
	l.write(entry)
}

// Error logs an error message and also writes to stderr
func (l *Logger) Error(message string, fields ...interface{}) {
	entry := l.formatEntry("error", message, fields...)
	l.write(entry)
	fmt.Fprintf(os.Stderr, "[%s] ERROR: %s\n", l.hookName, message)
}
