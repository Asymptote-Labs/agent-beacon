package cmd

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pricing"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
)

var pricingOpts struct {
	userMode    bool
	systemMode  bool
	pricingFile string
	provider    string
	json        bool
}

func pricingUserMode() bool {
	if pricingOpts.systemMode {
		return false
	}
	return pricingOpts.userMode
}

var pricingCmd = &cobra.Command{
	Use:   "pricing",
	Short: "Inspect the price list behind token cost estimates, and your overrides",
	Long: `Inspect how 'beacon token-usage' and the dashboard price token usage.

Estimates come from a list-price catalog embedded in Beacon and, where it names a model, from
an overrides file you write: ~/.beacon/endpoint/pricing/overrides.json (or the system endpoint's
pricing/overrides.json with --system), or the file named by --pricing-file. An override row
replaces the catalog's price for that model; an alias sends a reported name to an existing
entry. Every command here is offline and read-only: Beacon never writes the overrides file.`,
}

// pricingLoad returns the pricer for the selected overrides file. A missing default file is
// not an error; a file named with --pricing-file must exist, and an invalid file is an error.
func pricingLoad() (*pricing.Pricer, string, error) {
	path, required := pricingOverridesPath()
	p, err := pricing.LoadPricer(pricing.Default(), path, required)
	return p, path, err
}

func pricingOverridesPath() (path string, required bool) {
	if explicit := strings.TrimSpace(pricingOpts.pricingFile); explicit != "" {
		return explicit, true
	}
	return pricing.DefaultOverridesPath(pricingUserMode()), false
}

// pricingBand is one long-context band in pricing output.
type pricingBand struct {
	AboveTokens int64               `json:"above_tokens"`
	Rates       tokens.RatesPerMTok `json:"rates_usd_per_mtok"`
}

// pricingShowResult is 'beacon pricing show --json'.
type pricingShowResult struct {
	Model  string `json:"model"`
	Priced bool   `json:"priced"`
	// Source is where the rates came from (catalog or override); Key the entry.
	Source   string               `json:"source,omitempty"`
	Key      string               `json:"key,omitempty"`
	Alias    string               `json:"alias,omitempty"`
	Provider string               `json:"provider,omitempty"`
	Match    string               `json:"match,omitempty"`
	Stripped []string             `json:"stripped,omitempty"`
	Rates    *tokens.RatesPerMTok `json:"rates_usd_per_mtok,omitempty"`
	Bands    []pricingBand        `json:"bands,omitempty"`
	// Fallbacks names each rate the entry does not publish and how its tokens are priced.
	Fallbacks        []string                 `json:"fallbacks,omitempty"`
	Candidates       []string                 `json:"candidates,omitempty"`
	CandidatesSource string                   `json:"candidates_source,omitempty"`
	Catalog          tokens.PricingCatalog    `json:"catalog"`
	Overrides        *tokens.PricingOverrides `json:"overrides,omitempty"`
}

var pricingShowCmd = &cobra.Command{
	Use:          "show <model>",
	Short:        "Show how a model name is priced",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, _, err := pricingLoad()
		if err != nil {
			return err
		}
		model := args[0]
		res, ok := p.Lookup(model)
		out := pricingShowResult{
			Model:     model,
			Priced:    ok,
			Catalog:   pricingCatalogInfo(p.Catalog()),
			Overrides: pricingOverridesInfo(p),
		}
		if ok {
			rates := tokens.RatesUSDPerMTok(res.Rates.RateSet)
			out.Source, out.Key, out.Alias = res.Source, res.Key, res.Alias
			out.Provider = res.Rates.Provider
			out.Match = string(res.Match)
			out.Stripped = res.Stripped
			out.Rates = &rates
			for _, b := range res.Rates.Bands {
				out.Bands = append(out.Bands, pricingBand{AboveTokens: b.AboveTokens, Rates: tokens.RatesUSDPerMTok(b.RateSet)})
			}
			out.Fallbacks = pricingFallbacks(res.Rates.RateSet)
		} else if len(res.Candidates) > 0 {
			out.Source = res.Source
			out.Match = string(res.Match)
			out.Candidates = res.Candidates
			out.CandidatesSource = res.Source
		}
		w := cmd.OutOrStdout()
		if pricingOpts.json {
			return writeIndentedJSON(w, out)
		}
		renderPricingShow(w, p, res, out)
		return nil
	},
}

