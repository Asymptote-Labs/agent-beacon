// Package threatrules implements the Beacon "Threat Rules" detection-rule standard: a
// YAML rule format whose match conditions are CEL expressions over the Beacon endpoint
// event, plus a reference evaluator and an embedded-fixture conformance harness.
//
// This package is the open reference implementation of the spec under
// spec/threat-rules. A separate (possibly closed) engine conforms if it produces the
// same Verdict each rule's fixtures declare.
package threatrules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// idPattern constrains rule ids to a stable kebab slug.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidID reports whether id is a well-formed rule id. The pattern admits no
// path separators or "." segments, so a valid id is always a safe single
// filename component when joined with the store directory.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Status is a rule's maturity level; it drives the conformance gates (see lint.go).
type Status string

const (
	StatusExperimental Status = "experimental"
	StatusStable       Status = "stable"
	StatusDeprecated   Status = "deprecated"
)

// Posture declares whether a rule is observe-only or eligible for enforcement. It maps
// onto PolicyInfo.Enforcement; only "detect" is acted on in this phase.
type Posture string

const (
	PostureDetect         Posture = "detect"
	PostureEnforceCapable Posture = "enforce-capable"
)

// Scope is the correlation grouping key. v1 supports session only.
type Scope string

const ScopeSession Scope = "session"

// Rule is a single threat-detection rule. Exactly one of Match or Correlation is set.
type Rule struct {
	ID          string                    `yaml:"id"`
	Version     int                       `yaml:"version"`
	Title       string                    `yaml:"title"`
	Description string                    `yaml:"description,omitempty"`
	Severity    asymptoteobserve.Severity `yaml:"severity"`
	Status      Status                    `yaml:"status"`
	Posture     Posture                   `yaml:"posture"`
	Taxonomy    map[string]string         `yaml:"taxonomy,omitempty"`
	Match       string                    `yaml:"match,omitempty"`
	Correlation *Correlation              `yaml:"correlation,omitempty"`
	Emit        Emit                      `yaml:"emit"`
	Tests       []Fixture                 `yaml:"tests"`
}

// Correlation is the ordered-window, session-scoped form of a rule.
type Correlation struct {
	Scope  Scope             `yaml:"scope"`
	Window string            `yaml:"window"`
	Steps  []CorrelationStep `yaml:"steps"`
}

// ParseWindow parses the raw Window duration. Valid to call after Validate succeeds.
func (c *Correlation) ParseWindow() (time.Duration, error) {
	return time.ParseDuration(c.Window)
}

// CorrelationStep is one ordered step of a correlation rule.
type CorrelationStep struct {
	ID    string `yaml:"id"`
	Match string `yaml:"match"`
}

// Emit describes the finding produced when a rule matches. The rest of a PolicyInfo is
// derived from the rule (id, title, posture); only the human-readable reason is authored.
type Emit struct {
	Reason string `yaml:"reason"`
}

// Fixture is an embedded conformance test case: feed Events through an evaluator and the
// produced verdict must equal Verdict.
type Fixture struct {
	Name    string         `yaml:"name"`
	Verdict Verdict        `yaml:"verdict"`
	Events  []FixtureEvent `yaml:"events"`
}

// EventList returns the fixture's events as plain Beacon events for evaluation.
func (f Fixture) EventList() []asymptoteobserve.Event {
	events := make([]asymptoteobserve.Event, len(f.Events))
	for i := range f.Events {
		events[i] = f.Events[i].Event
	}
	return events
}

// FixtureEvent wraps a Beacon Event so fixture YAML can use the event's JSON field names
// (e.g. event.action, command.command, gen_ai.usage.input_tokens) — the same names a
// rule's CEL expression references. yaml.v3 keys off Go field names, not json tags, so we
// decode each event through a YAML->generic->JSON bridge to honor the json tags.
type FixtureEvent struct {
	Event asymptoteobserve.Event
}

// UnmarshalYAML decodes one fixture event via the YAML->JSON bridge described above.
func (fe *FixtureEvent) UnmarshalYAML(node *yaml.Node) error {
	var raw map[string]any
	if err := node.Decode(&raw); err != nil {
		return err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &fe.Event)
}

