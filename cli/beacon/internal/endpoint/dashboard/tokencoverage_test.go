package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
)

// stubInventory makes the installed side of the join deterministic. Without it the test reads the
// machine it runs on, which is how a coverage test can pass with the behaviour it checks reverted.
func stubInventory(t *testing.T, runtimes ...string) {
	t.Helper()
	old := scanInventory
	t.Cleanup(func() { scanInventory = old })
	scanInventory = func(inventory.Options) inventory.Result {
		result := inventory.Result{}
		for _, runtime := range runtimes {
			result.Configs = append(result.Configs, inventory.Config{
				Runtime:       runtime,
				Path:          "/stub/" + runtime + ".json",
				ConfigKind:    "hooks",
				BeaconManaged: true,
				Exists:        true,
			})
		}
		return result
	}
}

func writeCoverageLog(t *testing.T, lines ...string) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return logPath
}

func getCoverage(t *testing.T, logPath, rawQuery string) tokens.CoverageReport {
	t.Helper()
	handler, err := Handler(Options{UserMode: true, LogPath: logPath})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tokens/coverage?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var report tokens.CoverageReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal coverage response: %v", err)
	}
	return report
}

func lineFor(t *testing.T, report tokens.CoverageReport, harness string) tokens.RuntimeCoverage {
	t.Helper()
	for _, runtime := range report.Runtimes {
		if runtime.Harness == harness {
			return runtime
		}
	}
	t.Fatalf("no coverage line for %q in %+v", harness, report.Runtimes)
	return tokens.RuntimeCoverage{}
}

const (
	claudeUsageLine = `{"vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","timestamp":"2026-06-11T10:00:00Z","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"harness":{"name":"claude_code"},"model":"claude-opus-5","session":{"id":"s1"},"gen_ai":{"usage":{"input_tokens":100,"output_tokens":20}}}`
	codexPlainLine  = `{"vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","timestamp":"2026-06-11T10:01:00Z","event":{"kind":"agent_runtime","action":"command.executed","category":"command"},"harness":{"name":"codex_cli"},"session":{"id":"s2"}}`
)

// The route exists to show what the spend rollup cannot: a runtime that ran and reported nothing is
// absent from every total, indistinguishable from one nobody used.
func TestTokensCoverageRouteReportsCoveredAndSilent(t *testing.T) {
	stubInventory(t, "claude_code", "codex_cli")
	report := getCoverage(t, writeCoverageLog(t, claudeUsageLine, codexPlainLine), "")

	if got := lineFor(t, report, "claude_code"); got.Status != tokens.CoverageCovered {
		t.Errorf("claude_code status = %q, want covered", got.Status)
	}
	if got := lineFor(t, report, "codex_cli"); got.Status != tokens.CoverageSilent {
		t.Errorf("codex_cli status = %q, want silent", got.Status)
	}
	if report.Covered != 1 || report.Silent != 1 {
		t.Errorf("covered=%d silent=%d, want 1/1", report.Covered, report.Silent)
	}
}

// The filters the CLI drops are dropped here too. A model filter selects usage-bearing events
// almost by definition, so honoring it would report every runtime covered and hide the silent row
// this route exists to surface.
func TestTokensCoverageRouteIgnoresFiltersThatWouldHideSilence(t *testing.T) {
	stubInventory(t, "claude_code", "codex_cli")
	logPath := writeCoverageLog(t, claudeUsageLine, codexPlainLine)

	unfiltered := getCoverage(t, logPath, "")
	filtered := getCoverage(t, logPath, "model=claude-opus-5&session=s1&repository=/repo")
	if filtered.Silent != unfiltered.Silent || len(filtered.Runtimes) != len(unfiltered.Runtimes) {
		t.Errorf("filtered report = %+v, want the same shape as unfiltered %+v", filtered, unfiltered)
	}
	if got := lineFor(t, filtered, "codex_cli"); got.Status != tokens.CoverageSilent {
		t.Errorf("codex_cli status under a model filter = %q, want silent", got.Status)
	}
}

// A harness scope narrows both sides of the join. Narrowing only the events would leave every
// other installed runtime with none in the filtered set, reporting inactive -- which reads as
// "installed but unused" when the caller only asked about one runtime.
func TestTokensCoverageRouteScopesBothSidesByHarness(t *testing.T) {
	stubInventory(t, "claude_code", "codex_cli")
	report := getCoverage(t, writeCoverageLog(t, claudeUsageLine, codexPlainLine), "harness=claude_code")

	if len(report.Runtimes) != 1 {
		t.Fatalf("runtimes = %+v, want only the scoped one", report.Runtimes)
	}
	if got := lineFor(t, report, "claude_code"); got.Status != tokens.CoverageCovered {
		t.Errorf("claude_code status = %q, want covered", got.Status)
	}
}

// The scoped-installed filter compares against a canonical name, so it has to canonicalize what it
// compares. InstalledRuntimes returns runtime identifiers exactly as the scanner spells them --
// normalization happens inside Coverage, not before it -- so filtering them with a raw comparison
// drops every runtime whose scanner name differs from its harness name.
//
// vscode is that case: it normalizes to vscode_copilot, so a raw match found nothing and the
// runtime vanished from a scoped report rather than appearing as its own row. The first version of
// this test used claude_code, where the two spellings are identical, and passed against the bug.
// This is the third time in this series that a raw-versus-canonical asymmetry has produced a wrong
// row, so the test now uses a name where the two actually differ.
func TestTokensCoverageRouteScopesInstalledByCanonicalName(t *testing.T) {
	stubInventory(t, "vscode")
	report := getCoverage(t, writeCoverageLog(t, claudeUsageLine), "harness=vscode")

	line := lineFor(t, report, "vscode_copilot")
	if !line.Installed {
		t.Errorf("vscode_copilot installed = false, want true; the scanner spells it %q", "vscode")
	}
	if line.Status != tokens.CoverageInactive {
		t.Errorf("status = %q, want inactive -- it is installed and produced no events in scope", line.Status)
	}
}
