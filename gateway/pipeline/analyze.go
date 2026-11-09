package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/asymptote-labs/agent-beacon/gateway/store"
)

// Analyzer inspects a turn and reports findings and records. Analyze runs on the call's path and
// concurrently with other calls, including calls of the same session: it must be fast and keep
// anything that outlives the call in Turn.Store, not in its own memory.
type Analyzer interface {
	Name() string
	Analyze(ctx context.Context, t *Turn) (Report, error)
}

// Background is implemented by analyzers that also work off the call path, such as analysis
// across a session's calls. New starts it with the pipeline's environment; Close stops it before
// the outputs close, and Stop must return only after its last emit.
type Background interface {
	Start(env Env)
	Stop()
}

// Env is what a background analyzer shares with the call path: the record emitter and the same
// store calls read, so state it writes is what the next call sees.
type Env struct {
	Emit  func(...Record)
	Store store.Store
	Log   *slog.Logger
}

// Report is what an analyzer found in one turn.
type Report struct {
	// Findings are new detections: they count toward the verdict and are recorded.
	Findings []Finding
	// Standing are detections recorded before that still hold for this call: something learned
	// about the session on an earlier call, or resent content that matched when it was first
	// reported. They count toward the verdict and are not recorded again.
	Standing []Finding
	// Records go to the sinks routed for their kind.
	Records []Record
}

// Finding is one detection.
type Finding struct {
	Analyzer string   `json:"analyzer"`
	Rule     string   `json:"rule"`
	Title    string   `json:"title,omitempty"`
	Severity Severity `json:"severity"`
	Reason   string   `json:"reason"`
	Session  string   `json:"session,omitempty"`
	Gateway  string   `json:"gateway"`
	CallID   string   `json:"call_id"`
	Phase    string   `json:"phase"`
	// Evidence identifies what matched, in the analyzer's own terms (Beacon: event ids).
	Evidence []string `json:"evidence,omitempty"`
}

// Severity orders findings.
type Severity uint8

// Severities, lowest first.
const (
	SeverityInfo Severity = iota
	SeverityLow
	SeverityMedium
	SeverityHigh
	SeverityCritical
)

var severityNames = [...]string{"info", "low", "medium", "high", "critical"}

func (s Severity) String() string {
	if int(s) < len(severityNames) {
		return severityNames[s]
	}
	return fmt.Sprintf("severity(%d)", s)
}

// ParseSeverity parses a severity name.
func ParseSeverity(name string) (Severity, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	for i, n := range severityNames {
		if n == name {
			return Severity(i), nil
		}
	}
	return 0, fmt.Errorf("invalid severity %q (info|low|medium|high|critical)", name)
}

// MarshalText renders the severity's name.
func (s Severity) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText parses a severity name.
func (s *Severity) UnmarshalText(b []byte) error {
	v, err := ParseSeverity(string(b))
	if err != nil {
		return err
	}
	*s = v
	return nil
}
