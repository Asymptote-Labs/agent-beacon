package check

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/hostguard"
	obs "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// MaxEventBytes mirrors the writer's cap (cli/beacon/internal/endpoint/writer/writer.go).
// Anything larger means the size-control path failed.
const MaxEventBytes = 64 * 1024

// KnownActions is the set of event.action values the shipping code emits.
//
// There are no Go constants for actions anywhere in the product -- every one is an inline
// string literal across three modules -- so a typo yields a silently unmatched event and a
// silently dead threat rule. This list makes the harness the safety net for that, and an
// unrecognized action is reported rather than ignored.
var KnownActions = map[string]bool{
	// agent runtime activity
	"session.started": true, "session.ended": true, "session.activity": true,
	"session.compacting": true, "session.event": true, "session.created": true,
	"session.deleted": true, "session.idle": true, "session.error": true,
	"session.compacted": true,
	"prompt.submitted":  true,
	"tool.invoked":      true, "tool.completed": true, "tool.failed": true,
	"command.executed": true, "command.invoked": true,
	"file.read": true, "file.modified": true, "file.created": true,
	"mcp.tool_invoked":   true,
	"approval.requested": true, "approval.allowed": true, "approval.denied": true,
	"policy.blocked":   true,
	"subagent.started": true, "subagent.stopped": true,
	"agent.reasoning": true, "agent.response": true, "model.retry": true,
	"token.usage": true, "cost.usage": true, "metric.observed": true,
	"trace.unclassified": true,
	// endpoint management
	"agent.detected": true, "telemetry.enabled": true, "telemetry.disabled": true,
	"telemetry.misconfigured": true, "inventory.heartbeat": true, "inventory.snapshot": true,
	"installation.updated": true, "endpoint.health_failed": true,
	"endpoint.tamper_detected": true, "endpoint.validation": true,
}

// Secrets holds values that must never appear in a collected artifact.
type Secrets struct {
	// Values are matched literally. Empty strings are ignored -- a naive substring check
	// against "" matches every line, which produced a false "leaked" verdict during M0.
	Values map[string]string
	// Withheld names credentials whose value is deliberately not available locally, mapped to
	// the reason. Using a provider-stored secret is the most secure option precisely because
	// the value never enters this process, but that also means there is nothing to search the
	// artifacts for. Recording the reason keeps the resulting warning honest: the check did
	// not pass, it could not run, and that is a consequence of the chosen credential path
	// rather than a defect.
	Withheld map[string]string
}

// Invariants checks properties that hold regardless of what the agent chose to do, so they
// need no scenario knowledge and no ground truth.
func Invariants(v *Verdict, log Log, secrets Secrets) {
	v.EventCount = len(log.Events)
	v.Histogram = log.ActionHistogram()
	v.Harnesses = log.HarnessNames()

	checkParseable(v, log)
	checkSchema(v, log)
	checkSizes(v, log)
	checkTimestamps(v, log)
	checkOrdering(v, log)
	checkActions(v, log)
	checkSecrets(v, log, secrets)
}

// checkParseable fails on any line that is not valid JSON. This is why the reader does not
// use dashboard.StreamEvents, which would silently drop such lines.
func checkParseable(v *Verdict, log Log) {
	var ev []string
	for _, e := range log.Events {
		if e.ParseErr != nil {
			ev = append(ev, fmt.Sprintf("%s:%d (%v)", log.Path, e.Line, e.ParseErr))
		}
	}
	if len(ev) > 0 {
		v.Add(Finding{
			Check: "invariant.jsonl_parseable", Severity: SevFail,
			Summary:  fmt.Sprintf("%d of %d lines are not valid JSON", len(ev), len(log.Events)),
			Why:      "a malformed line means the writer emitted corrupt output; the dashboard would hide this by skipping it",
			Evidence: cap5(ev),
		})
	}
}

// checkSchema runs the shipped validator, so the harness tracks the real release contract
// instead of a copy that can drift.
func checkSchema(v *Verdict, log Log) {
	var ev []string
	for _, e := range log.Events {
		if e.ParseErr != nil {
			continue
		}
		if err := e.Typed.Validate(); err != nil {
			ev = append(ev, fmt.Sprintf("%s:%d (%v)", log.Path, e.Line, err))
		}
	}
	if len(ev) > 0 {
		v.Add(Finding{
			Check: "invariant.schema_valid", Severity: SevFail,
			Summary:  fmt.Sprintf("%d event(s) fail asymptoteobserve.Event.Validate()", len(ev)),
			Why:      "vendor/product/schema_version and the required event fields are a release contract",
			Evidence: cap5(ev),
		})
	}
}

