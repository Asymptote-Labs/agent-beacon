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

// The case that keeps the report from crying wolf. Cursor's hooks carry no token counts, so
// Cursor contributing nothing is correct behavior. Reporting it as silent every week would train
// a reader to ignore the column that matters.
func TestCoverageDoesNotFlagRuntimesThatCannotReportUsage(t *testing.T) {
	events := []schema.Event{plainEvent("cursor")}
	report := Coverage(events, []string{"cursor"})
	for _, harness := range []string{"cursor"} {
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
		"deepseek_harness", "devin-cli", "devin-desktop", "factory", "gemini_cli", "grok", "hermes",
		"kimi_code", "kiro", "muse_code", "omo_senpi", "omp", "openclaw_gateway", "opencode",
		"openhands", "pi_cli", "prime_agent", "qwen_code", "vscode",
	}
	for _, runtime := range scanned {
		harness := normalizedHarnessForTest(runtime)
		if _, ok := usageExpectation[harness]; !ok {
			t.Errorf("scanner runtime %q normalizes to %q, which has no usageExpectation entry; "+
				"a session from it that reports no tokens would be classified silent", runtime, harness)
		}
	}
}

// The Devin regression in report form: a Devin spelling whose only collection path cannot carry
// token counts must not be alerted on. Plain "devin" and "devin-desktop" are hook-only and stay
// not_instrumented; "devin-cli" is expected to report once `beacon endpoint devin sync` has run,
// so a covered session with no sync yet is silent and the reason names the fix.
func TestCoverageFlagsDevinSpellingsByWhatBeaconCanRead(t *testing.T) {
	for _, harness := range []string{"devin", "devin-desktop"} {
		report := Coverage([]schema.Event{plainEvent(harness)}, []string{harness})
		if got := lineFor(t, report, harness).Status; got != CoverageNotInstrumented {
			t.Errorf("%s status = %q, want not_instrumented -- these hooks report no token counts", harness, got)
		}
		if report.Silent != 0 {
			t.Errorf("%s produced silent=%d, want 0", harness, report.Silent)
		}
	}

	report := Coverage([]schema.Event{plainEvent("devin-cli")}, []string{"devin-cli"})
	line := lineFor(t, report, "devin-cli")
	if line.Status != CoverageSilent {
		t.Fatalf("devin-cli status = %q, want silent until the session sync runs", line.Status)
	}
	if !strings.Contains(line.Reason, "beacon endpoint devin sync") {
		t.Fatalf("devin-cli reason = %q, want it to name the sync that reports usage", line.Reason)
	}
}

// Grok Bot and Grok Build are separate products and must not share a row.
func TestCoverageKeepsGrokAndGrokBotApart(t *testing.T) {
	both := Coverage([]schema.Event{plainEvent("grok"), plainEvent("grok_bot")}, nil)
	if len(both.Runtimes) != 2 {
		t.Fatalf("got %d rows, want grok and grok_bot kept apart: %+v", len(both.Runtimes), both.Runtimes)
	}
}

// Only rows Beacon itself configured count as evidence a runtime is wired up to report.
//
// Three rounds of review found three different files that exist for reasons other than the runtime
// the scanner files them under -- a shell profile, the shared workspace .mcp.json, VS Code's
// settings.json -- each producing a permanently inactive row for a product nobody had. The rule is
// no longer "exclude the known liars" but "only count what Beacon put there".
func TestInstalledRuntimesCountsOnlyWhatBeaconConfigured(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config InstalledConfig
		want   bool
	}{
		{
			name:   "a config Beacon wired up counts",
			config: InstalledConfig{Runtime: "claude_code", Path: "/home/u/.claude/settings.json", Kind: "native_config", BeaconManaged: true, Exists: true},
			want:   true,
		},
		{
			// The case that started this: ~/.zshrc exists on every machine with a shell.
			name:   "a shell profile Beacon did not touch does not",
			config: InstalledConfig{Runtime: "copilot_cli", Path: "/home/u/.zshrc", Kind: "profile", Exists: true},
			want:   false,
		},
		{
			name:   "a managed config that does not exist does not",
			config: InstalledConfig{Runtime: "cline", Path: "/home/u/.cline/config.json", Kind: "native_config", BeaconManaged: true, Exists: false},
			want:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := len(InstalledRuntimes([]InstalledConfig{tc.config})) == 1
			if got != tc.want {
				t.Fatalf("InstalledRuntimes(%+v) counted=%v, want %v", tc.config, got, tc.want)
			}
		})
	}
}

