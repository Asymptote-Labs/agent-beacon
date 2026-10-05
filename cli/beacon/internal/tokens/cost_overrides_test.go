package tokens

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pricing"
)

// An organisation's overrides file: a negotiated OpenAI rate that publishes no cache-read
// price, an internal model the catalog cannot know with a long-context band, and a gateway
// alias for a catalog model.
const orgOverrides = `{
  "schema": "beacon.pricing.overrides/v1",
  "comment": "Acme 2026 agreement",
  "models": {
    "gpt-5": {"provider": "openai", "input_usd_per_mtok": 1, "output_usd_per_mtok": 8},
    "acme-coder-2": {
      "provider": "acme",
      "input_usd_per_mtok": 1, "output_usd_per_mtok": 4,
      "bands": [{"above_tokens": 200000, "input_usd_per_mtok": 2, "output_usd_per_mtok": 6}]
    }
  },
  "aliases": {"my-gateway-model": "claude-sonnet-4-5"}
}`

func overridesFixtureEvents() []schema.Event {
	return []schema.Event{
		// A Codex turn span (aggregate, base rates): 1000 in, 4000 cache read, 200 out.
		usageEventFixture("2026-06-11T10:00:00Z", "codex_cli", "codex-1", "gpt-5", chain(
			withUsage(1000, 200, 4000, 0, 0, 0), collected("otlp"),
			func(e *schema.Event) { e.Raw = map[string]interface{}{"source": "codex_turn_span"} })),
		// An OpenCode step (one request) on the internal model, past its band threshold.
		usageEventFixture("2026-06-11T10:01:00Z", "opencode", "oc-1", "acme-coder-2", chain(
			withUsage(250_000, 1000, 0, 0, 0, 0), collected("plugin"))),
		// A gateway alias for claude-sonnet-4-5 ($3 in, $15 out).
		usageEventFixture("2026-06-11T10:02:00Z", "gemini_cli", "gw-1", "my-gateway-model", chain(
			withUsage(1000, 100, 0, 0, 0, 0), collected("otlp"))),
		// Nobody prices this one.
		usageEventFixture("2026-06-11T10:03:00Z", "gemini_cli", "gw-2", "mystery-model", withUsage(10, 0, 0, 0, 0, 0)),
	}
}