func renderPricingShow(w io.Writer, p *pricing.Pricer, res pricing.Resolution, out pricingShowResult) {
	fmt.Fprintf(w, "Model:      %s\n", out.Model)
	if !out.Priced {
		if len(out.Candidates) > 0 {
			fmt.Fprintf(w, "Not priced: %d %s entries match at the %s rung with different prices:\n", len(out.Candidates), out.CandidatesSource, out.Match)
			for _, c := range out.Candidates {
				fmt.Fprintf(w, "  %s\n", c)
			}
			fmt.Fprintln(w, "Report the model under one of those names, or add it to the overrides file under its own name.")
		} else {
			fmt.Fprintln(w, "Not priced: no catalog key or override matches it at any rung.")
			fmt.Fprintln(w, "Estimates leave its tokens out. Add it to the overrides file, or alias it to a catalog key.")
		}
		pricingFooter(w, p)
		return
	}
	switch out.Source {
	case pricing.SourceOverride:
		fmt.Fprintf(w, "Priced by:  override %q\n", out.Key)
		if c := p.Overrides().ModelComment(out.Key); c != "" {
			fmt.Fprintf(w, "Comment:    %s\n", c)
		}
		if _, ok := p.Catalog().Rates(out.Key); ok {
			fmt.Fprintf(w, "            (replaces the catalog entry of the same name)\n")
		}
	default:
		fmt.Fprintf(w, "Priced by:  catalog key %q\n", out.Key)
	}
	if out.Alias != "" {
		fmt.Fprintf(w, "Alias:      %s -> %s (overrides file)\n", out.Alias, out.Key)
	}
	if out.Provider != "" {
		fmt.Fprintf(w, "Provider:   %s\n", out.Provider)
	}
	match := out.Match
	if len(out.Stripped) > 0 {
		match += " after stripping " + strings.Join(out.Stripped, ", ")
	}
	fmt.Fprintf(w, "Match:      %s\n", match)
	fmt.Fprintln(w, "Rates (USD per million tokens):")
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	set := res.Rates.RateSet
	fmt.Fprintf(tw, "  input\t%s\n", usdPerMTok(set.Input))
	fmt.Fprintf(tw, "  output\t%s\n", usdPerMTok(set.Output))
	fmt.Fprintf(tw, "  cache read\t%s\n", usdPerMTokOr(set.CacheRead, "unpublished: priced at the input rate"))
	fmt.Fprintf(tw, "  cache write (5m)\t%s\n", usdPerMTokOr(set.CacheWrite, "unpublished: priced at the input rate"))
	fmt.Fprintf(tw, "  cache write (1h)\t%s\n", usdPerMTokOr(set.CacheWrite1h, "unpublished: priced at the 5m write rate"))
	tw.Flush()
	if len(res.Rates.Bands) > 0 {
		fmt.Fprintln(w, "Long-context bands (a single request whose prompt is above the threshold is billed at the band's rates for every token):")
		for _, b := range res.Rates.Bands {
			fmt.Fprintf(w, "  above %d: input %s, output %s, cache read %s, cache write %s, cache write 1h %s\n",
				b.AboveTokens, usdPerMTok(b.Input), usdPerMTok(b.Output), usdPerMTokOr(b.CacheRead, "-"),
				usdPerMTokOr(b.CacheWrite, "-"), usdPerMTokOr(b.CacheWrite1h, "-"))
		}
	}
	pricingFooter(w, p)
}

func pricingFooter(w io.Writer, p *pricing.Pricer) {
	cat := pricingCatalogInfo(p.Catalog())
	fmt.Fprintf(w, "\nCatalog:    %s, generated %s\n", cat.Name, cat.GeneratedAt)
	if o := p.Overrides(); o != nil {
		fmt.Fprintf(w, "Overrides:  %s (sha256 %s)\n", o.Path(), shortSHA(o.SHA256()))
	}
}

