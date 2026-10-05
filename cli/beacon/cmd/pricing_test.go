package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pricing"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// A file shaped like an organisation's own: a negotiated rate replacing a catalog row, an
// internal model with a long-context band, and a gateway alias.
const testOverridesDoc = `{
  "schema": "beacon.pricing.overrides/v1",
  "comment": "Acme enterprise agreement",
  "models": {
    "claude-sonnet-4-5": {
      "provider": "anthropic",
      "comment": "20% off list",
      "input_usd_per_mtok": 2.4, "output_usd_per_mtok": 12, "cache_read_usd_per_mtok": 0.24,
      "bands": [{"above_tokens": 200000, "input_usd_per_mtok": 4.8, "output_usd_per_mtok": 18}]
    },
    "acme-coder-2": {"provider": "acme", "input_usd_per_mtok": 1, "output_usd_per_mtok": 4}
  },
  "aliases": {"my-gateway-model": "gpt-5"}
}`

// runPricing executes `beacon pricing ...` through the root command with fresh flag values and
// an isolated home, so no real overrides file can leak in.
func runPricing(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	pricingOpts.userMode, pricingOpts.systemMode = true, false
	pricingOpts.pricingFile, pricingOpts.provider, pricingOpts.json = "", "", false
	var reset func(*cobra.Command)
	reset = func(c *cobra.Command) {
		for _, fs := range []*pflag.FlagSet{c.Flags(), c.PersistentFlags()} {
			fs.VisitAll(func(f *pflag.Flag) {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			})
		}
		for _, sub := range c.Commands() {
			reset(sub)
		}
	}
	reset(pricingCmd)
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(append([]string{"pricing"}, args...))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err = rootCmd.Execute()
	return out.String(), errOut.String(), err
}

func writeOverrides(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "overrides.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPricingCommandTree(t *testing.T) {
	for _, sub := range []string{"show", "list", "info", "validate"} {
		cmd, _, err := rootCmd.Find([]string{"pricing", sub})
		if err != nil || cmd.Name() != sub {
			t.Fatalf("pricing %s not registered: %v", sub, err)
		}
		for _, flag := range []string{"pricing-file", "json", "user", "system"} {
			if cmd.Flags().Lookup(flag) == nil && cmd.InheritedFlags().Lookup(flag) == nil {
				t.Fatalf("pricing %s missing --%s", sub, flag)
			}
		}
	}
	if cmd, _, _ := rootCmd.Find([]string{"token-usage"}); cmd.Flags().Lookup("pricing-file") == nil {
		t.Fatal("token-usage missing --pricing-file")
	}
}

func TestPricingShowCatalogModelText(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	out, _, err := runPricing(t, "show", "us.anthropic.claude-sonnet-4-5-20250929-v1:0[1m]")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Priced by:  catalog key "us.anthropic.claude-sonnet-4-5-20250929-v1:0"`,
		"Match:      exact after stripping context_window",
		"input             3.30",
		"Catalog:    LiteLLM",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show missing %q:\n%s", want, out)
		}
	}
}

func TestPricingShowOverrideJSON(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	path := writeOverrides(t, testOverridesDoc)
	out, _, err := runPricing(t, "show", "claude-sonnet-4-5-20250929", "--pricing-file", path, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got pricingShowResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// The catalog has an exact row for the dated snapshot; the override for the model wins.
	if !got.Priced || got.Source != pricing.SourceOverride || got.Key != "claude-sonnet-4-5" || got.Match != "exact" ||
		len(got.Stripped) != 1 || got.Stripped[0] != pricing.StrippedDateSnapshot {
		t.Fatalf("resolution = %+v", got)
	}
	if got.Rates == nil || got.Rates.Input != 2.4 || got.Rates.CacheRead != 0.24 || got.Rates.CacheWrite != 0 {
		t.Fatalf("rates = %+v", got.Rates)
	}
	// The band restates input and output and inherits the row's cache-read rate.
	if len(got.Bands) != 1 || got.Bands[0].AboveTokens != 200_000 || got.Bands[0].Rates.Input != 4.8 || got.Bands[0].Rates.CacheRead != 0.24 {
		t.Fatalf("bands = %+v", got.Bands)
	}
	if strings.Join(got.Fallbacks, ",") != "cache_write_at_input_rate,cache_write_1h_at_write_rate" {
		t.Fatalf("fallbacks = %v", got.Fallbacks)
	}
	if got.Overrides == nil || got.Overrides.Path != path || len(got.Overrides.SHA256) != 64 {
		t.Fatalf("overrides = %+v", got.Overrides)
	}

	// An alias through to a catalog key.
	out, _, err = runPricing(t, "show", "My-Gateway-Model", "--pricing-file", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Priced by:  catalog key "gpt-5"`, "Alias:      my-gateway-model -> gpt-5", "Match:      canonical", "Overrides:  " + path} {
		if !strings.Contains(out, want) {
			t.Fatalf("alias show missing %q:\n%s", want, out)
		}
	}
	// An override row shows its comment and that it replaces a catalog entry.
	out, _, _ = runPricing(t, "show", "claude-sonnet-4-5", "--pricing-file", path)
	for _, want := range []string{`override "claude-sonnet-4-5"`, "Comment:    20% off list", "replaces the catalog entry of the same name"} {
		if !strings.Contains(out, want) {
			t.Fatalf("override show missing %q:\n%s", want, out)
		}
	}
}

