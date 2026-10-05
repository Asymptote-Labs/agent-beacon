package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// overridesCatalog is a small catalog with real list prices, so the override tests are
// hand-checkable and independent of catalog regenerations.
func overridesCatalog(t *testing.T) *Catalog {
	t.Helper()
	return testCatalog(t, map[string]Rates{
		"claude-sonnet-4-5": {Provider: "anthropic",
			RateSet: RateSet{Input: 3_000_000, Output: 15_000_000, CacheRead: 300_000, CacheWrite: 3_750_000, CacheWrite1h: 6_000_000},
			Bands:   []Band{{AboveTokens: 200_000, RateSet: RateSet{Input: 6_000_000, Output: 22_500_000, CacheRead: 600_000, CacheWrite: 7_500_000, CacheWrite1h: 12_000_000}}},
		},
		"claude-sonnet-4-5-20250929": {Provider: "anthropic",
			RateSet: RateSet{Input: 3_000_000, Output: 15_000_000, CacheRead: 300_000, CacheWrite: 3_750_000},
		},
		"gpt-5": {Provider: "openai", RateSet: RateSet{Input: 1_250_000, Output: 10_000_000, CacheRead: 125_000}},
	})
}

func mustOverrides(t *testing.T, c *Catalog, doc string) *Overrides {
	t.Helper()
	o, err := LoadOverrides([]byte(doc), c)
	if err != nil {
		t.Fatalf("LoadOverrides: %v", err)
	}
	return o
}

