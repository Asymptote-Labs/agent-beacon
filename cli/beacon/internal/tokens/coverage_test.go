package tokens

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func normalizedHarnessForTest(name string) string {
	return asymptoteobserve.NormalizeHarnessName(name)
}

// plainEvent is an event with no usage: it proves a runtime ran without contributing tokens,
// which is the distinction the whole report turns on.
func plainEvent(harness string) schema.Event {
	return schema.Event{
		Timestamp: "2026-06-11T10:00:00Z",
		Event:     schema.EventInfo{Kind: "agent_runtime", Action: "command.executed", Category: "command"},
		Harness:   schema.HarnessInfo{Name: harness},
	}
}

func lineFor(t *testing.T, report CoverageReport, harness string) RuntimeCoverage {
	t.Helper()
	for _, runtime := range report.Runtimes {
		if runtime.Harness == harness {
			return runtime
		}
	}
	t.Fatalf("no coverage line for %q in %+v", harness, report.Runtimes)
	return RuntimeCoverage{}
}

func TestCoverageMarksReportingRuntimesCovered(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-opus-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(100)
			e.GenAI.Usage.OutputTokens = int64Ptr(20)
		}),
	}
	report := Coverage(events, []string{"claude_code"})
	line := lineFor(t, report, "claude_code")
	if line.Status != CoverageCovered {
		t.Fatalf("status = %q, want covered", line.Status)
	}
	if line.Tokens != 120 {
		t.Fatalf("tokens = %d, want 120", line.Tokens)
	}
	if report.Covered != 1 || report.Silent != 0 {
		t.Fatalf("covered=%d silent=%d, want 1/0", report.Covered, report.Silent)
	}
}

// The case the report exists for: a runtime Beacon is built to read tokens from ran, and reported
// none. That is a broken capture path, not a quiet week, and it must be surfaced.
func TestCoverageFlagsARuntimeThatShouldHaveReportedUsage(t *testing.T) {
	report := Coverage([]schema.Event{plainEvent("claude_code")}, []string{"claude_code"})
	line := lineFor(t, report, "claude_code")
	if line.Status != CoverageSilent {
		t.Fatalf("status = %q, want silent", line.Status)
	}
	if report.Silent != 1 {
		t.Fatalf("silent = %d, want 1", report.Silent)
	}
}

// The case that keeps the report from crying wolf. Cursor's hooks carry no token counts, so
// Cursor contributing nothing is correct behavior. Reporting it as silent every week would train
// a reader to ignore the column that matters.
func TestCoverageDoesNotFlagRuntimesThatCannotReportUsage(t *testing.T) {
	events := []schema.Event{plainEvent("cursor"), plainEvent("qwen_code"), plainEvent("muse_code"), plainEvent("devin-cli"), plainEvent("devin-desktop")}
	report := Coverage(events, []string{"cursor", "qwen_code", "muse_code", "devin-cli", "devin-desktop"})
	for _, harness := range []string{"cursor", "qwen_code", "muse_code", "devin-cli", "devin-desktop"} {
		line := lineFor(t, report, harness)
		if line.Status != CoverageNotInstrumented {
			t.Errorf("%s status = %q, want not_instrumented", harness, line.Status)
		}
		if line.Reason == "" {
			t.Errorf("%s has no reason; the reason is what stops a reader chasing it", harness)
		}
	}
	if report.Silent != 0 {
		t.Fatalf("silent = %d, want 0 -- none of these can report usage", report.Silent)
	}
}

// Installed but never run is not the same as installed and broken, and conflating them would
// make every machine look faulty on the day someone did not use one of their agents.
func TestCoverageSeparatesInactiveFromSilent(t *testing.T) {
	report := Coverage([]schema.Event{plainEvent("claude_code")}, []string{"claude_code", "codex_cli"})
	if got := lineFor(t, report, "codex_cli").Status; got != CoverageInactive {
		t.Fatalf("codex_cli status = %q, want inactive", got)
	}
	if got := lineFor(t, report, "claude_code").Status; got != CoverageSilent {
		t.Fatalf("claude_code status = %q, want silent", got)
	}
	if report.Silent != 1 {
		t.Fatalf("silent = %d, want 1 -- an unused runtime is not a fault", report.Silent)
	}
}