func TestAggregatePricesWithOverrides(t *testing.T) {
	catalog := pricing.Default()
	events := overridesFixtureEvents()

	// Without the file, the internal model and the alias are unpriced and gpt-5 is list price:
	// 1000 x $1.25 + 4000 x $0.125 + 200 x $10 = 3750 microdollars.
	plain := Aggregate(events, Options{})
	if got := plain.Totals.estimated; got != 3750 {
		t.Fatalf("catalog-only estimate = %d, want 3750", got)
	}
	if plain.Pricing.Overrides != nil || len(plain.Pricing.Unpriced) != 3 {
		t.Fatalf("catalog-only pricing = %+v", plain.Pricing)
	}

	o, err := pricing.LoadOverrides([]byte(orgOverrides), catalog)
	if err != nil {
		t.Fatal(err)
	}
	report := Aggregate(events, Options{Pricer: pricing.NewPricer(catalog, o)})

	// gpt-5 at $1 in (cache reads at the input rate: the row publishes none), $8 out:
	// 1000 + 4000 + 1600 = 6600. acme-coder-2 in its band: 250000 x $2 + 1000 x $6 = 506000.
	// my-gateway-model at claude-sonnet-4-5's list price: 1000 x $3 + 100 x $15 = 4500.
	if got := report.Totals.estimated; got != 6600+506_000+4500 {
		t.Fatalf("estimate = %d, want %d", got, 6600+506_000+4500)
	}
	if got := report.Totals.EffectiveCostUSD; !approxEqual(got, usd(6600+506_000+4500)) {
		t.Fatalf("effective = %v", got)
	}
	if report.Totals.UnpricedEvents != 1 || report.Totals.UnpricedTokens != 10 {
		t.Fatalf("unpriced = %d events %d tokens", report.Totals.UnpricedEvents, report.Totals.UnpricedTokens)
	}

	block := report.Pricing
	if block.Overrides == nil || block.Overrides.SHA256 != o.SHA256() || block.Overrides.Models != 2 || block.Overrides.Aliases != 1 || block.Overrides.Error != "" {
		t.Fatalf("overrides block = %+v", block.Overrides)
	}
	byModel := map[string]ModelPricing{}
	for _, m := range block.Models {
		byModel[m.Model] = m
	}
	if m := byModel["gpt-5"]; m.Source != pricing.SourceOverride || m.Key != "gpt-5" || m.Rates.Input != 1 || m.Rates.CacheRead != 0 ||
		!reflect.DeepEqual(m.Fallbacks, []string{"cache_read_at_input_rate"}) || m.EstimatedCostUSD != usd(6600) {
		t.Fatalf("gpt-5 = %+v", m)
	}
	if m := byModel["acme-coder-2"]; m.Source != pricing.SourceOverride || m.Provider != "acme" ||
		!reflect.DeepEqual(m.BandsApplied, []BandUse{{AboveTokens: 200_000, Requests: 1}}) {
		t.Fatalf("acme-coder-2 = %+v", m)
	}
	if m := byModel["my-gateway-model"]; m.Source != pricing.SourceCatalog || m.Alias != "my-gateway-model" || m.Key != "claude-sonnet-4-5" || m.Rates.Input != 3 {
		t.Fatalf("my-gateway-model = %+v", m)
	}
	if len(block.Unpriced) != 1 || block.Unpriced[0].Model != "mystery-model" {
		t.Fatalf("unpriced = %+v", block.Unpriced)
	}

	var text bytes.Buffer
	RenderText(&text, report)
	for _, want := range []string{
		"or at your overrides file's rate for the models it names",
		"overrides from  (sha256 " + o.SHA256()[:12] + ", 2 model(s), 1 alias(es))",
		"priced by the overrides file: acme-coder-2, gpt-5, my-gateway-model",
		"beacon pricing show <model>",
	} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text missing %q:\n%s", want, text.String())
		}
	}
}

// A reported cost still beats any estimate, however it was priced: an override is a better
// estimate, not a reported figure.
func TestOverridesDoNotDisplaceReportedCost(t *testing.T) {
	catalog := pricing.Default()
	o, err := pricing.LoadOverrides([]byte(`{"schema":"beacon.pricing.overrides/v1","models":{"claude-sonnet-4-5":{"input_usd_per_mtok":1,"output_usd_per_mtok":1}}}`), catalog)
	if err != nil {
		t.Fatal(err)
	}
	events := []schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "claude_code", "s1", "claude-sonnet-4-5", withUsage(1000, 0, 0, 0, 0, 0.25)),
	}
	report := Aggregate(events, Options{Pricer: pricing.NewPricer(catalog, o)})
	if report.Totals.EffectiveCostUSD != 0.25 || report.Totals.CostSource != CostSourceReported || report.Totals.estimated != 1000 {
		t.Fatalf("totals = %+v", report.Totals)
	}
}

func TestOverridesTieIsReportedWithItsSource(t *testing.T) {
	catalog := pricing.Default()
	o, err := pricing.LoadOverrides([]byte(`{"schema":"beacon.pricing.overrides/v1","models":{
		"gw-a/acme-x":{"input_usd_per_mtok":1,"output_usd_per_mtok":1},
		"gw-b/acme-x":{"input_usd_per_mtok":2,"output_usd_per_mtok":2}}}`), catalog)
	if err != nil {
		t.Fatal(err)
	}
	report := Aggregate([]schema.Event{
		usageEventFixture("2026-06-11T10:00:00Z", "gemini_cli", "s1", "acme-x", withUsage(10, 0, 0, 0, 0, 0)),
	}, Options{Pricer: pricing.NewPricer(catalog, o)})
	u := report.Pricing.Unpriced
	if len(u) != 1 || u[0].CandidatesSource != pricing.SourceOverride || !reflect.DeepEqual(u[0].Candidates, []string{"gw-a/acme-x", "gw-b/acme-x"}) {
		t.Fatalf("unpriced = %+v", u)
	}
}
