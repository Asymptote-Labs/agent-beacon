package integrations

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Discovery and telemetry use different vocabularies for the same runtime: `--harness vscode` is
// the CLI surface, `vscode_copilot` is what its events carry. Comparing them literally asks
// whether two unrelated naming schemes happen to coincide, and answers "no" for a runtime that is
// working -- so doctor's harness_observed never clears and the operator is told to run an agent
// they have already been running.
func TestHarnessEventsMatchAcrossNamingSchemes(t *testing.T) {
	for name, tc := range map[string]struct{ discovery, event string }{
		"vscode discovery vs canonical event":      {"vscode", "vscode_copilot"},
		"claude_code discovery vs raw hook event":  {"claude_code", "claude"},
		"codex_cli discovery vs raw hook event":    {"codex_cli", "codex"},
		"identical names still match":              {"claude_code", "claude_code"},
		"case and spacing are not load-bearing":    {"Claude_Code", "  claude_code  "},
		"antigravity discovery vs canonical event": {"antigravity", "antigravity_cli"},
	} {
		t.Run(name, func(t *testing.T) {
			log := writeLog(t, `{"timestamp":"2026-08-12T10:00:00Z","harness":{"name":"`+tc.event+`"}}`)
			if !HasRecentHarnessEvent(log, tc.discovery) {
				t.Errorf("an event from %q did not match discovery name %q, so a working runtime "+
					"reads as never observed", tc.event, tc.discovery)
			}
		})
	}
}

// Normalizing must not make everything match everything. A runtime that genuinely has no events
// has to keep reporting that, or the check stops meaning anything.
func TestUnrelatedHarnessesStillDoNotMatch(t *testing.T) {
	log := writeLog(t, `{"timestamp":"2026-08-12T10:00:00Z","harness":{"name":"claude_code"}}`)
	for _, other := range []string{"vscode", "codex_cli", "gemini_cli", "cursor", "copilot_cli"} {
		if HasRecentHarnessEvent(log, other) {
			t.Errorf("a claude_code event matched %q; the check would report every runtime as observed", other)
		}
	}
}

// VS Code Copilot Chat and the Copilot CLI are different products. Collapsing them would report
// one as observed because the other ran.
func TestVSCodeAndCopilotCLIRemainDistinct(t *testing.T) {
	vscodeLog := writeLog(t, `{"timestamp":"2026-08-12T10:00:00Z","harness":{"name":"vscode_copilot"}}`)
	if HasRecentHarnessEvent(vscodeLog, "copilot_cli") {
		t.Error("a VS Code event matched the Copilot CLI, which is a different product")
	}
	cliLog := writeLog(t, `{"timestamp":"2026-08-12T10:00:00Z","harness":{"name":"copilot_cli"}}`)
	if HasRecentHarnessEvent(cliLog, "vscode") {
		t.Error("a Copilot CLI event matched VS Code, which is a different product")
	}
}

func TestScanCodexUsageSignalsSeparatesAttributionSources(t *testing.T) {
	log := writeLog(t,
		`{"event":{"action":"session.context"},"harness":{"name":"codex"},"raw":{"source":"codex_session_start_hook"}}`,
		`{"event":{"action":"token.usage"},"harness":{"name":"codex_cli"},"raw":{"source":"codex_turn_span","turn_id":"turn-1"}}`,
		`{"event":{"action":"token.usage"},"harness":{"name":"codex_cli"},"raw":{"metric_name":"codex.turn.token_usage"}}`,
		`{"event":{"action":"token.usage"},"harness":{"name":"claude_code"},"raw":{"metric_name":"claude_code.token.usage"}}`,
	)

	got := ScanCodexUsageSignals(log)
	if got.SessionContexts != 1 || got.TurnSpans != 1 || got.LegacyMetrics != 1 {
		t.Fatalf("signals = %#v, want one of each Codex source", got)
	}
}

// DeepSeek Harness writes deepseek_harness events from two paths: live hooks and the offline poll
// of its session store. A log holding only poll events must not read as live hook capture, because
// that is exactly the log a sandboxed dsh produces: every hook fails to write and `dsh sync` fills
// the log anyway (#605).
func TestLastHarnessEventByCollectionMethodIgnoresOtherPaths(t *testing.T) {
	log := writeLog(t,
		`{"timestamp":"2026-09-21T10:00:00Z","harness":{"name":"deepseek_harness","collection_method":"hook"}}`,
		`{"timestamp":"2026-09-21T12:00:00Z","harness":{"name":"deepseek_harness","collection_method":"poll"}}`,
		`{"timestamp":"2026-09-21T13:00:00Z","harness":{"name":"claude","collection_method":"hook"}}`,
	)
	got, ok := LastHarnessEventByCollectionMethod(log, "dsh", "hook")
	if !ok {
		t.Fatal("no hook event found, want the 10:00 one")
	}
	if want := "2026-09-21T10:00:00Z"; got.UTC().Format("2006-01-02T15:04:05Z") != want {
		t.Fatalf("last hook event = %s, want %s: a poll event or another harness's hook was counted", got, want)
	}

	pollOnly := writeLog(t, `{"timestamp":"2026-09-21T12:00:00Z","harness":{"name":"deepseek_harness","collection_method":"poll"}}`)
	if _, ok := LastHarnessEventByCollectionMethod(pollOnly, "deepseek_harness", "hook"); ok {
		t.Fatal("a poll-only log reported a hook event")
	}
	if !HasRecentHarnessEvent(pollOnly, "deepseek_harness") {
		t.Fatal("the unfiltered lookup must still count poll events")
	}
	if _, ok := LastHarnessEventByCollectionMethod(log, "deepseek_harness", ""); ok {
		t.Fatal("an empty method must not match everything; callers use LastHarnessEvent for that")
	}
}

func TestCollectionMethodFiltersKeepLegacyAndGeneralReadersDistinct(t *testing.T) {
	path := writeLog(t,
		`{"timestamp":"2026-10-13T10:00:00Z","harness":{"name":"openclaw_gateway","collection_method":"otlp"}}`,
		`{"timestamp":"2026-10-13T11:00:00Z","harness":{"name":"openclaw_gateway"}}`,
		`{"timestamp":"2026-10-13T12:00:00Z","harness":{"name":"openclaw_gateway","collection_method":"plugin"}}`,
	)
	all, ok := LastHarnessEvent(path, "openclaw")
	if !ok || all.Format(time.RFC3339) != "2026-10-13T12:00:00Z" {
		t.Fatalf("general reader = %s, %v; want newest plugin event", all, ok)
	}
	strict, ok := LastHarnessEventByCollectionMethod(path, "openclaw", "otlp")
	if !ok || strict.Format(time.RFC3339) != "2026-10-13T10:00:00Z" {
		t.Fatalf("strict reader = %s, %v; want marked OTLP only", strict, ok)
	}
	legacy, ok := LastHarnessEventByCollectionMethods(path, "openclaw", "otlp", "")
	if !ok || legacy.Format(time.RFC3339) != "2026-10-13T11:00:00Z" {
		t.Fatalf("legacy-compatible reader = %s, %v; want latest OTLP-compatible event", legacy, ok)
	}
	if _, ok := LastHarnessEventByCollectionMethod(path, "openclaw", ""); ok {
		t.Fatal("the existing strict helper must continue rejecting an empty filter")
	}
	if _, ok := LastHarnessEventByCollectionMethods(path, "openclaw"); ok {
		t.Fatal("an empty explicit filter list must not become a wildcard")
	}
}
