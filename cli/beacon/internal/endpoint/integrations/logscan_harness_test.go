package integrations

import (
	"os"
	"path/filepath"
	"testing"
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
