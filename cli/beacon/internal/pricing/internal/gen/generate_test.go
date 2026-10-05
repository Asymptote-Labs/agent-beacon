package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pricing"
)

var update = flag.Bool("update", false, "rewrite testdata/catalog.golden.json")

const fixtureCommit = "0123456789abcdef0123456789abcdef01234567"

func generateFixture(t *testing.T) ([]byte, report) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "litellm_fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	out, rep, err := generate(raw, sourceInfo(fixtureCommit, "2026-10-01"), "2026-10-02")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return out, rep
}

// The catalog is checked in and reviewed as a diff, so generating twice from the same input
// must give the same bytes, and the bytes must be the golden layout: sorted, one model a line.
func TestGenerateIsDeterministicAndMatchesGolden(t *testing.T) {
	first, _ := generateFixture(t)
	for i := 0; i < 5; i++ {
		again, _ := generateFixture(t)
		if !bytes.Equal(first, again) {
			t.Fatalf("run %d produced different bytes:\n%s\n---\n%s", i, first, again)
		}
	}
	golden := filepath.Join("testdata", "catalog.golden.json")
	if *update {
		if err := os.WriteFile(golden, first, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, want) {
		t.Fatalf("generated catalog differs from %s (rerun with -update after checking):\n%s", golden, first)
	}
}

func TestGenerateSelectionAndRates(t *testing.T) {
	out, rep := generateFixture(t)
	cat, err := pricing.LoadCatalog(out)
	if err != nil {
		t.Fatalf("generated catalog does not load: %v", err)
	}
	src := cat.Source()
	if src.Commit != fixtureCommit || src.FetchedAt != "2026-10-01" || src.License != "MIT" || src.URL != upstreamURL {
		t.Fatalf("source = %+v", src)
	}
	if cat.GeneratedAt() != "2026-10-02" {
		t.Fatalf("generated_at = %q", cat.GeneratedAt())
	}

	wantKeys := []string{
		"claude-sonnet-4-5",
		"deepseek/deepseek-chat",
		"gemini/gemini-2.5-pro",
		"gpt-5-codex",
		"gpt-5.4",
		"mistral/devstral-medium-latest",
		// Not priced by any first-party entry, from a vendor the catalog covers.
		"openrouter/moonshotai/kimi-k2",
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"xai/grok-code-fast-1",
		"zai/glm-4.6",
	}
	if got := cat.Keys(); !reflect.DeepEqual(got, wantKeys) {
		// Left out, each for its own reason: sample_spec; a fine-tune; an audio model; an
		// embedding; a bedrock/<region>/ path; a non-Claude Bedrock model; a subscription
		// product; a reseller; a model with no prices; an openrouter copy of a model Anthropic
		// prices (claude-sonnet-4.5 meets claude-sonnet-4-5 at the dotted-version rung); a
		// :free alias; an openrouter model from a vendor the catalog does not cover.
		t.Fatalf("keys =\n%q\nwant\n%q", got, wantKeys)
	}

	sonnet, _ := cat.Rates("claude-sonnet-4-5")
	wantSonnet := pricing.Rates{
		Provider: "anthropic",
		RateSet:  pricing.RateSet{Input: 3_000_000, Output: 15_000_000, CacheRead: 300_000, CacheWrite: 3_750_000, CacheWrite1h: 6_000_000},
		Bands: []pricing.Band{{AboveTokens: 200_000, RateSet: pricing.RateSet{
			Input: 6_000_000, Output: 22_500_000, CacheRead: 600_000, CacheWrite: 7_500_000, CacheWrite1h: 12_000_000,
		}}},
	}
	if !reflect.DeepEqual(sonnet, wantSonnet) {
		t.Fatalf("claude-sonnet-4-5 = %+v\nwant %+v", sonnet, wantSonnet)
	}

	// Priority, flex and batch fields, including a priority band, are ignored.
	gpt, _ := cat.Rates("gpt-5.4")
	wantGPT := pricing.Rates{
		Provider: "openai",
		RateSet:  pricing.RateSet{Input: 2_500_000, Output: 15_000_000, CacheRead: 250_000},
		Bands:    []pricing.Band{{AboveTokens: 272_000, RateSet: pricing.RateSet{Input: 5_000_000, Output: 22_500_000, CacheRead: 500_000}}},
	}
	if !reflect.DeepEqual(gpt, wantGPT) {
		t.Fatalf("gpt-5.4 = %+v\nwant %+v", gpt, wantGPT)
	}

	// A band that restates only input and output inherits the base cache-read price.
	grok, _ := cat.Rates("xai/grok-code-fast-1")
	wantGrokBand := []pricing.Band{{AboveTokens: 200_000, RateSet: pricing.RateSet{Input: 2_000_000, Output: 4_000_000, CacheRead: 200_000}}}
	if !reflect.DeepEqual(grok.Bands, wantGrokBand) {
		t.Fatalf("xai/grok-code-fast-1 bands = %+v, want %+v", grok.Bands, wantGrokBand)
	}

	// DeepSeek publishes its cache price only as input_cost_per_token_cache_hit, and a zero
	// cache-write price is recorded as unpublished rather than as free.
	ds, _ := cat.Rates("deepseek/deepseek-chat")
	if ds.RateSet != (pricing.RateSet{Input: 280_000, Output: 420_000, CacheRead: 28_000}) {
		t.Fatalf("deepseek/deepseek-chat = %+v", ds.RateSet)
	}
	glm, _ := cat.Rates("zai/glm-4.6")
	if glm.CacheWrite != 0 || glm.CacheRead != 110_000 {
		t.Fatalf("zai/glm-4.6 = %+v", glm.RateSet)
	}

	// The one inexact value is rounded half up and reported, not silently changed.
	wantRounded := []Rounding{{Key: "mistral/devstral-medium-latest", Field: "input_cost_per_token", Value: "4.0000004e-07", Rounded: 400_000}}
	if !reflect.DeepEqual(rep.Rounded, wantRounded) {
		t.Fatalf("rounded = %+v, want %+v", rep.Rounded, wantRounded)
	}
	if rep.Kept != len(wantKeys) {
		t.Fatalf("report kept %d, want %d", rep.Kept, len(wantKeys))
	}
}

func TestPerMillionMicrodollarsIsExact(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		exact   bool
		ok      bool
		comment string
	}{
		{"3e-06", 3_000_000, true, true, "float64 3e-06*1e12 is 2999999.9999999995"},
		{"1.5e-05", 15_000_000, true, true, ""},
		{"6.712e-07", 671_200, true, true, ""},
		{"2.8e-08", 28_000, true, true, ""},
		{"0.000003", 3_000_000, true, true, "plain decimal"},
		{"0", 0, true, true, ""},
		{"1.5e-12", 2, false, true, "1.5 rounds half up"},
		{"2.5e-12", 3, false, true, "2.5 rounds half up, not to even"},
		{"1.49e-12", 1, false, true, ""},
		{"-1e-06", 0, false, false, "negative"},
	}
	for _, c := range cases {
		v, _, exact, ok := perMillionMicrodollars(json.Number(c.in))
		if v != c.want || exact != c.exact || ok != c.ok {
			t.Errorf("%s: got (%d, exact=%v, ok=%v), want (%d, %v, %v) %s", c.in, v, exact, ok, c.want, c.exact, c.ok, c.comment)
		}
	}
}
