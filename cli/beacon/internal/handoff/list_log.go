package handoff

import (
	"errors"
	"fmt"
)

// ListWithLog includes sessions retained only in the runtime log. A store's
// metadata wins, including subagent status, even when a filter hides that entry.
func ListWithLog(sources []Source, logPath string, filter Filter) ([]Session, error) {
	var combined []Source
	seen := map[string]bool{}
	for _, source := range sources {
		if filter.Harness != "" && source.Harness() != filter.Harness {
			continue
		}
		sessions, err := source.List()
		combined = append(combined, listedSource{source.Harness(), sessions, err})
		for _, s := range sessions {
			seen[s.Harness+"\x00"+s.ID] = true
		}
	}
	logged, logErr := LogSessions(logPath, Filter{Harness: filter.Harness})
	byHarness := map[string][]Session{}
	var names []string
	for _, s := range logged {
		if seen[s.Harness+"\x00"+s.ID] {
			continue
		}
		if _, ok := byHarness[s.Harness]; !ok {
			names = append(names, s.Harness)
		}
		byHarness[s.Harness] = append(byHarness[s.Harness], s)
	}
	for _, name := range names {
		combined = append(combined, listedSource{name, byHarness[name], nil})
	}
	sessions, err := List(combined, filter)
	if logErr != nil {
		logErr = fmt.Errorf("runtime log %s: %w", logPath, logErr)
		return sessions, errors.Join(err, logErr)
	}
	return sessions, err
}

type listedSource struct {
	harness  string
	sessions []Session
	err      error
}

func (s listedSource) Harness() string          { return s.harness }
func (s listedSource) List() ([]Session, error) { return s.sessions, s.err }
