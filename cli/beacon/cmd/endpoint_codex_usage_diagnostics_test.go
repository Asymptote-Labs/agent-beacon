package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
)

func TestCodexTokenAttributionCheckReportsTurnUsage(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	line := `{"event":{"action":"token.usage"},"harness":{"name":"codex_cli"},"raw":{"source":"codex_turn_span"}}`
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := codexTokenAttributionCheck(logPath, true)
	if got.Status != diagnostics.StatusOK || got.Evidence != "codex_turn_usage_observed" {
		t.Fatalf("check = %#v", got)
	}
}

func TestCodexTokenAttributionCheckWarnsWhenContextHasNoUsage(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	line := `{"event":{"action":"session.context"},"harness":{"name":"codex_cli"},"raw":{"source":"codex_session_start_hook"}}`
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := codexTokenAttributionCheck(logPath, false)
	if got.Status != diagnostics.StatusWarn ||
		got.Evidence != "codex_session_without_turn_usage" ||
		!strings.Contains(got.Action, "--system") {
		t.Fatalf("check = %#v", got)
	}
}

func TestCodexTokenAttributionCheckIdentifiesLegacyMetrics(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	line := `{"event":{"action":"token.usage"},"harness":{"name":"codex_cli"},"raw":{"metric_name":"codex.turn.token_usage"}}`
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := codexTokenAttributionCheck(logPath, true)
	if got.Status != diagnostics.StatusWarn || got.Evidence != "codex_legacy_usage_metrics" {
		t.Fatalf("check = %#v", got)
	}
}