func TestPricingShowUnpriced(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	path := writeOverrides(t, `{"schema":"beacon.pricing.overrides/v1","models":{
		"gw-a/acme-x":{"input_usd_per_mtok":1,"output_usd_per_mtok":1},
		"gw-b/acme-x":{"input_usd_per_mtok":2,"output_usd_per_mtok":2}}}`)
	out, _, err := runPricing(t, "show", "acme-x", "--pricing-file", path, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got pricingShowResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Priced || got.CandidatesSource != pricing.SourceOverride || strings.Join(got.Candidates, ",") != "gw-a/acme-x,gw-b/acme-x" || got.Rates != nil {
		t.Fatalf("tie = %+v", got)
	}
	out, _, _ = runPricing(t, "show", "nobody-prices-this")
	if !strings.Contains(out, "Not priced: no catalog key or override matches it") {
		t.Fatalf("unpriced text:\n%s", out)
	}
}

func TestPricingListMarksOverrides(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	path := writeOverrides(t, testOverridesDoc)
	out, _, err := runPricing(t, "list", "--provider", "anthropic", "--pricing-file", path, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Models  []pricingListEntry  `json:"models"`
		Aliases []pricingAliasEntry `json:"aliases"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) < 2 || got.Models[0].Key != "claude-sonnet-4-5" || got.Models[0].Source != pricing.SourceOverride {
		t.Fatalf("first entry = %+v", got.Models[0])
	}
	var overridden, other bool
	for _, m := range got.Models {
		if m.Provider != "anthropic" {
			t.Fatalf("provider filter let %+v through", m)
		}
		if m.Source == pricing.SourceCatalog && m.Key == "claude-sonnet-4-5" {
			overridden = m.Overridden
		}
		if m.Key == "acme-coder-2" {
			other = true
		}
	}
	if !overridden || other {
		t.Fatalf("overridden=%v acme listed=%v", overridden, other)
	}
	// The alias resolves to an openai entry, so the anthropic filter drops it.
	if len(got.Aliases) != 0 {
		t.Fatalf("aliases = %+v", got.Aliases)
	}

	out, _, err = runPricing(t, "list", "--provider", "acme", "--pricing-file", path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acme-coder-2") || !strings.Contains(out, "override") || strings.Contains(out, "claude-") {
		t.Fatalf("acme list:\n%s", out)
	}
}

func TestPricingInfo(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	out, _, err := runPricing(t, "info", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got pricingInfoResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Catalog.Commit == "" || got.Catalog.URL == "" || got.CatalogModels < 100 || got.OverridesFound ||
		got.OverridesPath != filepath.Join(home, ".beacon", "endpoint", "pricing", "overrides.json") {
		t.Fatalf("info = %+v", got)
	}
	// info reports an unusable file instead of failing on it.
	bad := writeOverrides(t, `{"schema":"nope"}`)
	out, _, err = runPricing(t, "info", "--pricing-file", bad)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "NOT usable") || !strings.Contains(out, `schema "nope"`) || !strings.Contains(out, "Commit:") {
		t.Fatalf("info text:\n%s", out)
	}
}

func TestPricingValidate(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	path := writeOverrides(t, testOverridesDoc)
	out, _, err := runPricing(t, "validate", "--pricing-file", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ok: " + path,
		"2 model(s), 1 alias(es)",
		"model  claude-sonnet-4-5 replaces the catalog entry of the same name",
		"alias  my-gateway-model -> gpt-5 (catalog)",
		"unpublished, priced by fallback: cache_read_at_input_rate",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("validate missing %q:\n%s", want, out)
		}
	}

	bad := writeOverrides(t, `{"schema":"beacon.pricing.overrides/v1","models":{"acme":{"input_usd_per_mtok":3,"output_usd_per_mtok":0.0000001}}}`)
	if _, _, err := runPricing(t, "validate", "--pricing-file", bad); err == nil || !strings.Contains(err.Error(), `models["acme"].output_usd_per_mtok`) {
		t.Fatalf("invalid file: %v", err)
	}
	// There is nothing to validate at the default location.
	if _, _, err := runPricing(t, "validate"); err == nil || !strings.Contains(err.Error(), "no pricing overrides file at") {
		t.Fatalf("missing default: %v", err)
	}
	// Collisions are warnings on stderr, not failures.
	tie := writeOverrides(t, `{"schema":"beacon.pricing.overrides/v1","models":{
		"gw-a/acme-x":{"input_usd_per_mtok":1,"output_usd_per_mtok":1},
		"gw-b/acme-x":{"input_usd_per_mtok":2,"output_usd_per_mtok":2}}}`)
	_, stderr, err := runPricing(t, "validate", "--pricing-file", tie)
	if err != nil || !strings.Contains(stderr, "warning:") {
		t.Fatalf("tie: err=%v stderr=%q", err, stderr)
	}
}

func TestPricingDefaultFileFollowsUserAndSystemMode(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	userPath := pricing.DefaultOverridesPath(true)
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(testOverridesDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := runPricing(t, "show", "acme-coder-2", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"source": "override"`) {
		t.Fatalf("user default not read:\n%s", out)
	}
	out, _, err = runPricing(t, "info", "--system", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var info pricingInfoResult
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatal(err)
	}
	if info.OverridesPath != pricing.DefaultOverridesPath(false) || strings.HasPrefix(info.OverridesPath, home) {
		t.Fatalf("system path = %q", info.OverridesPath)
	}
}

// token-usage prices with the overrides file: --pricing-file names one, the default location is
// read when present, a missing named file and an invalid file are errors.
func TestTokenUsagePricesWithOverrides(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	lines := []string{
		// A Codex turn span on a gateway alias for gpt-5 ($1.25 in, $10 out list).
		`{"timestamp":"2026-06-11T10:01:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"hostname":"mac","os":"darwin"},"harness":{"name":"codex_cli","collection_method":"otlp"},"session":{"id":"codex-1"},"model":"my-gateway-model","gen_ai":{"usage":{"input_tokens":1000,"output_tokens":200}},"message":"codex.turn.token_usage","raw":{"source":"codex_turn_span","turn_id":"turn-1"}}`,
		// An OpenCode step on an internal model.
		`{"timestamp":"2026-06-11T10:02:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"hostname":"mac","os":"darwin"},"harness":{"name":"opencode","collection_method":"plugin"},"session":{"id":"oc-1"},"model":"acme-coder-2","gen_ai":{"usage":{"input_tokens":1000,"output_tokens":100}},"message":"opencode.step"}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// No file anywhere: both models are unpriced.
	var report map[string]interface{}
	if err := json.Unmarshal([]byte(runTokenUsageCommand(t, "--log-path", logPath, "--json")), &report); err != nil {
		t.Fatal(err)
	}
	if totals := report["totals"].(map[string]interface{}); totals["unpriced_events"] != 2.0 {
		t.Fatalf("no overrides: totals = %v", totals)
	}

	path := writeOverrides(t, testOverridesDoc)
	output := runTokenUsageCommand(t, "--log-path", logPath, "--json", "--pricing-file", path)
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	totals := report["totals"].(map[string]interface{})
	// gpt-5 via the alias: 1000 x $1.25 + 200 x $10 = $0.00325; acme-coder-2: 1000 x $1 +
	// 100 x $4 = $0.0014.
	if totals["estimated_cost_usd"] != 0.00465 || totals["unpriced_events"] != nil {
		t.Fatalf("with overrides: totals = %v", totals)
	}
	block := report["pricing"].(map[string]interface{})
	overrides := block["overrides"].(map[string]interface{})
	if overrides["path"] != path || len(overrides["sha256"].(string)) != 64 {
		t.Fatalf("overrides block = %v", overrides)
	}
	sources := map[string]string{}
	for _, m := range block["models"].([]interface{}) {
		row := m.(map[string]interface{})
		sources[row["model"].(string)] = row["source"].(string)
		if row["model"] == "my-gateway-model" && (row["alias"] != "my-gateway-model" || row["key"] != "gpt-5") {
			t.Fatalf("alias row = %v", row)
		}
	}
	if sources["acme-coder-2"] != "override" || sources["my-gateway-model"] != "catalog" {
		t.Fatalf("sources = %v", sources)
	}

	text := runTokenUsageCommand(t, "--log-path", logPath, "--pricing-file", path)
	for _, want := range []string{"overrides from " + path, "priced by the overrides file: acme-coder-2, my-gateway-model"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}

	// The default location is read without the flag.
	defaultPath := pricing.DefaultOverridesPath(true)
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultPath, []byte(testOverridesDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runTokenUsageCommand(t, "--log-path", logPath, "--json"); !strings.Contains(out, defaultPath) {
		t.Fatalf("default overrides file not used:\n%s", out)
	}

	// Errors: a named file that does not exist, and an invalid default file.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--pricing-file", filepath.Join(home, "missing.json")}, "no pricing overrides file at"},
	} {
		if err := tokenUsageError(t, append([]string{"--log-path", logPath}, tc.args...)...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: err = %v", tc.args, err)
		}
	}
	if err := os.WriteFile(defaultPath, []byte(`{"schema":"beacon.pricing.overrides/v1","models":{"x":{"input_usd_per_mtok":"$3","output_usd_per_mtok":1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tokenUsageError(t, "--log-path", logPath); err == nil || !strings.Contains(err.Error(), `models["x"].input_usd_per_mtok`) {
		t.Fatalf("invalid default: err = %v", err)
	}
}

func tokenUsageError(t *testing.T, args ...string) error {
	t.Helper()
	tokenUsageOpts = tokenUsageOptions{userMode: true}
	if err := tokenUsageCmd.Flags().Parse(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	var out bytes.Buffer
	tokenUsageCmd.SetOut(&out)
	defer tokenUsageCmd.SetOut(nil)
	return runTokenUsage(tokenUsageCmd, nil)
}