// pricingFallbacks names, in the report's vocabulary, each rate an entry leaves unpublished.
func pricingFallbacks(set pricing.RateSet) []string {
	var out []string
	if set.CacheRead <= 0 {
		out = append(out, "cache_read_at_input_rate")
	}
	if set.CacheWrite <= 0 {
		out = append(out, "cache_write_at_input_rate")
	}
	if set.CacheWrite1h <= 0 {
		out = append(out, "cache_write_1h_at_write_rate")
	}
	return out
}

// usdPerMTok renders an exact rate with at least two decimal places and no trailing zeros
// beyond them: 3000000 is "3.00", 375000 is "0.375", 1 is "0.000001".
func usdPerMTok(micros int64) string {
	s := pricing.Microdollars(micros).Format(6)
	for strings.HasSuffix(s, "0") && len(s)-strings.IndexByte(s, '.') > 3 {
		s = s[:len(s)-1]
	}
	return s
}

func usdPerMTokOr(micros int64, unpublished string) string {
	if micros <= 0 {
		return unpublished
	}
	return usdPerMTok(micros)
}

func pricingCatalogInfo(c *pricing.Catalog) tokens.PricingCatalog {
	src := c.Source()
	return tokens.PricingCatalog{
		Name:        src.Name,
		License:     src.License,
		URL:         src.URL,
		Commit:      src.Commit,
		FetchedAt:   src.FetchedAt,
		GeneratedAt: c.GeneratedAt(),
	}
}

func pricingOverridesInfo(p *pricing.Pricer) *tokens.PricingOverrides {
	if o := p.Overrides(); o != nil {
		return &tokens.PricingOverrides{Path: o.Path(), SHA256: o.SHA256(), Models: len(o.Models()), Aliases: len(o.Aliases())}
	}
	if path, err := p.OverridesError(); err != nil {
		return &tokens.PricingOverrides{Path: path, Error: err.Error()}
	}
	return nil
}

// pricingListEntry is one priced entry in 'beacon pricing list --json'.
type pricingListEntry struct {
	Key      string              `json:"key"`
	Source   string              `json:"source"`
	Provider string              `json:"provider,omitempty"`
	Rates    tokens.RatesPerMTok `json:"rates_usd_per_mtok"`
	Bands    []pricingBand       `json:"bands,omitempty"`
	// Overridden is set on a catalog entry that an override row of the same name replaces.
	Overridden bool `json:"overridden,omitempty"`
}

type pricingAliasEntry struct {
	Alias        string `json:"alias"`
	Target       string `json:"target"`
	TargetSource string `json:"target_source"`
}

var pricingListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List catalog entries and overrides",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, _, err := pricingLoad()
		if err != nil {
			return err
		}
		provider := strings.TrimSpace(pricingOpts.provider)
		keep := func(r pricing.Rates) bool { return provider == "" || strings.EqualFold(r.Provider, provider) }
		var entries []pricingListEntry
		var aliases []pricingAliasEntry
		o := p.Overrides()
		if o != nil {
			for _, name := range o.Models() {
				r, _ := o.Rates(name)
				if keep(r) {
					entries = append(entries, pricingEntry(name, pricing.SourceOverride, r, false))
				}
			}
			for _, alias := range o.Aliases() {
				target, source, _ := o.AliasTarget(alias)
				if provider != "" {
					r, _ := p.Lookup(alias)
					if !keep(r.Rates) {
						continue
					}
				}
				aliases = append(aliases, pricingAliasEntry{Alias: alias, Target: target, TargetSource: source})
			}
		}
		for _, key := range p.Catalog().Keys() {
			r, _ := p.Catalog().Rates(key)
			if !keep(r) {
				continue
			}
			overridden := false
			if o != nil {
				_, overridden = o.Rates(key)
			}
			entries = append(entries, pricingEntry(key, pricing.SourceCatalog, r, overridden))
		}
		w := cmd.OutOrStdout()
		if pricingOpts.json {
			return writeIndentedJSON(w, map[string]interface{}{
				"models":    nonNil(entries),
				"aliases":   nonNilAliases(aliases),
				"catalog":   pricingCatalogInfo(p.Catalog()),
				"overrides": pricingOverridesInfo(p),
			})
		}
		tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "MODEL\tSOURCE\tPROVIDER\tINPUT\tOUTPUT\tCACHE READ\tCACHE WRITE\tBANDS")
		for _, e := range entries {
			source := e.Source
			if e.Overridden {
				source = "catalog*"
			}
			r := e.Rates
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", e.Key, source, dashIfEmpty(e.Provider),
				usdFloat(r.Input), usdFloat(r.Output), usdFloat(r.CacheRead), usdFloat(r.CacheWrite), len(e.Bands))
		}
		tw.Flush()
		if len(aliases) > 0 {
			fmt.Fprintln(w)
			tw = tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ALIAS\tTARGET\tTARGET SOURCE")
			for _, a := range aliases {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", a.Alias, a.Target, a.TargetSource)
			}
			tw.Flush()
		}
		fmt.Fprintf(w, "\n%d entr(ies). Rates are USD per million tokens; - is unpublished.", len(entries))
		if o != nil {
			fmt.Fprintf(w, " catalog* is replaced by an override.\nOverrides: %s", o.Path())
		}
		fmt.Fprintln(w)
		return nil
	},
}

