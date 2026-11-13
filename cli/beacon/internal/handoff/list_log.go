package handoff

import (
	"errors"
	"fmt"
	"strings"
)

// LogReadError identifies a runtime-log read failure separately from store errors.
type LogReadError struct {
	Path string
	Err  error
}

func (e *LogReadError) Error() string { return fmt.Sprintf("runtime log %s: %v", e.Path, e.Err) }
func (e *LogReadError) Unwrap() error { return e.Err }

// ListWithLog merges runtime stores with log-only sessions before filtering and
// sorting. Store metadata wins even when a filter hides the stored entry.
func ListWithLog(sources []Source, logPath string, filter Filter) ([]Session, error) {
	stored, storeErr := List(sources, Filter{Harness: filter.Harness, IncludeSubagents: true})
	seen := map[string]bool{}
	for _, session := range stored {
		seen[session.Harness+"\x00"+session.ID] = true
	}
	logged, logErr := LogSessions(logPath, Filter{Harness: filter.Harness, IncludeSubagents: true})
	for _, session := range logged {
		key := session.Harness + "\x00" + session.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		session.FromLog = true
		// With no prompt or parent link, log-only child runtimes cannot otherwise
		// be distinguished. Keep promptless sessions behind the subagents opt-in.
		session.Subagent = strings.TrimSpace(session.Title) == ""
		stored = append(stored, session)
	}
	if logErr != nil {
		logErr = &LogReadError{Path: logPath, Err: logErr}
	}
	return filterSessions(stored, filter), errors.Join(storeErr, logErr)
}
