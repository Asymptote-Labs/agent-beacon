package harness2session

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// MappedEvent is one event mapped from a trajectory, with the line it came from so the collector can
// advance its cursor only past lines whose events were all written.
type MappedEvent struct {
	DedupID    string
	SourceLine int
	Event      schema.Event
}

// MapOptions says how much of a trajectory an earlier sweep already wrote.
type MapOptions struct {
	// MinLine is the last line whose events were all written. Lines at or before it are still read,
	// because the session id and the running token totals come from the whole stream, but emit
	// nothing.
	MinLine int
	// SkipStarted is set once session.started was written for this trajectory.
	SkipStarted bool
}

// MapTrajectory maps a decoded trajectory to events in stream order.
func MapTrajectory(t Trajectory, opts MapOptions) []MappedEvent {
	m := &mapper{ref: t.Ref, opts: opts, sessionID: sessionIDOf(t)}
	switch t.Ref.Format {
	case FormatOpenCode:
		m.mapOpenCode(t.Lines)
	case FormatCodexExec:
		m.mapCodexExec(t.Lines)
	}
	return m.out
}

type mapper struct {
	ref       TrajectoryRef
	opts      MapOptions
	sessionID string
	out       []MappedEvent
}

// sessionIDOf is the executor's own id for the session: OpenCode's sessionID, which every event
// carries, or the thread id Codex announces first. A stream that never names one -- a Codex run
// that failed before its thread started -- gets an id derived from the file's place in the runs
// directory, so its events still group as one session and keep their ids across sweeps.
func sessionIDOf(t Trajectory) string {
	for _, line := range t.Lines {
		switch t.Ref.Format {
		case FormatOpenCode:
			if id := str(line.Object, "sessionID"); id != "" {
				return id
			}
		case FormatCodexExec:
			if str(line.Object, "type") == "thread.started" {
				if id := str(line.Object, "thread_id"); id != "" {
					return id
				}
			}
		}
	}
	sum := sha256.Sum256([]byte(t.Ref.Format.Executor() + ":" + t.Ref.Rel))
	return "harness2-" + hex.EncodeToString(sum[:8])
}

func (m *mapper) emits(line Line) bool { return line.Number > m.opts.MinLine }

func (m *mapper) base(line Line, timestampMS int64, action, category string, severity schema.Severity, fidelity, message string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Fidelity: fidelity,
		Message:  message,
		Origin:   schema.OriginLocal,
		Harness: schema.HarnessInfo{
			Name:             Harness,
			CollectionMethod: schema.CollectionMethodPoll,
		},
	})
	// Codex's exec stream carries no times at all, so its events take the file's modification time:
	// when Harness² last wrote the stream. That is the end of the execution rather than the moment
	// of each step, and the order within the session comes from the order the events are written.
	if timestampMS <= 0 {
		timestampMS = m.ref.ModTimeUnixMS
	}
	if timestampMS > 0 {
		ev.Timestamp = schema.FormatTimestamp(time.UnixMilli(timestampMS))
	}
	ev.Session = &schema.SessionInfo{ID: m.sessionID}
	ev.Raw = map[string]interface{}{"harness2": m.contextRaw(line)}
	return ev
}

// contextRaw is the Harness² context every event carries: which executor ran, and what the runs
// directory says about the rollout. It is the reason to read these files through this collector
// rather than as plain OpenCode or Codex sessions.
func (m *mapper) contextRaw(line Line) map[string]interface{} {
	ctx := m.ref.Context
	raw := map[string]interface{}{
		"executor":   m.ref.Format.Executor(),
		"format":     string(m.ref.Format),
		"trajectory": m.ref.Rel,
		"line":       line.Number,
	}
	for key, value := range map[string]string{
		"benchmark":           ctx.Benchmark,
		"subset":              ctx.Subset,
		"task_id":             ctx.TaskID,
		"label":               ctx.Label,
		"run_id":              ctx.RunID,
		"trial":               ctx.Trial,
		"harness_fingerprint": ctx.HarnessFingerprint,
	} {
		if value != "" {
			raw[key] = value
		}
	}
	if ctx.Attempt > 0 {
		raw["attempt"] = ctx.Attempt
	}
	return raw
}

// emitStarted writes session.started once per trajectory, at the stream's first event. Neither
// stream format records a session start as such -- OpenCode's opens with its first step and Codex's
// with the thread id -- so the event is inferred from the stream having begun, and it is where the
// session's Harness² context is first stated.
func (m *mapper) emitStarted(line Line, timestampMS int64, fidelity string) {
	if m.opts.SkipStarted || !m.emits(line) {
		return
	}
	ev := m.base(line, timestampMS, "session.started", "session", schema.SeverityInfo, fidelity, "Harness² "+m.ref.Format.Executor()+" rollout started")
	m.append(line, "session.started", ev)
}