func pricingEntry(key, source string, r pricing.Rates, overridden bool) pricingListEntry {
	e := pricingListEntry{Key: key, Source: source, Provider: r.Provider, Rates: tokens.RatesUSDPerMTok(r.RateSet), Overridden: overridden}
	for _, b := range r.Bands {
		e.Bands = append(e.Bands, pricingBand{AboveTokens: b.AboveTokens, Rates: tokens.RatesUSDPerMTok(b.RateSet)})
	}
	return e
}

func nonNil(e []pricingListEntry) []pricingListEntry {
	if e == nil {
		return []pricingListEntry{}
	}
	return e
}

func nonNilAliases(a []pricingAliasEntry) []pricingAliasEntry {
	if a == nil {
		return []pricingAliasEntry{}
	}
	return a
}

func usdFloat(v float64) string {
	if v == 0 {
		return "-"
	}
	return usdPerMTok(int64(v*1_000_000 + 0.5))
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// pricingInfoResult is 'beacon pricing info --json'.
type pricingInfoResult struct {
	Catalog       tokens.PricingCatalog `json:"catalog"`
	CatalogModels int                   `json:"catalog_models"`
	Unit          string                `json:"unit"`
	Tier          string                `json:"tier"`
	// OverridesPath is the file the commands and reports read; OverridesFound says whether it
	// exists, and Overrides describes it when it does.
	OverridesPath  string                   `json:"overrides_path"`
	OverridesFound bool                     `json:"overrides_found"`
	Overrides      *tokens.PricingOverrides `json:"overrides,omitempty"`
}

var pricingInfoCmd = &cobra.Command{
	Use:          "info",
	Short:        "Show where the prices come from: the catalog's provenance and the overrides file",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, _ := pricingOverridesPath()
		// info reports an unusable file rather than failing on it: saying why is its job.
		p, _ := pricing.LoadPricer(pricing.Default(), path, false)
		info := pricingInfoResult{
			Catalog:       pricingCatalogInfo(p.Catalog()),
			CatalogModels: len(p.Catalog().Keys()),
			Unit:          "USD per million tokens",
			Tier:          tokens.PricingTier,
			OverridesPath: path,
			Overrides:     pricingOverridesInfo(p),
		}
		info.OverridesFound = info.Overrides != nil
		w := cmd.OutOrStdout()
		if pricingOpts.json {
			return writeIndentedJSON(w, info)
		}
		c := info.Catalog
		fmt.Fprintf(w, "Catalog:    %s (%s license)\n", c.Name, c.License)
		fmt.Fprintf(w, "Source:     %s\n", c.URL)
		if c.Commit != "" {
			fmt.Fprintf(w, "Commit:     %s\n", c.Commit)
		}
		fmt.Fprintf(w, "Fetched:    %s\n", c.FetchedAt)
		fmt.Fprintf(w, "Generated:  %s\n", c.GeneratedAt)
		fmt.Fprintf(w, "Models:     %d\n", info.CatalogModels)
		fmt.Fprintf(w, "Tier:       %s (no batch, flex, priority or subscription pricing)\n", info.Tier)
		fmt.Fprintf(w, "\nOverrides:  %s\n", path)
		switch o := info.Overrides; {
		case o == nil:
			fmt.Fprintln(w, "            not present; estimates use the catalog alone")
		case o.Error != "":
			fmt.Fprintf(w, "            NOT usable: %s\n", o.Error)
		default:
			fmt.Fprintf(w, "            sha256 %s, %d model(s), %d alias(es)\n", o.SHA256, o.Models, o.Aliases)
		}
		return nil
	},
}