// A runtime with several managed rows is one runtime, and one managed row is enough among many
// unmanaged ones.
func TestInstalledRuntimesDeduplicatesAndNeedsOnlyOneManagedRow(t *testing.T) {
	got := InstalledRuntimes([]InstalledConfig{
		{Runtime: "claude_code", Path: "/home/u/.claude/settings.json", Kind: "native_config", BeaconManaged: true, Exists: true},
		{Runtime: "claude_code", Path: "/home/u/.claude.json", Kind: "native_config", BeaconManaged: true, Exists: true},
		{Runtime: "vscode", Path: "/home/u/.config/Code/User/settings.json", Kind: "native_config", Exists: true},
		{Runtime: "vscode", Path: "/home/u/.copilot/hooks/beacon.json", Kind: "hook_config", BeaconManaged: true, Exists: true},
	})
	if len(got) != 2 || got[0] != "claude_code" || got[1] != "vscode" {
		t.Fatalf("InstalledRuntimes = %v, want [claude_code vscode]", got)
	}
}

// The interaction between the two halves of this series. gen_ai.context events reach the shared
// collector -- that is how the utilization report sees them -- so coverage would otherwise count
// one as proof a runtime is reporting its spend. Qwen Code is the live case: it reports how full
// its window was and never what a turn cost, so it must stay not_instrumented with zero usage
// events. Counting occupancy as coverage would mark the one runtime whose spend Beacon cannot
// read as fully covered, which is worse than saying nothing.
func TestCoverageDoesNotCountContextOnlyEventsAsUsage(t *testing.T) {
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "qwen_code", "s1", "qwen3-coder-plus", func(e *schema.Event) {
			e.GenAI.Usage = nil
			e.GenAI.Context = &schema.GenAIContextInfo{
				UsedTokens:  int64Ptr(110100),
				LimitTokens: int64Ptr(262144),
			}
		}),
	}
	report := Coverage(events, []string{"qwen_code"})
	line := lineFor(t, report, "qwen_code")
	if line.Status != CoverageNotInstrumented {
		t.Errorf("status = %q, want not_instrumented", line.Status)
	}
	if line.UsageEvents != 0 {
		t.Errorf("usage events = %d, want 0 -- context occupancy is not spend", line.UsageEvents)
	}
	if line.Tokens != 0 {
		t.Errorf("tokens = %d, want 0", line.Tokens)
	}
	if report.Covered != 0 {
		t.Errorf("covered = %d, want 0", report.Covered)
	}
}

// pollUsageEvent is a token.usage event shaped like the ones the session-store sync commands
// write: collected by poll, carrying a session and canonical usage.
func pollUsageEvent(harness, session string, input, output int64, mutate func(*schema.Event)) schema.Event {
	event := usageEventFixture("2026-06-11T10:05:00Z", harness, session, "claude-opus-4-7", func(e *schema.Event) {
		e.Harness.CollectionMethod = schema.CollectionMethodPoll
		e.GenAI.Usage.InputTokens = int64Ptr(input)
		e.GenAI.Usage.OutputTokens = int64Ptr(output)
	})
	if mutate != nil {
		mutate(&event)
	}
	return event
}

// hookEvent is what a runtime's live hooks write: activity, never usage.
func hookEvent(harness, session string) schema.Event {
	event := plainEvent(harness)
	event.Harness.CollectionMethod = schema.CollectionMethodHook
	event.Session = &schema.SessionInfo{ID: session}
	return event
}

// Some runtimes report usage only through their session-store sync: Hermes Agent's hooks carry
// no token counts, but `beacon endpoint hermes sync` differences the session totals Hermes keeps
// in its state database, cost included; Factory Droid's usage lives in each session's settings
// file, which `beacon endpoint factory sync` reads. Both are runtimes Beacon is built to read
// usage from, so a window with their hook activity and no usage is spend missing from the total
// -- silent, with a note that names the sync -- and their poll-collected usage covers them.
func TestCoverageCountsPollSourcedUsage(t *testing.T) {
	for _, tc := range []struct {
		harness, command string
	}{
		{"hermes", "beacon endpoint hermes sync"},
	} {
		silent := lineFor(t, Coverage([]schema.Event{hookEvent(tc.harness, "s1")}, []string{tc.harness}), tc.harness)
		if silent.Status != CoverageSilent || silent.Expectation != ExpectReported {
			t.Errorf("%s hooks only = %s/%s, want silent/reported -- its usage is read by %s",
				tc.harness, silent.Status, silent.Expectation, tc.command)
		}
		if !strings.Contains(silent.Reason, tc.command) {
			t.Errorf("%s reason = %q, want it to name %q, the only path its usage arrives on",
				tc.harness, silent.Reason, tc.command)
		}

		events := []schema.Event{
			hookEvent(tc.harness, "s1"),
			pollUsageEvent(tc.harness, "s1", 400, 75, func(e *schema.Event) {
				if tc.harness == "hermes" {
					e.GenAI.Usage.CostUSD = float64Ptr(0.25)
				}
			}),
		}
		covered := lineFor(t, Coverage(events, []string{tc.harness}), tc.harness)
		if covered.Status != CoverageCovered || covered.UsageEvents != 1 || covered.Tokens != 475 {
			t.Errorf("%s with synced usage = %+v, want covered with one usage event and 475 tokens", tc.harness, covered)
		}
	}
}