// A runtime can reach the log without the scanner knowing its config: cloud agents, CI, a
// runtime installed somewhere the scanner does not look. Dropping those rows would hide exactly
// the coverage the reader came for.
func TestCoverageReportsRuntimesSeenButNotInstalled(t *testing.T) {
	report := Coverage([]schema.Event{plainEvent("claude_code")}, nil)
	line := lineFor(t, report, "claude_code")
	if line.Installed {
		t.Fatal("claude_code reported as installed when the scanner never saw it")
	}
	if line.Status != CoverageSilent {
		t.Fatalf("status = %q, want silent", line.Status)
	}
}

// Installed names are the inventory scanner's spellings, which are not always harness names.
// Without normalization vscode would never join to vscode_copilot and would be reported as
// inactive alongside a covered vscode_copilot -- one runtime shown twice, once wrongly.
func TestCoverageNormalizesInstalledRuntimeNames(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "vscode_copilot", "s1", "gpt-4o", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(10)
		}),
	}
	report := Coverage(events, []string{"vscode"})
	if len(report.Runtimes) != 1 {
		t.Fatalf("got %d runtimes, want 1 -- vscode and vscode_copilot are one runtime: %+v", len(report.Runtimes), report.Runtimes)
	}
	line := lineFor(t, report, "vscode_copilot")
	if !line.Installed || line.Status != CoverageCovered {
		t.Fatalf("line = %+v, want installed and covered", line)
	}
}

// Coverage counts usage through the same pipeline the report totals use. Claude Code emits the
// same tokens on both a log record and a metric; if coverage counted raw gen_ai.usage blocks it
// would credit a runtime for usage the report then discards as a duplicate.
func TestCoverageCountsUsageThroughTheSamePipelineAsTheReport(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-opus-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(100)
		}),
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-opus-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(100)
			e.Raw = map[string]interface{}{"metric_name": "claude_code.token.usage"}
		}),
	}
	report := Coverage(events, []string{"claude_code"})
	aggregate := Aggregate(events, Options{})
	if got := lineFor(t, report, "claude_code").Tokens; got != aggregate.Totals.TotalTokens() {
		t.Fatalf("coverage tokens = %d, report totals = %d; they must agree", got, aggregate.Totals.TotalTokens())
	}
}

// Silent rows are the only ones anyone acts on, so they sort first regardless of spend.
func TestCoverageSortsSilentRuntimesFirst(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-opus-5", func(e *schema.Event) {
			e.GenAI.Usage.InputTokens = int64Ptr(1_000_000)
		}),
		plainEvent("codex_cli"),
	}
	report := Coverage(events, nil)
	if report.Runtimes[0].Harness != "codex_cli" {
		t.Fatalf("first row = %q, want the silent codex_cli ahead of the high-spend covered row", report.Runtimes[0].Harness)
	}
}

// A coverage report over an empty window says nothing at all, and must say so rather than
// rendering an empty table that reads like a clean bill of health.
func TestRenderCoverageTextSaysWhenTheWindowIsEmpty(t *testing.T) {
	var sb strings.Builder
	RenderCoverageText(&sb, Coverage(nil, nil))
	if !strings.Contains(sb.String(), "No events in this window") {
		t.Fatalf("empty-window report did not say so:\n%s", sb.String())
	}
}

func TestRenderCoverageTextExplainsSilentRuntimes(t *testing.T) {
	var sb strings.Builder
	RenderCoverageText(&sb, Coverage([]schema.Event{plainEvent("claude_code")}, nil))
	out := sb.String()
	if !strings.Contains(out, "silent") || !strings.Contains(out, "beacon endpoint diagnostics") {
		t.Fatalf("silent report did not tell the reader what to do:\n%s", out)
	}
}

