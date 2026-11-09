package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/store"
)

// Analyzer inspects a turn and reports findings and records. Analyze runs concurrently with
// other calls, including calls of the same session: it must be fast and keep anything that
// outlives the call in Turn.Store, not in its own memory. Analyzers run in order, one at a time
// per call; the events analyzer runs first and leaves the call's events on the Turn.
type Analyzer interface {
	Name() string
	Analyze(ctx context.Context, t *Turn) (Report, error)
}

// Inline is implemented by an analyzer whose findings must be known before a call is answered.
// When no analyzer is inline and the policy is the default, nothing analysis finds can change a
// verdict, so the pipeline answers each call as soon as it is admitted and analyzes it after.
type Inline interface {
	Inline() bool
}

// ResentReader is implemented by an analyzer that judges content a session already reported when
// a request sends it again (Turn.Resent). Without one, resent content is not listed at all.
type ResentReader interface {
	ReadsResent() bool
}

// Observer is called once per analyzed call, after its verdict, with everything the pipeline
// knows of it, and returns records of its own for the call's batch. It runs on the analysis path
// and must be fast.
type Observer interface {
	Observe(o *Observation) []Record
}

// Observation is one analyzed call, as an Observer sees it.
type Observation struct {
	Turn *Turn
	// Findings are new; Standing held over from earlier calls. Both counted toward Verdict.
	Findings, Standing []Finding
	Verdict            gateway.Verdict
	// Errors are why analysis did not complete, if it did not.
	Errors []error
	// Elapsed is the time from the start of analysis to the verdict.
	Elapsed time.Duration
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