func checkSizes(v *Verdict, log Log) {
	var ev []string
	for _, e := range log.Events {
		if len(e.Raw) > MaxEventBytes {
			ev = append(ev, fmt.Sprintf("%s:%d (%d bytes)", log.Path, e.Line, len(e.Raw)))
		}
	}
	if len(ev) > 0 {
		v.Add(Finding{
			Check: "invariant.event_size", Severity: SevFail,
			Summary:  fmt.Sprintf("%d event(s) exceed the %d byte cap", len(ev), MaxEventBytes),
			Why:      "oversized events indicate the truncation and size-control path did not run",
			Evidence: cap5(ev),
		})
	}
}

// checkTimestamps requires parseable RFC3339 timestamps, and reports large regressions
// within a session as a warning only.
//
// Not a hard failure: the collector batches, and the exporter stamps metric events from the
// datapoint time while log events use their own, so modest out-of-order arrival is normal
// rather than a defect.
func checkTimestamps(v *Verdict, log Log) {
	var unparseable []string
	latestBySession := map[string]time.Time{}
	var regressions []string

	for _, e := range log.Events {
		if e.ParseErr != nil {
			continue
		}
		ts, err := obs.ParseTimestamp(e.Typed.Timestamp)
		if err != nil {
			unparseable = append(unparseable, fmt.Sprintf("%s:%d (%q)", log.Path, e.Line, e.Typed.Timestamp))
			continue
		}
		sess := ""
		if e.Typed.Session != nil {
			sess = e.Typed.Session.ID
		}
		// Fetched once and branched on explicitly. The previous form read prev inside an
		// `else if` attached to the same `if prev, ok := ...` initializer, which is valid Go but
		// relies on prev being the zero time when ok is false -- subtle enough that a reviewer
		// read it as a compile error. Same behavior, stated outright.
		prev, seen := latestBySession[sess]
		switch {
		case seen && ts.Before(prev.Add(-5*time.Second)):
			regressions = append(regressions, fmt.Sprintf("%s:%d (%s after %s)",
				log.Path, e.Line, ts.Format(time.RFC3339), prev.Format(time.RFC3339)))
		case !seen || ts.After(prev):
			latestBySession[sess] = ts
		}
	}

	if len(unparseable) > 0 {
		v.Add(Finding{
			Check: "invariant.timestamp_parseable", Severity: SevFail,
			Summary:  fmt.Sprintf("%d event(s) have a non-RFC3339 timestamp", len(unparseable)),
			Evidence: cap5(unparseable),
		})
	}
	if len(regressions) > 0 {
		v.Add(Finding{
			Check: "invariant.timestamp_order", Severity: SevWarn,
			Summary:  fmt.Sprintf("%d event(s) regress more than 5s within a session", len(regressions)),
			Why:      "expected to some degree given batching; a large or growing count suggests a stamping bug",
			Evidence: cap5(regressions),
		})
	}
}

