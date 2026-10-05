package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A runtime that reports no cost must still get a cost in the report: a list-price estimate,
// labeled as one, beside the runtime-reported figure rather than in place of it.
func TestTokenUsageJSONCarriesListPriceEstimates(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	lines := []string{
		// Claude Code token metrics plus its own cost metric: the reported cost is authoritative.
		`{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"hostname":"mac","os":"darwin"},"harness":{"name":"claude_code","collection_method":"otlp"},"session":{"id":"claude-1"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"input_tokens":100}},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","metric_temporality":"Delta"}}`,
		`{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"hostname":"mac","os":"darwin"},"harness":{"name":"claude_code","collection_method":"otlp"},"session":{"id":"claude-1"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"output_tokens":40}},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","metric_temporality":"Delta"}}`,
		`{"timestamp":"2026-06-11T10:00:05Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"cost.usage","category":"metric"},"severity":"info","endpoint":{"hostname":"mac","os":"darwin"},"harness":{"name":"claude_code","collection_method":"otlp"},"session":{"id":"claude-1"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"cost_usd":0.5}},"message":"claude_code.cost.usage","raw":{"metric_name":"claude_code.cost.usage","metric_temporality":"Delta"}}`,
		// A Codex turn span: Codex reports no cost at all.
		`{"timestamp":"2026-06-11T10:01:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"hostname":"mac","os":"darwin"},"harness":{"name":"codex_cli","collection_method":"otlp"},"session":{"id":"codex-1"},"model":"gpt-5","gen_ai":{"usage":{"input_tokens":1000,"output_tokens":200,"cache_read":{"input_tokens":4000}}},"message":"codex.turn.token_usage","raw":{"source":"codex_turn_span","turn_id":"turn-1"}}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
	output := runTokenUsageCommand(t, "--log-path", logPath, "--json")
	var report map[string]interface{}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("unmarshal JSON report: %v\n%s", err, output)
	}
	totals, _ := report["totals"].(map[string]interface{})
	// Claude: 100 x $3 + 40 x $15 per MTok = $0.0009. Codex gpt-5: 1000 x $1.25 + 4000 x
	// $0.125 + 200 x $10 per MTok = $0.00375. The estimate covers both: $0.00465.
	if got, ok := totals["estimated_cost_usd"].(float64); !ok || got != 0.00465 {
		t.Fatalf("totals.estimated_cost_usd = %#v, want 0.00465\n%s", totals["estimated_cost_usd"], output)
	}
	// Effective: Claude's reported $0.5 replaces its estimate; Codex contributes its estimate.
	if got, ok := totals["effective_cost_usd"].(float64); !ok || got != 0.50375 {
		t.Fatalf("totals.effective_cost_usd = %#v, want 0.50375", totals["effective_cost_usd"])
	}
	if got := totals["cost_source"]; got != "mixed" {
		t.Fatalf("totals.cost_source = %#v, want mixed", got)
	}
	// cost_usd keeps its meaning: what runtimes reported, nothing else.
	if got := totals["cost_usd"]; got != 0.5 {
		t.Fatalf("totals.cost_usd = %#v, want the reported 0.5 only", got)
	}
	pricingBlock, _ := report["pricing"].(map[string]interface{})
	catalog, _ := pricingBlock["catalog"].(map[string]interface{})
	if commit, _ := catalog["commit"].(string); commit == "" || catalog["generated_at"] == nil {
		t.Fatalf("pricing.catalog lacks provenance: %#v", pricingBlock)
	}
}

func TestTokenUsageTextLabelsReportedAndEstimatedCost(t *testing.T) {
	logPath := writeTokensFixtureLog(t)
	output := runTokenUsageCommand(t, "--log-path", logPath)
	for _, want := range []string{
		"COST USD",
		"EST COST USD",
		"COST USD is what the runtimes reported",
		"list price",
		"cache writes are priced at the 5m rate unless the source reported them as 1h writes",
		// 100 input + 40 output tokens of claude-sonnet-4-5 at $3/$15 per MTok.
		"0.0009",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("text report missing %q:\n%s", want, output)
		}
	}
}
