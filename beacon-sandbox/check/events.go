// Package check turns collected artifacts into a verdict.
//
// Deliberately a pure function of what is on disk: no sandbox, no model, no network. That
// makes it instant and free to re-run while iterating on what counts as correct, and it
// makes a verdict reproducible even though the session that produced it was not.
package check

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	obs "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Event is one parsed JSONL line, kept alongside its raw form and line number so findings
// can point at exact evidence.
type Event struct {
	Line int
	Raw  string
	// Decoded is the generic form, used for dotted-path field lookups.
	Decoded map[string]any
	// Typed is the shipped struct, so we validate against the real schema contract
	// rather than a copy of it that can drift.
	Typed obs.Event
	// ParseErr is set when the line is not valid JSON.
	ParseErr error
}

// Action returns event.action, or "" when absent.
func (e Event) Action() string {
	return e.Typed.Event.Action
}

// Field resolves a dotted path such as "command.command" or "gen_ai.usage.input_tokens",
// reporting whether it is present and non-empty. Absent sub-objects are not an error --
// they are exactly what we are testing for.
func (e Event) Field(path string) (any, bool) {
	cur := any(e.Decoded)
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	switch v := cur.(type) {
	case nil:
		return nil, false
	case string:
		return v, strings.TrimSpace(v) != ""
	case map[string]any:
		return v, len(v) > 0
	case []any:
		return v, len(v) > 0
	default:
		return cur, true
	}
}

// Log is a parsed runtime JSONL file.
type Log struct {
	Path   string
	Events []Event
}

// ReadLog parses a Beacon runtime JSONL file strictly.
//
// Deliberately does not go through dashboard.StreamEvents, which silently skips malformed
// lines (internal/endpoint/dashboard/events.go) -- a corrupt writer would look clean. Bad
// lines are retained here as events carrying ParseErr so the invariant layer can fail on
// them.
func ReadLog(path string) (Log, error) {
	f, err := os.Open(path)
	if err != nil {
		return Log{Path: path}, err
	}
	defer f.Close()

	log := Log{Path: path}
	sc := bufio.NewScanner(f)
	// Events are capped at 64 KiB by the writer, but a corrupt line could be longer and
	// we want to see it rather than error out mid-scan.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for n := 1; sc.Scan(); n++ {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		ev := Event{Line: n, Raw: raw}
		if err := json.Unmarshal([]byte(raw), &ev.Decoded); err != nil {
			ev.ParseErr = err
			log.Events = append(log.Events, ev)
			continue
		}
		if err := json.Unmarshal([]byte(raw), &ev.Typed); err != nil {
			ev.ParseErr = fmt.Errorf("decode into schema struct: %w", err)
		}
		log.Events = append(log.Events, ev)
	}
	if err := sc.Err(); err != nil {
		return log, fmt.Errorf("scan %s: %w", path, err)
	}
	return log, nil
}

// ActionHistogram summarizes what was captured, for the report.
func (l Log) ActionHistogram() map[string]int {
	h := map[string]int{}
	for _, e := range l.Events {
		if e.ParseErr != nil {
			continue
		}
		h[e.Action()]++
	}
	return h
}

// HarnessNames reports the distinct harness.name values seen. More than one for a single
// runtime is a known inconsistency between the OTLP and hook capture paths.
func (l Log) HarnessNames() map[string]int {
	h := map[string]int{}
	for _, e := range l.Events {
		if e.ParseErr != nil {
			continue
		}
		h[e.Typed.Harness.Name]++
	}
	return h
}