// checkOrdering requires the log to be orderable: sub-second timestamps, and a sequence
// on the events that share one anyway.
//
// The pathology it exists for was measured, not imagined: in a 5,595-event runtime log,
// zero events carried a sub-second timestamp and 97% shared a timestamp with another event
// in the same session, so nothing downstream could tell which of two events came first and
// ordered detection fell back to the order the lines happened to land in the file.
//
// A log with no sub-second timestamp at all is that regression returning, and fails. A log
// where only some events carry one is a log spanning an agent upgrade, which is expected
// and only warns.
func checkOrdering(v *Verdict, log Log) {
	var (
		stamped   int
		parseable int
		collided  []string
	)
	type collisionKey struct {
		session string
		at      int64 // UnixNano of the parsed timestamp
	}
	seen := map[collisionKey]string{}
	for _, e := range log.Events {
		if e.ParseErr != nil {
			continue
		}
		ts := e.Typed.Timestamp
		parsed, err := obs.ParseTimestamp(ts)
		if err != nil {
			continue
		}
		parseable++
		if strings.Contains(ts, ".") {
			stamped++
		}
		session := ""
		if e.Typed.Session != nil {
			session = e.Typed.Session.ID
		}
		// A shared timestamp is only a problem when nothing else separates the two events.
		if e.Typed.Sequence != 0 {
			continue
		}
		key := collisionKey{session: session, at: parsed.UnixNano()}
		if previous, ok := seen[key]; ok {
			collided = append(collided, fmt.Sprintf("%s:%d (%s, shared with %s, neither sequenced)",
				log.Path, e.Line, ts, previous))
			continue
		}
		seen[key] = fmt.Sprintf("%s:%d", log.Path, e.Line)
	}

	if parseable > 0 && stamped == 0 {
		v.Add(Finding{
			Check: "invariant.timestamp_precision", Severity: SevFail,
			Summary:  fmt.Sprintf("0 of %d event(s) carry a sub-second timestamp", parseable),
			Why:      "without sub-second stamping most events in a session share a timestamp, and nothing can order them",
			Evidence: cap5(firstTimestamps(log)),
		})
	} else if stamped < parseable {
		v.Add(Finding{
			Check: "invariant.timestamp_precision", Severity: SevWarn,
			Summary: fmt.Sprintf("%d of %d event(s) carry a second-resolution timestamp",
				parseable-stamped, parseable),
			Why: "expected in a log written across an agent upgrade; otherwise a writer is not stamping canonically",
		})
	}
	if len(collided) > 0 {
		v.Add(Finding{
			Check: "invariant.event_orderable", Severity: SevWarn,
			Summary:  fmt.Sprintf("%d event(s) share a session timestamp with no sequence to separate them", len(collided)),
			Why:      "two events that tie on both keys can only be ordered by where they landed in the file",
			Evidence: cap5(collided),
		})
	}
}

// firstTimestamps returns the timestamps of the first few parseable events, as evidence for
// a precision finding.
func firstTimestamps(log Log) []string {
	var out []string
	for _, e := range log.Events {
		if e.ParseErr != nil || e.Typed.Timestamp == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d (%q)", log.Path, e.Line, e.Typed.Timestamp))
		if len(out) == 5 {
			break
		}
	}
	return out
}

func checkActions(v *Verdict, log Log) {
	unknown := map[string][]string{}
	for _, e := range log.Events {
		if e.ParseErr != nil {
			continue
		}
		a := e.Action()
		if a == "" || KnownActions[a] {
			continue
		}
		unknown[a] = append(unknown[a], fmt.Sprintf("%s:%d", log.Path, e.Line))
	}
	if len(unknown) == 0 {
		return
	}
	names := make([]string, 0, len(unknown))
	var ev []string
	for a, lines := range unknown {
		names = append(names, a)
		ev = append(ev, lines...)
	}
	sort.Strings(names)
	v.Add(Finding{
		Check: "invariant.known_actions", Severity: SevWarn,
		Summary:  "unrecognized event.action value(s): " + strings.Join(names, ", "),
		Why:      "actions are inline string literals with no shared enum, so a typo silently disables matching rules",
		Evidence: cap5(ev),
	})
}

// checkSecrets asserts no injected secret survived into the artifact.
func checkSecrets(v *Verdict, log Log, secrets Secrets) {
	for name, reason := range secrets.Withheld {
		v.Add(Finding{
			Check: "invariant.no_secret_leak", Severity: SevWarn,
			Summary: fmt.Sprintf("cannot check for %s: %s, so the check is vacuous", name, reason),
			Why: "the artifacts cannot be searched for a value this process never held; treat " +
				"this as unverified rather than clean",
		})
	}
	for name, val := range secrets.Values {
		if strings.TrimSpace(val) == "" {
			// Not a pass: an empty value means the check could not run, and reporting it
			// as clean would be a false assurance.
			v.Add(Finding{
				Check: "invariant.no_secret_leak", Severity: SevWarn,
				Summary: fmt.Sprintf("cannot check for %s: value was empty, so the check is vacuous", name),
				Why:     "a substring search for the empty string matches everything; treat this as unverified",
			})
			continue
		}
		var ev []string
		for _, e := range log.Events {
			if strings.Contains(e.Raw, val) {
				ev = append(ev, fmt.Sprintf("%s:%d", log.Path, e.Line))
			}
		}
		if len(ev) > 0 {
			v.Add(Finding{
				Check: "invariant.no_secret_leak", Severity: SevFail,
				Summary:  fmt.Sprintf("%s appears verbatim in %d event(s)", name, len(ev)),
				Why:      "retained content is routed through redaction; a live credential in the log is a disclosure",
				Evidence: cap5(ev),
			})
		}
	}
}

