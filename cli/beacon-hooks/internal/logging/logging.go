package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
)

var endpointSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)authorization\s*[:=]\s*bearer\s+[^"',\s]+`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|authorization)\s*[:=]\s*["']?[^"',\s]+`),
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),
}

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
	l.writeEndpointEvent(entry)
}

func (l *Logger) writeEndpointEvent(entry map[string]interface{}) {
	path := endpointLogPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	hostname, _ := os.Hostname()
	action, severity := endpointAction(l.hookName, entry)
	event := map[string]interface{}{
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"vendor":         "beacon",
		"product":        "endpoint-agent",
		"schema_version": "1.0",
		"event": map[string]interface{}{
			"kind":     "agent_runtime",
			"action":   action,
			"category": "hook",
		},
		"severity": severity,
		"endpoint": map[string]interface{}{
			"hostname": hostname,
			"os":       runtime.GOOS,
		},
		"user": map[string]interface{}{
			"name": os.Getenv("USER"),
		},
		"harness": map[string]interface{}{
			"name": l.platform,
		},
		"message": redactEndpointString(truncateEndpoint(fmt.Sprint(entry["message"]), 4096)),
	}
	if l.sessionID != "" {
		event["session"] = map[string]interface{}{"id": l.sessionID}
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	if len(data) > 64*1024 {
		event["field_truncated"] = true
		event["message"] = truncateEndpoint(fmt.Sprint(event["message"]), 1024)
		data, err = json.Marshal(event)
		if err != nil {
			return
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

func (l *Logger) EndpointEvent(action, category, severity, message string, fields map[string]interface{}) {
	path := endpointLogPath()
	if path == "" {
		return
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
	retention := string(config.ContentRetentionMode())
	if retention == "" {
		retention = "metadata"
	}
	event["content"] = map[string]interface{}{
		"retention": retention,
		"included":  retention != "metadata",
		"redacted":  retention == "redacted",
	}
	if raw, ok := event["raw"].(map[string]interface{}); ok {
		event["raw"] = retentionAwareRaw(raw, retention)
	}
	writeEndpointJSON(path, event)
}

func (l *Logger) baseEndpointEvent(action, category, severity, message string) map[string]interface{} {
	hostname, _ := os.Hostname()
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
		"user": map[string]interface{}{
			"name": os.Getenv("USER"),
		},
		"harness": map[string]interface{}{
			"name": l.platform,
		},
		"message": redactEndpointString(truncateEndpoint(message, 4096)),
	}
	if l.sessionID != "" {
		event["session"] = map[string]interface{}{"id": l.sessionID}
	}
	return event
}

func writeEndpointJSON(path string, event map[string]interface{}) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	data, err := json.Marshal(sanitizeEndpointMap(event))
	if err != nil {
		return
	}
	if len(data) > 64*1024 {
		event["field_truncated"] = true
		event["raw"] = nil
		event["message"] = truncateEndpoint(fmt.Sprint(event["message"]), 1024)
		data, err = json.Marshal(sanitizeEndpointMap(event))
		if err != nil {
			return
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

func endpointAction(hookName string, entry map[string]interface{}) (action, severity string) {
	message := strings.ToLower(fmt.Sprint(entry["message"]))
	switch hookName {
	case "session-start":
		return "session.started", "info"
	case "session-end":
		return "session.ended", "info"
	case "pre-tool":
		if strings.Contains(message, "denying") || strings.Contains(message, "block") {
			return "policy.warned", "medium"
		}
		return "tool.invoked", "info"
	case "stop-async-handler":
		if strings.Contains(message, "blocking with violations") {
			return "policy.blocked", "high"
		}
		if strings.Contains(message, "warning") {
			return "policy.warned", "medium"
		}
		return "session.ended", "info"
	default:
		return "tool.invoked", "info"
	}
}

func endpointLogPath() string {
	if path := os.Getenv("BEACON_ENDPOINT_LOG"); path != "" {
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
	for _, pattern := range endpointSecretPatterns {
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

func retentionAwareRaw(raw map[string]interface{}, retention string) map[string]interface{} {
	if retention != "metadata" {
		return raw
	}
	return map[string]interface{}{"field_count": len(raw)}
}

func sanitizeEndpointMap(input map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case string:
			out[key] = redactEndpointString(truncateEndpoint(typed, 4096))
		case map[string]interface{}:
			out[key] = sanitizeEndpointMap(typed)
		case []interface{}:
			out[key] = sanitizeEndpointSlice(typed)
		default:
			out[key] = typed
		}
	}
	return out
}

func sanitizeEndpointSlice(input []interface{}) []interface{} {
	out := make([]interface{}, len(input))
	for i, value := range input {
		switch typed := value.(type) {
		case string:
			out[i] = redactEndpointString(truncateEndpoint(typed, 4096))
		case map[string]interface{}:
			out[i] = sanitizeEndpointMap(typed)
		case []interface{}:
			out[i] = sanitizeEndpointSlice(typed)
		default:
			out[i] = typed
		}
	}
	return out
}

func truncateEndpoint(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit < 32 {
		return value[:limit]
	}
	return value[:limit-15] + "...[truncated]"
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