// Every expectation entry must key on a name that survives normalization, or the lookup silently
// misses and the runtime is reported as an unrecognized generic-OTLP one.
func TestUsageExpectationKeysAreCanonicalHarnessNames(t *testing.T) {
	for name := range usageExpectation {
		if got := normalizedHarnessForTest(name); got != name {
			t.Errorf("expectation key %q normalizes to %q; the lookup would never hit it", name, got)
		}
	}
}

// Every runtime the config scanner can report must resolve to an expectation entry.
//
// This is the invariant the Devin bug broke: the scanner reports "devin-cli" and
// "devin-desktop", NormalizeHarnessName passes both through unchanged, and the table keyed only
// "devin" -- so a Devin session that correctly reported no tokens fell to the unrecognized
// default and was classified silent, which is the one status this table exists to keep clean.
//
// The list is duplicated from the inventory scanner rather than imported, deliberately: importing
// it would make the test track a rename automatically and prove nothing, while a literal list
// fails and makes someone look. Add a runtime to the scanner, add it here and to usageExpectation.
func TestEveryScannedRuntimeHasAnExpectation(t *testing.T) {
	// Mirrors the runtime identifiers in internal/endpoint/inventory/inventory.go.
	scanned := []string{
		"antigravity_cli", "claude_code", "cline", "codex_cli", "copilot_cli", "cursor",
		"devin-cli", "devin-desktop", "factory", "gemini_cli", "grok", "hermes",
		"muse_code", "omp", "opencode", "openhands", "pi_cli", "qwen_code", "vscode",
	}
	for _, runtime := range scanned {
		harness := normalizedHarnessForTest(runtime)
		if _, ok := usageExpectation[harness]; !ok {
			t.Errorf("scanner runtime %q normalizes to %q, which has no usageExpectation entry; "+
				"a session from it that reports no tokens would be classified silent", runtime, harness)
		}
	}
}

// The Devin regression in report form: a Devin CLI or Devin Desktop session that reports no
// tokens must not be alerted on.
func TestCoverageDoesNotFlagAnyDevinSpelling(t *testing.T) {
	for _, harness := range []string{"devin", "devin-cli", "devin-desktop"} {
		report := Coverage([]schema.Event{plainEvent(harness)}, []string{harness})
		if got := lineFor(t, report, harness).Status; got != CoverageNotInstrumented {
			t.Errorf("%s status = %q, want not_instrumented -- Devin reports no token counts", harness, got)
		}
		if report.Silent != 0 {
			t.Errorf("%s produced silent=%d, want 0", harness, report.Silent)
		}
	}
}

// The two harnesses added to main while this branch was open, pinned so neither becomes a
// standing false alarm.
//
// They land in different categories for a reason that is the whole point of the distinction:
// OpenHands is hooked on the endpoint and none of its six hook payloads carries token counts, so
// its silence is a fact about the runtime. Grok Bot runs on a Cursor-hosted cloud computer and
// reaches Beacon only through Cursor's server-side OpenTelemetry export, so its silence depends
// on what that export carries -- unknown, not established.
func TestNewHarnessesAreClassifiedNotAlerted(t *testing.T) {
	openhands := lineFor(t, Coverage([]schema.Event{plainEvent("openhands")}, []string{"openhands"}), "openhands")
	if openhands.Status != CoverageNotInstrumented || openhands.Expectation != ExpectNone {
		t.Errorf("openhands = %+v, want not_instrumented/none -- no OpenHands hook payload carries token counts", openhands)
	}

	grokBot := lineFor(t, Coverage([]schema.Event{plainEvent("grok_bot")}, nil), "grok_bot")
	if grokBot.Expectation != ExpectGenericOTLP {
		t.Errorf("grok_bot expectation = %q, want generic_otlp -- it arrives only via Cursor's OTel export", grokBot.Expectation)
	}

	// Grok Bot and Grok Build are separate products and must not share a row.
	both := Coverage([]schema.Event{plainEvent("grok"), plainEvent("grok_bot")}, nil)
	if len(both.Runtimes) != 2 {
		t.Fatalf("got %d rows, want grok and grok_bot kept apart: %+v", len(both.Runtimes), both.Runtimes)
	}
}
