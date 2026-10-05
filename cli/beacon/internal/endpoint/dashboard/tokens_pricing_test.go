package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pricing"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
)

// /api/tokens prices with the same overrides file `beacon token-usage` reads by default,
// re-reads it per request, survives an invalid one, and never writes it.
func TestTokensEndpointUsesThePricingOverridesFile(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	// A Codex turn span on gpt-5 ($1.25 in, $10 out list) and an OpenCode step on an internal
	// model the catalog does not know.
	lines := `{"timestamp":"2026-06-11T10:01:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"darwin"},"harness":{"name":"codex_cli","collection_method":"otlp"},"session":{"id":"codex-1"},"model":"gpt-5","gen_ai":{"usage":{"input_tokens":1000,"output_tokens":200}},"message":"codex.turn.token_usage","raw":{"source":"codex_turn_span","turn_id":"turn-1"}}
{"timestamp":"2026-06-11T10:02:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"darwin"},"harness":{"name":"opencode","collection_method":"plugin"},"session":{"id":"oc-1"},"model":"acme-coder-2","gen_ai":{"usage":{"input_tokens":1000,"output_tokens":100}},"message":"opencode.step"}
`
	if err := os.WriteFile(logPath, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(Options{UserMode: true, LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	get := func() tokens.Report {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tokens", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var report tokens.Report
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		return report
	}

	// No file: list prices, no overrides block, the internal model unpriced.
	// gpt-5: 1000 x $1.25 + 200 x $10 = $0.00325.
	report := get()
	if report.Pricing.Overrides != nil || report.Totals.EstimatedCostUSD != 0.00325 || report.Totals.UnpricedEvents != 1 {
		t.Fatalf("no file: totals %+v overrides %+v", report.Totals, report.Pricing.Overrides)
	}

	path := pricing.DefaultOverridesPath(true)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := []byte(`{"schema":"beacon.pricing.overrides/v1","models":{
		"gpt-5":{"input_usd_per_mtok":1,"output_usd_per_mtok":8},
		"acme-coder-2":{"input_usd_per_mtok":2,"output_usd_per_mtok":4}}}`)
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// The file is picked up without restarting the dashboard. gpt-5: 1000 x $1 + 200 x $8 =
	// $0.0026; acme-coder-2: 1000 x $2 + 100 x $4 = $0.0024.
	report = get()
	if o := report.Pricing.Overrides; o == nil || o.Path != path || o.Models != 2 || o.Error != "" {
		t.Fatalf("overrides block = %+v", report.Pricing.Overrides)
	}
	if report.Totals.EstimatedCostUSD != 0.005 || report.Totals.UnpricedEvents != 0 {
		t.Fatalf("with overrides: totals %+v", report.Totals)
	}
	for _, m := range report.Pricing.Models {
		if m.Source != pricing.SourceOverride {
			t.Fatalf("model %s source = %q, want override", m.Model, m.Source)
		}
	}

	// An invalid file degrades to list prices and says why; the view still answers.
	if err := os.WriteFile(path, []byte(`{"schema":"beacon.pricing.overrides/v1","models":{"gpt-5":{"input_usd_per_mtok":-1,"output_usd_per_mtok":8}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report = get()
	if o := report.Pricing.Overrides; o == nil || o.Error == "" || o.Path != path {
		t.Fatalf("invalid file block = %+v", report.Pricing.Overrides)
	}
	if report.Totals.EstimatedCostUSD != 0.00325 {
		t.Fatalf("invalid file: totals %+v", report.Totals)
	}

	// Read-only: restore the valid file and check that serving leaves it byte-identical.
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	get()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, doc) {
		t.Fatal("the dashboard changed the overrides file")
	}
	if testenv.HasPOSIXFileModes() {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != before.Mode().Perm() {
			t.Fatalf("mode changed from %v to %v", before.Mode().Perm(), info.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("pricing dir holds %v (%v), want only the overrides file", entries, err)
	}
}