// HostSafety folds the out-of-band safety signals into the verdict.
//
// These are not derived from the event log, but they belong in the same verdict: a run that
// captured perfect telemetry while clobbering the developer's ~/.beacon, or while leaking the
// credential into the process table, is not a passing run.
type HostSafety struct {
	// HostChanged describes any modification to guarded host state, empty if clean.
	HostChanged string
	// SecretInArgv is true if the credential was found in the guest process table.
	SecretInArgv bool
	// ArgvCheckRan is false when the scan could not run, so its result proves nothing.
	ArgvCheckRan bool
	// Disposability is the evidence a local-guest run offers in place of a comparison. Only
	// meaningful when HostChanged is hostguard.EphemeralDescription.
	Disposability string
}

// Safety adds findings for the host-isolation and credential-handling guarantees.
func Safety(v *Verdict, s HostSafety) {
	// hostguard.Diff.Describe always returns a non-empty string -- "host state unchanged" when
	// clean -- so an empty value can only mean the comparison never happened. Silence there
	// would report an unrun check as a passing one, which is the same mistake the argv branch
	// below already avoids. Reported by Cursor Bugbot, which spotted the inconsistency between
	// the two halves of this function.
	switch {
	case s.HostChanged == "":
		v.Add(Finding{
			Check: "safety.host_untouched", Severity: SevWarn,
			Summary: "no host comparison was recorded, so host isolation is unverified for this run",
			Why:     "treat as unproven, not as clean; a clean comparison records \"host state unchanged\"",
		})
	case s.HostChanged == hostguard.PartialDescription:
		// Nothing was seen to change, but the service probe could not run, so half the guard is
		// unverified. Not a failure -- a flaky launchctl or systemctl is not a host escape, and
		// treating it as one would manufacture the most serious finding this tool has out of a
		// tooling hiccup. Not silence either.
		v.Add(Finding{
			Check: "safety.host_untouched", Severity: SevWarn,
			Summary: "files unchanged, but the service probe could not run, so services are unverified",
			Why:     "treat the service half as unproven; the file half of the guard did pass",
		})
	case s.HostChanged == hostguard.EphemeralDescription:
		// The guest was this machine, so there was no comparison to make: the install the
		// scenario is testing *is* the change a comparison would report. Isolation therefore
		// rests on the machine being disposable, which is a real but weaker guarantee -- so it
		// is reported rather than passed over in silence, and the evidence is named so a reader
		// can tell "GitHub hands out a fresh VM per job" from "the operator said so".
		//
		// Warn, not fail: a dispatched CI run is a legitimate and intended configuration. Warn,
		// not silence: the file-and-service comparison every other run gets did not happen here,
		// and silence in this tool means verified-clean.
		why := "the comparison every other run gets did not happen; isolation is only as good as " +
			"the machine being disposable"
		if s.Disposability != "" {
			why += " (" + s.Disposability + ")"
		}
		v.Add(Finding{
			Check: "safety.host_untouched", Severity: SevWarn,
			Summary: "host isolation was not verified by comparison because the guest was this machine",
			Why:     why,
		})
	case s.HostChanged != hostguard.CleanDescription:
		v.Add(Finding{
			Check: "safety.host_untouched", Severity: SevFail,
			Summary: "guarded host state changed during the run: " + s.HostChanged,
			Why: "every Beacon install and service operation must happen inside the sandbox; " +
				"a local change means a code path escaped isolation",
		})
	}

	switch {
	case s.SecretInArgv:
		v.Add(Finding{
			Check: "safety.secret_not_in_argv", Severity: SevFail,
			Summary: "the injected credential was found in the guest process table",
			Why:     "argv is world-readable via /proc, so a credential there is a disclosure",
		})
	case !s.ArgvCheckRan:
		// Deliberately a warning rather than silence: an unverified claim must not read as
		// a verified one.
		v.Add(Finding{
			Check: "safety.secret_not_in_argv", Severity: SevWarn,
			Summary: "the argv scan did not run, so credential handling is unverified for this run",
			Why:     "treat as unproven, not as clean",
		})
	}
}

func cap5(s []string) []string {
	if len(s) <= 5 {
		return s
	}
	return append(s[:5:5], fmt.Sprintf("... and %d more", len(s)-5))
}

var _ = obs.Vendor // keep the dependency explicit even if Validate() moves