func validSeverity(s asymptoteobserve.Severity) bool {
	switch s {
	case asymptoteobserve.SeverityInfo, asymptoteobserve.SeverityLow, asymptoteobserve.SeverityMedium,
		asymptoteobserve.SeverityHigh, asymptoteobserve.SeverityCritical:
		return true
	}
	return false
}

// Validate checks a rule against the spec: structural constraints plus every CEL match
// expression compiling to bool against the event schema. A nil return means the rule is
// loadable; it does not by itself assert maturity-ladder gates (see CheckMaturity).
func (r *Rule) Validate() error {
	if !idPattern.MatchString(r.ID) {
		return fmt.Errorf("id %q must match %s", r.ID, idPattern.String())
	}
	if r.Version < 1 {
		return fmt.Errorf("version must be >= 1, got %d", r.Version)
	}
	if r.Title == "" {
		return fmt.Errorf("title is required")
	}
	if !validSeverity(r.Severity) {
		return fmt.Errorf("invalid severity %q", r.Severity)
	}
	switch r.Status {
	case StatusExperimental, StatusStable, StatusDeprecated:
	default:
		return fmt.Errorf("invalid status %q", r.Status)
	}
	switch r.Posture {
	case PostureDetect, PostureEnforceCapable:
	default:
		return fmt.Errorf("invalid posture %q", r.Posture)
	}
	if r.Emit.Reason == "" {
		return fmt.Errorf("emit.reason is required")
	}
	if len(r.Tests) == 0 {
		return fmt.Errorf("at least one test fixture is required")
	}

	hasMatch := r.Match != ""
	hasCorrelation := r.Correlation != nil
	switch {
	case hasMatch && hasCorrelation:
		return fmt.Errorf("exactly one of match or correlation may be set, both are present")
	case !hasMatch && !hasCorrelation:
		return fmt.Errorf("exactly one of match or correlation must be set, neither is present")
	}

	if err := r.validateExpressions(); err != nil {
		return err
	}
	if hasCorrelation {
		if err := r.validateCorrelation(); err != nil {
			return err
		}
	}
	if err := r.validateFixtures(); err != nil {
		return err
	}
	return nil
}

// validateExpressions compiles every CEL match in the rule, surfacing unknown-field and
// non-bool errors at load time.
func (r *Rule) validateExpressions() error {
	if r.Match != "" {
		if _, err := CompileMatch(r.Match); err != nil {
			return fmt.Errorf("match: %w", err)
		}
	}
	if r.Correlation != nil {
		for i, step := range r.Correlation.Steps {
			if _, err := CompileMatch(step.Match); err != nil {
				return fmt.Errorf("correlation.steps[%d] (%s): %w", i, step.ID, err)
			}
		}
	}
	return nil
}

func (r *Rule) validateCorrelation() error {
	c := r.Correlation
	if c.Scope != ScopeSession {
		return fmt.Errorf("correlation.scope %q unsupported (only %q)", c.Scope, ScopeSession)
	}
	if _, err := c.ParseWindow(); err != nil {
		return fmt.Errorf("correlation.window %q: %w", c.Window, err)
	}
	if len(c.Steps) < 2 {
		return fmt.Errorf("correlation requires >= 2 steps, got %d", len(c.Steps))
	}
	for i, step := range c.Steps {
		if step.ID == "" {
			return fmt.Errorf("correlation.steps[%d] missing id", i)
		}
		if step.Match == "" {
			return fmt.Errorf("correlation.steps[%d] (%s) missing match", i, step.ID)
		}
	}
	return nil
}

func (r *Rule) validateFixtures() error {
	for i, f := range r.Tests {
		if f.Name == "" {
			return fmt.Errorf("tests[%d] missing name", i)
		}
		if !f.Verdict.valid() {
			return fmt.Errorf("tests[%d] (%s) invalid verdict %q", i, f.Name, f.Verdict)
		}
		if len(f.Events) == 0 {
			return fmt.Errorf("tests[%d] (%s) requires at least one event", i, f.Name)
		}
	}
	return nil
}