// pricingValidateEntry is one name in 'beacon pricing validate --json'.
type pricingValidateEntry struct {
	Name string `json:"name"`
	// Kind is "model" or "alias".
	Kind string `json:"kind"`
	// ReplacesCatalog is set on a model row whose name is also a catalog key.
	ReplacesCatalog bool   `json:"replaces_catalog,omitempty"`
	Target          string `json:"target,omitempty"`
	TargetSource    string `json:"target_source,omitempty"`
	// Fallbacks names each rate the row leaves unpublished.
	Fallbacks []string `json:"fallbacks,omitempty"`
}

var pricingValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Check a pricing overrides file",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, _ := pricingOverridesPath()
		// Validating a file that is not there is a failure, whichever path it is.
		p, err := pricing.LoadPricer(pricing.Default(), path, true)
		if err != nil {
			return err
		}
		o := p.Overrides()
		var entries []pricingValidateEntry
		for _, name := range o.Models() {
			r, _ := o.Rates(name)
			_, replaces := p.Catalog().Rates(name)
			entries = append(entries, pricingValidateEntry{Name: name, Kind: "model", ReplacesCatalog: replaces, Fallbacks: pricingFallbacks(r.RateSet)})
		}
		for _, alias := range o.Aliases() {
			target, source, _ := o.AliasTarget(alias)
			entries = append(entries, pricingValidateEntry{Name: alias, Kind: "alias", Target: target, TargetSource: source})
		}
		warnings := o.Warnings()
		w := cmd.OutOrStdout()
		if pricingOpts.json {
			if warnings == nil {
				warnings = []string{}
			}
			if entries == nil {
				entries = []pricingValidateEntry{}
			}
			return writeIndentedJSON(w, map[string]interface{}{
				"valid":    true,
				"path":     o.Path(),
				"sha256":   o.SHA256(),
				"entries":  entries,
				"warnings": warnings,
			})
		}
		fmt.Fprintf(w, "ok: %s (sha256 %s): %d model(s), %d alias(es)\n", o.Path(), shortSHA(o.SHA256()), len(o.Models()), len(o.Aliases()))
		for _, e := range entries {
			switch {
			case e.Kind == "alias":
				fmt.Fprintf(w, "  alias  %s -> %s (%s)\n", e.Name, e.Target, e.TargetSource)
			case e.ReplacesCatalog:
				fmt.Fprintf(w, "  model  %s replaces the catalog entry of the same name\n", e.Name)
			default:
				fmt.Fprintf(w, "  model  %s\n", e.Name)
			}
			if len(e.Fallbacks) > 0 {
				fmt.Fprintf(w, "         unpublished, priced by fallback: %s\n", strings.Join(e.Fallbacks, ", "))
			}
		}
		for _, warning := range warnings {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
		}
		return nil
	},
}

func init() {
	pricingCmd.PersistentFlags().BoolVar(&pricingOpts.userMode, "user", true, "Use per-user endpoint paths")
	pricingCmd.PersistentFlags().BoolVar(&pricingOpts.systemMode, "system", false, "Use system endpoint paths")
	pricingCmd.PersistentFlags().StringVar(&pricingOpts.pricingFile, "pricing-file", "", "Pricing overrides file to use instead of <endpoint>/pricing/overrides.json")
	pricingCmd.PersistentFlags().BoolVar(&pricingOpts.json, "json", false, "Print JSON")
	pricingListCmd.Flags().StringVar(&pricingOpts.provider, "provider", "", "Only list entries from this provider (for example anthropic, openai, gemini)")
	pricingCmd.AddCommand(pricingShowCmd, pricingListCmd, pricingInfoCmd, pricingValidateCmd)
	rootCmd.AddCommand(pricingCmd)
}
