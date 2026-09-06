package tokens

import (
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Token spend coverage: which runtimes on this machine are contributing token telemetry, and
// which are not.
//
// A token report answers "what did I spend". It cannot answer "is this all of it", and that is
// the question that matters before anyone treats a number as a bill. A runtime whose usage never
// arrives is indistinguishable, in every rollup Beacon prints, from a runtime nobody used: both
// are simply absent. So a total can quietly omit an entire agent and still look complete.
//
// Coverage answers the second question by joining what is installed against what actually
// reported, and it exists because the alternative is a reader inferring coverage from a docs
// table. The table describes what Beacon is built to read; only the log knows what this machine
// produced.
//
// The expectation table below is what keeps the report from crying wolf. Cursor's hooks carry no
// token counts at all, so Cursor contributing nothing is correct behavior, not a fault -- reported
// as NotInstrumented rather than Silent. Silent is reserved for the case worth investigating: a
// runtime that ran, that Beacon is built to read tokens from, and that reported none anyway.

// Coverage status values.
const (
	// CoverageCovered: the runtime reported usage in the window. Nothing to do.
	CoverageCovered = "covered"
	// CoverageSilent: the runtime produced events but no usage, and Beacon is built to read
	// usage from it. This is the actionable one -- a broken hook, an old runtime build, or a
	// capture path that regressed.
	CoverageSilent = "silent"
	// CoverageInactive: the runtime is installed but produced no events at all in the window.
	// Usually means it was not used; can also mean its hooks are not installed.
	CoverageInactive = "inactive"
	// CoverageNotInstrumented: the runtime produced events, and Beacon cannot read token usage
	// from it. Absence of tokens here is expected and is not a fault to chase.
	CoverageNotInstrumented = "not_instrumented"
)

// Expectation values: what Beacon can read from a runtime, independent of what it did read.
const (
	// ExpectReported: usage is collected from this runtime and verified against captured
	// payloads.
	ExpectReported = "reported"
	// ExpectGenericOTLP: usage is collected only if the runtime emits the OpenTelemetry GenAI
	// semconv names. The generic path is tested; these runtimes' own payloads are not pinned,
	// so silence here may mean the runtime spells its attributes differently rather than that
	// anything is broken.
	ExpectGenericOTLP = "generic_otlp"
	// ExpectNone: the runtime exposes no token counts on the surface Beacon collects from.
	ExpectNone = "none"
)

// usageExpectation records, per canonical harness name, whether Beacon can read token usage from
// that runtime and why. Keys are canonical harness names, so callers must normalize before
// looking up.
//
// This duplicates the per-runtime table in docs/cli/token-usage.mdx by necessity: the docs are for
// a reader deciding whether to expect numbers, this is for the report deciding whether silence is
// a fault. They must be kept in step, and the reason strings are written to be printed.
var usageExpectation = map[string]struct {
	expect string
	reason string
}{
	"claude_code":       {ExpectReported, "OTLP token and cost telemetry"},
	"codex_cli":         {ExpectReported, "per-turn usage trace; Codex emits no cost"},
	"claude_cowork":     {ExpectReported, "OTLP token and cost telemetry"},
	"cline":             {ExpectReported, "plugin reports usage once per task"},
	"opencode":          {ExpectReported, "plugin reports usage per assistant message"},
	"pi_cli":            {ExpectReported, "extension reports usage and cost"},
	"omp":               {ExpectReported, "extension reports usage and cost"},
	"vercel_fx":         {ExpectReported, "session store carries cumulative usage and cost"},
	"asymptote_observe": {ExpectReported, "SDK spans carry semconv usage"},

	"gemini_cli":       {ExpectGenericOTLP, "only if it emits OTel GenAI semconv usage"},
	"copilot_cli":      {ExpectGenericOTLP, "only if it emits OTel GenAI semconv usage"},
	"vscode_copilot":   {ExpectGenericOTLP, "only if it emits OTel GenAI semconv usage"},
	"factory":          {ExpectGenericOTLP, "only if it emits OTel GenAI semconv usage"},
	"factory_droid":    {ExpectGenericOTLP, "only if it emits OTel GenAI semconv usage"},
	"openclaw_gateway": {ExpectGenericOTLP, "only if it emits OTel GenAI semconv usage"},
	// Grok Bot runs on a Cursor-hosted cloud computer and reaches Beacon only through Cursor's
	// server-side OpenTelemetry export, so whether usage arrives depends on whether that export
	// carries the semconv names -- the generic-OTLP case exactly, not a runtime Beacon reads.
	"grok_bot": {ExpectGenericOTLP, "only if Cursor's server-side OTel export carries GenAI semconv usage"},

	"cursor":          {ExpectNone, "Cursor hook payloads carry no token counts"},
	"antigravity_cli": {ExpectNone, "hook payloads carry no token counts"},
	"grok":            {ExpectNone, "hook payloads carry no token counts"},
	"hermes":          {ExpectNone, "hook payloads carry no token counts"},
	// Devin reaches the log under three names, and all three need an entry. The hook installer
	// writes --platform devin-cli and devin-desktop, an older install still writes plain devin,
	// and NormalizeHarnessName passes all three through unchanged. Keying only "devin" left the
	// other two unrecognized, which classified a Devin session that correctly reported no tokens
	// as silent -- the one status this table exists to keep clean.
	"devin":         {ExpectNone, "hook payloads carry no token counts"},
	"devin-cli":     {ExpectNone, "hook payloads carry no token counts"},
	"devin-desktop": {ExpectNone, "Cascade/Windsurf hook payloads carry no token counts"},
	"muse_code":     {ExpectNone, "usage arrives on PostLLMCall, which Beacon does not subscribe to"},
	"qwen_code":     {ExpectNone, "Stop carries session context counters, not per-call usage"},
	"prime_agent":   {ExpectNone, "no managed extension ships yet"},
	"claude_web":    {ExpectNone, "no recorded claude.ai stream has carried a usage object"},
	"chatgpt_web":   {ExpectNone, "the chat stream reports no token counts"},
	"openhands":     {ExpectNone, "hook payloads carry no token counts"},
}

// RuntimeCoverage is one runtime's line in the coverage report.
type RuntimeCoverage struct {
	Harness     string `json:"harness"`
	Status      string `json:"status"`
	Expectation string `json:"expectation"`
	Reason      string `json:"reason,omitempty"`
	Installed   bool   `json:"installed"`
	Events      int    `json:"events"`
	UsageEvents int    `json:"usage_events"`
	Tokens      int64  `json:"tokens"`
}

// CoverageReport is the whole join: one line per runtime that is either installed or seen in the
// log, plus the counts a reader needs to judge the window itself.
type CoverageReport struct {
	Runtimes []RuntimeCoverage `json:"runtimes"`
	// Silent counts runtimes worth investigating. Zero is the good state.
	Silent int `json:"silent"`
	// Covered counts runtimes that contributed usage.
	Covered int `json:"covered"`
	// TotalEvents is the size of the window the join was computed over. A coverage report over
	// an empty window says nothing, and a reader needs to be able to see that.
	TotalEvents int `json:"total_events"`
}

// Coverage joins installed runtimes against the runtimes that actually reported token usage.
//
// installed carries runtime identifiers as the inventory scanner spells them; they are normalized
// here, so callers pass them through unchanged. A runtime seen in the log but not installed is
// still reported -- telemetry can arrive from a runtime whose config the scanner does not know,
// and dropping it would hide exactly the coverage a reader is looking for.
func Coverage(events []schema.Event, installed []string) CoverageReport {
	type counters struct {
		events      int
		usageEvents int
		tokens      int64
		installed   bool
	}
	byHarness := map[string]*counters{}
	get := func(name string) *counters {
		if byHarness[name] == nil {
			byHarness[name] = &counters{}
		}
		return byHarness[name]
	}

	for _, name := range installed {
		normalized := asymptoteobserve.NormalizeHarnessName(strings.TrimSpace(name))
		if normalized == "" {
			continue
		}
		get(normalized).installed = true
	}

	// Usage is counted through the same collector the report uses, so a runtime counts as
	// covered on exactly the events that would contribute to its totals -- including the
	// dedupe and cumulative-delta handling. Counting raw gen_ai.usage blocks here instead
	// would report a runtime as covered whose usage the report then discards.
	usageEvents := collectUsageEvents(events, sessionUserContexts(events))
	usageEvents = preferCodexTurnSpans(usageEvents)
	usageEvents = dedupeOverlappingChannels(usageEvents)
	resolveCumulativeSeries(usageEvents)

	for _, event := range events {
		name := asymptoteobserve.NormalizeHarnessName(strings.TrimSpace(event.Harness.Name))
		if name == "" {
			continue
		}
		get(name).events++
	}
	for _, ue := range usageEvents {
		name := asymptoteobserve.NormalizeHarnessName(strings.TrimSpace(ue.harness))
		if name == "" {
			continue
		}
		c := get(name)
		c.usageEvents++
		c.tokens += ue.usage.TotalTokens()
	}

	report := CoverageReport{TotalEvents: len(events)}
	for name, c := range byHarness {
		expectation, reason := expectationFor(name)
		line := RuntimeCoverage{
			Harness:     name,
			Expectation: expectation,
			Reason:      reason,
			Installed:   c.installed,
			Events:      c.events,
			UsageEvents: c.usageEvents,
			Tokens:      c.tokens,
		}
		switch {
		case c.usageEvents > 0:
			line.Status = CoverageCovered
			report.Covered++
		case c.events == 0:
			line.Status = CoverageInactive
		case expectation == ExpectNone:
			line.Status = CoverageNotInstrumented
		default:
			line.Status = CoverageSilent
			report.Silent++
		}
		report.Runtimes = append(report.Runtimes, line)
	}

	// Silent first, because that is the only status anyone has to act on; then by spend, so the
	// runtimes that matter most sit at the top of each group.
	sort.SliceStable(report.Runtimes, func(i, j int) bool {
		left, right := report.Runtimes[i], report.Runtimes[j]
		if rank := statusRank(left.Status) - statusRank(right.Status); rank != 0 {
			return rank < 0
		}
		if left.Tokens != right.Tokens {
			return left.Tokens > right.Tokens
		}
		return left.Harness < right.Harness
	})
	return report
}

func statusRank(status string) int {
	switch status {
	case CoverageSilent:
		return 0
	case CoverageCovered:
		return 1
	case CoverageInactive:
		return 2
	default:
		return 3
	}
}

// expectationFor reports what Beacon can read from a harness. An unrecognized harness is treated
// as generic OTLP rather than as "none": a runtime Beacon has never heard of reaching the log at
// all means something is exporting semconv telemetry, and reporting it as silent when it carries
// no usage is the honest answer -- calling it not_instrumented would assert knowledge about a
// runtime nobody has looked at.
func expectationFor(harness string) (string, string) {
	if entry, ok := usageExpectation[harness]; ok {
		return entry.expect, entry.reason
	}
	return ExpectGenericOTLP, "unrecognized runtime; usage is read only from OTel GenAI semconv names"
}
