package copilotsession

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// Copilot CLI reports what a session cost as totalNanoAiu on session.shutdown:
// the session-wide accumulated cost in nano AI units. The Copilot SDK defines
// one AI unit as 1e9 nano-AIU (NANO_AIU_PER_AIU, which it uses to turn a
// maxAiCredits limit into a nano-AIU ceiling), and GitHub bills Copilot in AI
// credits worth US$0.01 each. So one nano-AIU is US$1e-11.
const nanoAiuPerUSD = 100_000_000_000

// usageBillingCutover is when Copilot moved from premium requests to AI
// credits. A session started before it was billed per premium request, so
// whatever totalNanoAiu it carries is not the dollar charge and Beacon
// records no cost for it. Monthly allowances reset at 00:00 UTC, so the
// cutover is taken at midnight UTC on the first day of the new regime.
var usageBillingCutover = time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)

// maxExactNanoAiu keeps conversion exact: below 2^53 a float64 holds every
// integer, so the one division into dollars is the only rounding step.
const maxExactNanoAiu = 1 << 53

// nanoAiuValue reads a totalNanoAiu as a whole number of nano-AIU. Fractions
// of a nano-AIU (US$1e-11) are rounded to the nearest unit so differencing
// stays in integers.
func nanoAiuValue(value interface{}) (int64, bool) {
	var f float64
	switch v := value.(type) {
	case float64:
		f = v
	case int64:
		return v, v >= 0
	case int:
		return int64(v), v >= 0
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, n >= 0
		}
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	case string:
		text := strings.TrimSpace(v)
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return n, n >= 0
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f >= maxExactNanoAiu {
		return 0, false
	}
	return int64(math.Round(f)), true
}

func nanoAiuToUSD(n int64) float64 {
	return float64(n) / nanoAiuPerUSD
}

// noteSessionStart records when the session began, from session.start's own
// startTime or, failing that, the record's timestamp. It is what decides the
// billing regime, so a resume never moves it.
func (m *mapper) noteSessionStart(record Record) {
	if m.sessionStartMS > 0 {
		return
	}
	if ms := timestampMS(record.Data["startTime"], 0); ms > 0 {
		m.sessionStartMS = ms
		return
	}
	m.sessionStartMS = timestampMS(record.Time, 0)
}

// shutdownCostDelta returns the nano-AIU this shutdown adds to the session's
// reported cost, and advances the baseline. totalNanoAiu is cumulative and
// repeated on every shutdown, so only the rise since the last one is new. A
// total that falls is treated like the token totals beside it: the lower
// value becomes the baseline and nothing is emitted for it, so a reset can
// never count spend twice. omitted names why a positive rise was not recorded.
func (m *mapper) shutdownCostDelta(record Record) (delta int64, omitted string) {
	total, ok := nanoAiuValue(record.Data["totalNanoAiu"])
	if !ok {
		return 0, ""
	}
	if m.sessionStartMS <= 0 {
		// sessionStartTime is the runtime's own record of when the session
		// began; it covers a store whose session.start line is missing.
		m.sessionStartMS = timestampMS(record.Data["sessionStartTime"], 0)
	}
	previous := m.lastNanoAiu
	m.lastNanoAiu = total
	if total <= previous {
		return 0, ""
	}
	switch {
	case m.sessionStartMS <= 0:
		return 0, "session_start_unknown"
	case time.UnixMilli(m.sessionStartMS).Before(usageBillingCutover):
		return 0, "premium_request_billing"
	}
	return total - previous, ""
}