func (m *mapper) append(line Line, suffix string, ev schema.Event) {
	dedupID := fmt.Sprintf("%s:%s:%d:%s", m.ref.Format.Executor(), m.sessionID, line.Number, suffix)
	ev.Event.ID = harness2EventID(dedupID)
	m.out = append(m.out, MappedEvent{DedupID: dedupID, SourceLine: line.Number, Event: ev})
}

func (m *mapper) emitText(line Line, timestampMS int64, action, suffix, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	var ev schema.Event
	if action == "agent.reasoning" {
		ev = m.base(line, timestampMS, action, "session", schema.SeverityInfo, schema.FidelityObserved, "Harness² executor reasoning")
		ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: []interface{}{map[string]interface{}{
			"role":  "assistant",
			"parts": []interface{}{map[string]interface{}{"type": "reasoning", "content": text}},
		}}}}
	} else {
		ev = m.base(line, timestampMS, action, "session", schema.SeverityInfo, schema.FidelityObserved, "Harness² executor message")
		ev.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(text)}}
	}
	ev.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultRawStringLimit)
	m.append(line, suffix, ev)
}

func (m *mapper) emitError(line Line, timestampMS int64, severity schema.Severity, errorType, message string) {
	ev := m.base(line, timestampMS, "session.error", "session", severity, schema.FidelityObserved, "Harness² executor error")
	ev.Error = &schema.ErrorInfo{Type: firstNonEmpty(errorType, "session_error")}
	if message = strings.TrimSpace(message); message != "" {
		ev.Raw["error_message"] = truncate(message, 2000)
	}
	m.append(line, "error", ev)
}

func usageEvent(usage *schema.GenAIUsageInfo) bool {
	return usage != nil && (usage.InputTokens != nil || usage.OutputTokens != nil || usage.Reasoning != nil ||
		usage.CacheRead != nil || usage.CacheCreation != nil || usage.CostUSD != nil)
}

func (m *mapper) emitUsage(line Line, timestampMS int64, usage *schema.GenAIUsageInfo) {
	if !usageEvent(usage) {
		return
	}
	ev := m.base(line, timestampMS, "token.usage", "metric", schema.SeverityInfo, schema.FidelityObserved, "Harness² executor token usage")
	ev.GenAI = &schema.GenAIInfo{Usage: usage}
	m.append(line, "usage", ev)
}

// toolCall fills the GenAI tool-call block every tool event carries.
func toolCall(ev *schema.Event, name, callID string, args interface{}, result interface{}) {
	call := &schema.GenAIToolCallInfo{ID: callID}
	if args != nil {
		call.Arguments = args
	}
	if result != nil {
		call.Result = result
	}
	ev.GenAI = &schema.GenAIInfo{
		Operation: &schema.GenAIOperationInfo{Name: "execute_tool"},
		Tool:      &schema.GenAIToolInfo{Name: name, Call: call},
	}
}

var harness2IDNamespace = [16]byte{
	0x3c, 0x91, 0x0e, 0x7f, 0x22, 0xd4, 0x4b, 0x58,
	0x9a, 0x13, 0x6e, 0xc0, 0x45, 0xb7, 0x2f, 0xa8,
}

// harness2EventID is a name-based UUID over the event's coordinate in its session: the executor,
// the session id, the source line and what the event is. A re-read of the same stream names every
// event the same way, so a resent line is recognized rather than duplicated.
func harness2EventID(dedupID string) string {
	digest := sha1.New()
	digest.Write(harness2IDNamespace[:])
	io.WriteString(digest, dedupID)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}

func object(m map[string]interface{}, key string) map[string]interface{} {
	value, _ := m[key].(map[string]interface{})
	return value
}

func str(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// num reads a JSON number as an int64, refusing what is not a whole, finite count.
func num(m map[string]interface{}, key string) (int64, bool) {
	switch value := m[key].(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || math.Abs(value) > 1<<53 {
			return 0, false
		}
		return int64(value), true
	case json.Number:
		n, err := value.Int64()
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

// exitCode reads a process exit status. Both executors report it as a 32-bit integer (Codex's
// Option<i32>, OpenCode's process exit code); anything outside that range is not one, and is left
// out rather than wrapped into a plausible-looking value.
func exitCode(m map[string]interface{}, key string) *int {
	code, ok := num(m, key)
	if !ok || code < math.MinInt32 || code > math.MaxInt32 {
		return nil
	}
	exit := int(code)
	return &exit
}

func positive(m map[string]interface{}, key string) int64 {
	if value, ok := num(m, key); ok && value > 0 {
		return value
	}
	return 0
}

func ptr(value int64) *int64 { return &value }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xc0) == 0x80 {
		cut--
	}
	return s[:cut]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func outputText(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(data)
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