func TestOverridesConvertDecimalRatesExactly(t *testing.T) {
	c := overridesCatalog(t)
	// Values a float64 cannot hold exactly (0.3, 0.1+0.2-style sums) must still land on the
	// exact microdollar, and the exponent form is accepted.
	o := mustOverrides(t, c, `{
		"schema": "beacon.pricing.overrides/v1",
		"models": {
			"acme-internal-1": {
				"provider": "acme",
				"input_usd_per_mtok": 0.3,
				"output_usd_per_mtok": 2.4e0,
				"cache_read_usd_per_mtok": 0.030000,
				"cache_write_usd_per_mtok": 0.375,
				"cache_write_1h_usd_per_mtok": 0.000001
			}
		}
	}`)
	got, ok := o.Rates("acme-internal-1")
	if !ok {
		t.Fatal("row missing")
	}
	want := Rates{Provider: "acme", RateSet: RateSet{Input: 300_000, Output: 2_400_000, CacheRead: 30_000, CacheWrite: 375_000, CacheWrite1h: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rates = %+v, want %+v", got, want)
	}
}

func TestOverridesRejectBadValuesNamingTheKey(t *testing.T) {
	c := overridesCatalog(t)
	row := func(fields string) string {
		return `{"schema":"beacon.pricing.overrides/v1","models":{"m":{` + fields + `}}}`
	}
	cases := []struct {
		name, doc, want string
	}{
		{"negative", row(`"input_usd_per_mtok":-1,"output_usd_per_mtok":2`), `models["m"].input_usd_per_mtok: -1 is negative`},
		{"zero", row(`"input_usd_per_mtok":1,"output_usd_per_mtok":2,"cache_read_usd_per_mtok":0`), `models["m"].cache_read_usd_per_mtok: 0 is not a price`},
		{"too precise", row(`"input_usd_per_mtok":0.0000001,"output_usd_per_mtok":2`), `models["m"].input_usd_per_mtok: 0.0000001 is finer than one microdollar`},
		{"absurd", row(`"input_usd_per_mtok":3,"output_usd_per_mtok":100000.000001`), `models["m"].output_usd_per_mtok: 100000.000001 is above the 100000 USD`},
		{"huge exponent", row(`"input_usd_per_mtok":1e999999,"output_usd_per_mtok":2`), `models["m"].input_usd_per_mtok: 1e999999 is out of range`},
		{"quoted", row(`"input_usd_per_mtok":"3.00","output_usd_per_mtok":2`), `models["m"].input_usd_per_mtok: "3.00" is not a JSON number`},
		{"NaN string", row(`"input_usd_per_mtok":"NaN","output_usd_per_mtok":2`), `models["m"].input_usd_per_mtok: "NaN" is not a JSON number`},
		{"bare NaN", row(`"input_usd_per_mtok":NaN,"output_usd_per_mtok":2`), `parse:`},
		{"missing output", row(`"input_usd_per_mtok":1`), `models["m"].output_usd_per_mtok: required`},
		{"misspelled field", row(`"input_usd_per_mtok":1,"output_usd_per_mtok":2,"cache_read_usd_per_mtk":1`), `unknown field "cache_read_usd_per_mtk"`},
		{"duplicate key", row(`"input_usd_per_mtok":1,"output_usd_per_mtok":2,"input_usd_per_mtok":3`), `models["m"]: key "input_usd_per_mtok" appears twice`},
		{"duplicate model", `{"schema":"beacon.pricing.overrides/v1","models":{"m":{"input_usd_per_mtok":1,"output_usd_per_mtok":2},"m":{"input_usd_per_mtok":3,"output_usd_per_mtok":4}}}`, `models: key "m" appears twice`},
		{"band threshold", row(`"input_usd_per_mtok":1,"output_usd_per_mtok":2,"bands":[{"above_tokens":1.5,"input_usd_per_mtok":2}]`), `models["m"].bands[0].above_tokens: 1.5 is not a positive whole number`},
		{"band missing threshold", row(`"input_usd_per_mtok":1,"output_usd_per_mtok":2,"bands":[{"input_usd_per_mtok":2}]`), `models["m"].bands[0].above_tokens: required`},
		{"band duplicate", row(`"input_usd_per_mtok":1,"output_usd_per_mtok":2,"bands":[{"above_tokens":10,"input_usd_per_mtok":2},{"above_tokens":10,"input_usd_per_mtok":3}]`), `models["m"].bands: two bands above 10 tokens`},
		{"band bad rate", row(`"input_usd_per_mtok":1,"output_usd_per_mtok":2,"bands":[{"above_tokens":10,"output_usd_per_mtok":-2}]`), `models["m"].bands[0].output_usd_per_mtok: -2 is negative`},
		{"wrong schema", `{"schema":"beacon.pricing.overrides/v2","models":{}}`, `schema "beacon.pricing.overrides/v2"`},
		{"no schema", `{"models":{}}`, `schema ""`},
		{"blank name", `{"schema":"beacon.pricing.overrides/v1","models":{" ":{"input_usd_per_mtok":1,"output_usd_per_mtok":2}}}`, `models[" "]: the name is empty`},
		{"padded name", `{"schema":"beacon.pricing.overrides/v1","models":{"m ":{"input_usd_per_mtok":1,"output_usd_per_mtok":2}}}`, `leading or trailing white space`},
		{"alias to nothing", `{"schema":"beacon.pricing.overrides/v1","aliases":{"gw":"claude-sonnet-9"}}`, `aliases["gw"]: target "claude-sonnet-9" is neither a model in this file nor a catalog key`},
		{"alias is a model", `{"schema":"beacon.pricing.overrides/v1","models":{"m":{"input_usd_per_mtok":1,"output_usd_per_mtok":2}},"aliases":{"m":"gpt-5"}}`, `aliases["m"]: "m" is also a model in this file`},
		{"alias loose target", `{"schema":"beacon.pricing.overrides/v1","aliases":{"gw":"Claude-Sonnet-4-5"}}`, `target "Claude-Sonnet-4-5" is neither`},
		{"trailing data", `{"schema":"beacon.pricing.overrides/v1"} {}`, `trailing data`},
		// A stray closer after a complete document: the decoder's More() reports false in front
		// of it, so only an explicit end-of-input check refuses it.
		{"trailing bracket", `{"schema":"beacon.pricing.overrides/v1"}]`, `trailing data`},
		{"trailing brace", `{"schema":"beacon.pricing.overrides/v1"}}`, `trailing data`},
		{"lone bracket", `]`, `parse`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadOverrides([]byte(tc.doc), c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestOverridesWinOverTheCatalogAtEveryRung(t *testing.T) {
	c := overridesCatalog(t)
	o := mustOverrides(t, c, `{
		"schema": "beacon.pricing.overrides/v1",
		"comment": "Acme enterprise agreement, 2026",
		"models": {
			"claude-sonnet-4-5": {"input_usd_per_mtok": 2.4, "output_usd_per_mtok": 12, "comment": "20% off list"}
		}
	}`)
	p := NewPricer(c, o)
	// The catalog has an exact row for the dated snapshot, but the negotiated rate for the
	// model is what the organisation pays for its snapshots too.
	for _, model := range []string{"claude-sonnet-4-5", "anthropic/claude-sonnet-4-5", "claude-sonnet-4.5", "claude-sonnet-4-5-20250929", "claude-sonnet-4-5[1m]"} {
		res, ok := p.Lookup(model)
		if !ok || res.Source != SourceOverride || res.Key != "claude-sonnet-4-5" || res.Rates.Input != 2_400_000 {
			t.Fatalf("%s: %+v ok=%v, want the override", model, res, ok)
		}
	}
	// The override row replaces the catalog row whole: its omitted cache rates are
	// unpublished, not inherited, and its bands are its own (none here).
	res, _ := p.Lookup("claude-sonnet-4-5")
	if res.Rates.CacheRead != 0 || res.Rates.CacheWrite != 0 || len(res.Rates.Bands) != 0 {
		t.Fatalf("override inherited catalog fields: %+v", res.Rates)
	}
	est := Cost(res.Rates, Tokens{Input: 1_000_000, CacheRead: 1_000_000, CacheCreation: 1_000_000, Output: 1_000_000}, true)
	if est.Cost != 2_400_000*3+12_000_000 || !est.CacheReadAtInputRate || !est.CacheWriteAtInputRate || est.BandAboveTokens != 0 {
		t.Fatalf("estimate = %+v", est)
	}
	// Models the file does not name still come from the catalog.
	if res, ok := p.Lookup("gpt-5"); !ok || res.Source != SourceCatalog || res.Key != "gpt-5" {
		t.Fatalf("gpt-5 = %+v ok=%v", res, ok)
	}
	if got := o.ModelComment("claude-sonnet-4-5"); got != "20% off list" {
		t.Fatalf("comment = %q", got)
	}
	if o.Comment() != "Acme enterprise agreement, 2026" {
		t.Fatalf("file comment = %q", o.Comment())
	}
}

func TestOverridesPriceAModelTheCatalogDoesNotKnow(t *testing.T) {
	c := overridesCatalog(t)
	if _, ok := c.Lookup("acme-coder-2"); ok {
		t.Fatal("fixture model is in the catalog")
	}
	p := NewPricer(c, mustOverrides(t, c, `{"schema":"beacon.pricing.overrides/v1","models":{"acme-coder-2":{"input_usd_per_mtok":1,"output_usd_per_mtok":4}}}`))
	res, ok := p.Lookup("acme/acme-coder-2-20261001")
	if !ok || res.Key != "acme-coder-2" || res.Source != SourceOverride || res.Match != MatchExact || !reflect.DeepEqual(res.Stripped, []string{StrippedDateSnapshot}) {
		t.Fatalf("got %+v ok=%v", res, ok)
	}
}

func TestOverridesAliases(t *testing.T) {
	c := overridesCatalog(t)
	o := mustOverrides(t, c, `{
		"schema": "beacon.pricing.overrides/v1",
		"models": {"acme-coder-2": {"input_usd_per_mtok": 1, "output_usd_per_mtok": 4}},
		"aliases": {
			"my-gateway-model": "claude-sonnet-4-5",
			"acme-fast": "acme-coder-2",
			"gpt-5": "acme-coder-2"
		}
	}`)
	p := NewPricer(c, o)
	cases := []struct {
		model, key, source, alias string
		input                     int64
		match                     Match
	}{
		// An alias to a catalog key is priced by the catalog row, and says it went through the
		// alias.
		{"my-gateway-model", "claude-sonnet-4-5", SourceCatalog, "my-gateway-model", 3_000_000, MatchExact},
		// The alias itself is matched with the full ladder.
		{"litellm_proxy/My-Gateway-Model", "claude-sonnet-4-5", SourceCatalog, "my-gateway-model", 3_000_000, MatchCanonical},
		{"acme-fast", "acme-coder-2", SourceOverride, "acme-fast", 1_000_000, MatchExact},
		// An alias may claim a catalog name and send it elsewhere.
		{"gpt-5", "acme-coder-2", SourceOverride, "gpt-5", 1_000_000, MatchExact},
	}
	for _, tc := range cases {
		res, ok := p.Lookup(tc.model)
		if !ok || res.Key != tc.key || res.Source != tc.source || res.Alias != tc.alias || res.Rates.Input != tc.input || res.Match != tc.match {
			t.Fatalf("%s: got %+v ok=%v, want key %s source %s alias %s", tc.model, res, ok, tc.key, tc.source, tc.alias)
		}
	}
	// A catalog alias carries the catalog's bands.
	res, _ := p.Lookup("my-gateway-model")
	if len(res.Rates.Bands) != 1 {
		t.Fatalf("alias lost the catalog bands: %+v", res.Rates)
	}
	if target, source, ok := o.AliasTarget("my-gateway-model"); !ok || target != "claude-sonnet-4-5" || source != SourceCatalog {
		t.Fatalf("AliasTarget = %q %q %v", target, source, ok)
	}
	if got, want := o.Aliases(), []string{"acme-fast", "gpt-5", "my-gateway-model"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Aliases = %v", got)
	}
}

func TestOverridesBands(t *testing.T) {
	c := overridesCatalog(t)
	// Bands may be listed in any order and need restate only what changes; the rest is the
	// row's base rate.
	o := mustOverrides(t, c, `{
		"schema": "beacon.pricing.overrides/v1",
		"models": {"acme-long": {
			"input_usd_per_mtok": 1, "output_usd_per_mtok": 4, "cache_read_usd_per_mtok": 0.1,
			"bands": [
				{"above_tokens": 1e6, "input_usd_per_mtok": 4},
				{"above_tokens": 200000, "input_usd_per_mtok": 2, "output_usd_per_mtok": 6}
			]
		}}
	}`)
	got, _ := o.Rates("acme-long")
	want := []Band{
		{AboveTokens: 200_000, RateSet: RateSet{Input: 2_000_000, Output: 6_000_000, CacheRead: 100_000}},
		{AboveTokens: 1_000_000, RateSet: RateSet{Input: 4_000_000, Output: 4_000_000, CacheRead: 100_000}},
	}
	if !reflect.DeepEqual(got.Bands, want) {
		t.Fatalf("bands = %+v, want %+v", got.Bands, want)
	}
	p := NewPricer(c, o)
	res, _ := p.Lookup("acme-long")
	est := Cost(res.Rates, Tokens{Input: 300_000, Output: 1_000}, true)
	if est.BandAboveTokens != 200_000 || est.Cost != 300_000*2+1_000*6 {
		t.Fatalf("banded estimate = %+v", est)
	}
	if est := Cost(res.Rates, Tokens{Input: 300_000, Output: 1_000}, false); est.BandAboveTokens != 0 || est.Cost != 300_000+4_000 {
		t.Fatalf("aggregate estimate = %+v", est)
	}
}

func TestOverridesTieIsUnpricedAndDoesNotFallThrough(t *testing.T) {
	c := overridesCatalog(t)
	o := mustOverrides(t, c, `{
		"schema": "beacon.pricing.overrides/v1",
		"models": {
			"gw-a/gpt-5": {"input_usd_per_mtok": 1, "output_usd_per_mtok": 2},
			"gw-b/gpt-5": {"input_usd_per_mtok": 3, "output_usd_per_mtok": 4}
		}
	}`)
	p := NewPricer(c, o)
	res, ok := p.Lookup("gpt-5")
	if ok || res.Source != SourceOverride || !reflect.DeepEqual(res.Candidates, []string{"gw-a/gpt-5", "gw-b/gpt-5"}) {
		t.Fatalf("got %+v ok=%v, want an unpriced override tie", res, ok)
	}
	// The exact names still price.
	if res, ok := p.Lookup("gw-b/gpt-5"); !ok || res.Rates.Input != 3_000_000 {
		t.Fatalf("exact = %+v ok=%v", res, ok)
	}
	warnings := o.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], `"gw-a/gpt-5", "gw-b/gpt-5"`) || !strings.Contains(warnings[0], "canonical") {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestOverridesEmptyFileIsValid(t *testing.T) {
	c := overridesCatalog(t)
	o := mustOverrides(t, c, `{"schema":"beacon.pricing.overrides/v1"}`)
	p := NewPricer(c, o)
	if res, ok := p.Lookup("gpt-5"); !ok || res.Source != SourceCatalog {
		t.Fatalf("got %+v ok=%v", res, ok)
	}
	if len(o.Models()) != 0 || len(o.Aliases()) != 0 || len(o.Warnings()) != 0 {
		t.Fatalf("empty file has content")
	}
}

func TestLoadPricerMissingAndInvalidFiles(t *testing.T) {
	c := overridesCatalog(t)
	dir := t.TempDir()
	missing := filepath.Join(dir, "pricing", OverridesFileName)

	// The default location usually holds nothing; that is not an error.
	p, err := LoadPricer(c, missing, false)
	if err != nil || p.Overrides() != nil {
		t.Fatalf("missing default: %v %v", p.Overrides(), err)
	}
	if _, oerr := p.OverridesError(); oerr != nil {
		t.Fatalf("missing default recorded an error: %v", oerr)
	}
	// A file the user named must exist.
	if _, err := LoadPricer(c, missing, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing required: %v", err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"schema":"beacon.pricing.overrides/v1","models":{"m":{"input_usd_per_mtok":-1,"output_usd_per_mtok":1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err = LoadPricer(c, bad, false)
	if err == nil || !strings.Contains(err.Error(), bad) || !strings.Contains(err.Error(), `models["m"].input_usd_per_mtok`) {
		t.Fatalf("invalid: %v", err)
	}
	// The pricer still prices from the catalog and says why it is not using the file.
	if path, oerr := p.OverridesError(); path != bad || oerr == nil {
		t.Fatalf("OverridesError = %q %v", path, oerr)
	}
	if res, ok := p.Lookup("gpt-5"); !ok || res.Source != SourceCatalog {
		t.Fatalf("fallback lookup = %+v", res)
	}

	good := filepath.Join(dir, "good.json")
	doc := []byte(`{"schema":"beacon.pricing.overrides/v1","models":{"m":{"input_usd_per_mtok":1,"output_usd_per_mtok":1}}}`)
	if err := os.WriteFile(good, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err = LoadPricer(c, good, true)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	if o := p.Overrides(); o.Path() != good || o.SHA256() != hex.EncodeToString(sum[:]) {
		t.Fatalf("path %q digest %q", o.Path(), o.SHA256())
	}

	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, maxOverridesFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPricer(c, big, true); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized: %v", err)
	}
}

func TestDefaultOverridesPathIsUnderTheEndpointBaseDir(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	if got, want := DefaultOverridesPath(true), filepath.Join(home, ".beacon", "endpoint", "pricing", "overrides.json"); got != want {
		t.Fatalf("user path = %q, want %q", got, want)
	}
	if got := DefaultOverridesPath(false); !strings.HasSuffix(got, filepath.Join("pricing", "overrides.json")) || strings.HasPrefix(got, home) {
		t.Fatalf("system path = %q", got)
	}
}

// The digest and every resolution are a function of the file's bytes alone: loading the same
// document twice resolves the same way, whatever map order Go picks.
func TestOverridesAreDeterministic(t *testing.T) {
	c := overridesCatalog(t)
	doc := `{"schema":"beacon.pricing.overrides/v1","models":{"a/x":{"input_usd_per_mtok":1,"output_usd_per_mtok":1},"b/x":{"input_usd_per_mtok":1,"output_usd_per_mtok":1}},"aliases":{"y":"a/x","z":"gpt-5"}}`
	first := mustOverrides(t, c, doc)
	for i := 0; i < 20; i++ {
		again := mustOverrides(t, c, doc)
		if again.SHA256() != first.SHA256() {
			t.Fatal("digest moved")
		}
		a, _ := NewPricer(c, first).Lookup("x")
		b, _ := NewPricer(c, again).Lookup("x")
		if !reflect.DeepEqual(a, b) || a.Key != "a/x" {
			t.Fatalf("resolution moved: %+v vs %+v", a, b)
		}
	}
}
