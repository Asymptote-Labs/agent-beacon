package pricing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// OverridesSchema is the schema string an overrides file must declare. A layout change gets a
// new string, and a file declaring any other one is refused rather than half-read.
const OverridesSchema = "beacon.pricing.overrides/v1"

// OverridesFileName is the overrides file's name inside OverridesDir.
const OverridesFileName = "overrides.json"

// maxOverridesFileBytes bounds what ReadOverridesFile reads. A hand-written price list is a few
// kilobytes; anything near this is not one.
const maxOverridesFileBytes = 4 << 20

// MaxOverrideUSDPerMTok is the largest rate an overrides file may set: $100,000 per million
// tokens, ten cents a token. The most expensive published list price is two orders of magnitude
// below it, so a larger value is a unit mistake (a per-token or per-thousand price written where
// a per-million one belongs) and is refused rather than turned into a figure nobody paid.
const MaxOverrideUSDPerMTok = 100_000

// maxAboveTokens bounds a band threshold. No model has a context window near it.
const maxAboveTokens = 1_000_000_000_000

// OverridesDir is where the user or system endpoint keeps its pricing overrides.
func OverridesDir(userMode bool) string {
	return filepath.Join(endpointconfig.BaseDir(userMode), "pricing")
}

// DefaultOverridesPath is the overrides file `beacon token-usage`, `beacon pricing` and the
// dashboard read when no other file is named. Beacon never writes it: the file is the
// operator's, written by hand or by their own tooling.
func DefaultOverridesPath(userMode bool) string {
	return filepath.Join(OverridesDir(userMode), OverridesFileName)
}

// Overrides is a loaded, validated overrides file: price rows that replace or extend the
// catalog, and aliases that send a reported name to an existing entry.
type Overrides struct {
	path    string
	digest  string
	comment string
	// models are the file's own price rows; comments their optional notes.
	models   map[string]Rates
	comments map[string]string
	// aliases map an alias to its target; aliasSource says whether the target is one of
	// models (SourceOverride) or a catalog key (SourceCatalog).
	aliases     map[string]string
	aliasSource map[string]string
	// table indexes models and aliases together, an alias carrying its target's rates, so one
	// Lookup ladder decides between them exactly as it decides between catalog keys.
	table *table
}

// Path is the file the overrides were read from ("" when loaded from bytes with no path).
func (o *Overrides) Path() string { return o.path }

// SHA256 is the hex SHA-256 of the file's bytes, so a report can say exactly which file priced
// it.
func (o *Overrides) SHA256() string { return o.digest }

// Comment is the file's top-level comment.
func (o *Overrides) Comment() string { return o.comment }

// Models returns the names of the file's price rows, sorted.
func (o *Overrides) Models() []string { return sortedNames(o.models) }

// Rates returns the file's row for an exact model name.
func (o *Overrides) Rates(name string) (Rates, bool) {
	r, ok := o.models[name]
	return r, ok
}

// ModelComment returns the optional comment on a price row.
func (o *Overrides) ModelComment(name string) string { return o.comments[name] }

// Aliases returns the alias names, sorted.
func (o *Overrides) Aliases() []string { return sortedNames(o.aliases) }

// AliasTarget returns the entry an alias points at and whether that entry is an override row
// (SourceOverride) or a catalog key (SourceCatalog).
func (o *Overrides) AliasTarget(alias string) (target, source string, ok bool) {
	target, ok = o.aliases[alias]
	return target, o.aliasSource[alias], ok
}

// Warnings lists names in the file that collide once spelled loosely: two entries that Lookup
// would both match at one rung with different prices, which leaves a model that reaches them
// only by that rung unpriced. An exact query for either name still prices it, so this is a
// warning and not an error.
func (o *Overrides) Warnings() []string {
	var out []string
	// A collision at one rung repeats at every looser one; it is reported once, at the first.
	seen := map[string]bool{}
	for r := rungCanonical; r < numRungs; r++ {
		for _, spelled := range sortedNames(o.table.index[r]) {
			keys := o.table.index[r][spelled]
			id := spelled + "\x00" + strings.Join(keys, "\x00")
			if len(keys) < 2 || seen[id] {
				continue
			}
			seen[id] = true
			first := o.table.models[keys[0]]
			for _, k := range keys[1:] {
				if !samePrice(first, o.table.models[k]) {
					out = append(out, fmt.Sprintf("%s are the same name at the %s rung (%q) with different prices; a model reported as %q is left unpriced",
						quoteList(keys), rungMatch[r], spelled, spelled))
					break
				}
			}
		}
	}
	return out
}

func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The file layout. Rates are json.RawMessage so a value is converted from its decimal text
// exactly, never through a float64, and so a quoted number can be refused.
type overridesJSON struct {
	Schema  string                     `json:"schema"`
	Comment string                     `json:"comment,omitempty"`
	Models  map[string]overrideRowJSON `json:"models"`
	Aliases map[string]string          `json:"aliases"`
}

type overrideRatesJSON struct {
	Input        json.RawMessage `json:"input_usd_per_mtok"`
	Output       json.RawMessage `json:"output_usd_per_mtok"`
	CacheRead    json.RawMessage `json:"cache_read_usd_per_mtok"`
	CacheWrite   json.RawMessage `json:"cache_write_usd_per_mtok"`
	CacheWrite1h json.RawMessage `json:"cache_write_1h_usd_per_mtok"`
}

type overrideRowJSON struct {
	Provider string `json:"provider,omitempty"`
	Comment  string `json:"comment,omitempty"`
	overrideRatesJSON
	Bands []overrideBandJSON `json:"bands,omitempty"`
}

type overrideBandJSON struct {
	AboveTokens json.RawMessage `json:"above_tokens"`
	overrideRatesJSON
}

// ReadOverridesFile reads and validates the overrides file at path. A missing file is returned
// as an error satisfying errors.Is(err, os.ErrNotExist), so a caller reading the default path
// can treat it as "no overrides" while one reading a path the user named can refuse it.
func ReadOverridesFile(path string, catalog *Catalog) (*Overrides, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no pricing overrides file at %s: %w", path, err)
	}
	if err != nil {
		return nil, fmt.Errorf("read pricing overrides %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxOverridesFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read pricing overrides %s: %w", path, err)
	}
	if len(data) > maxOverridesFileBytes {
		return nil, fmt.Errorf("pricing overrides %s: larger than %d bytes", path, maxOverridesFileBytes)
	}
	o, err := LoadOverrides(data, catalog)
	if err != nil {
		return nil, fmt.Errorf("pricing overrides %s: %w", path, err)
	}
	o.path = path
	return o, nil
}

// LoadOverrides parses and validates an overrides document against catalog, which alias
// targets may name.
//
// Every error names the offending key as a path into the document (models["x"].bands[0]...),
// because a file that is refused prices nothing from it and the reader has to find the line.
func LoadOverrides(data []byte, catalog *Catalog) (*Overrides, error) {
	if err := checkDuplicateKeys(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	// An unknown field is almost always a misspelled rate. Ignoring it would leave that rate
	// unpublished and price its tokens by a fallback the author never asked for.
	dec.DisallowUnknownFields()
	var file overridesJSON
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("parse: trailing data after the document")
	}
	if file.Schema != OverridesSchema {
		return nil, fmt.Errorf("schema %q, want %q", file.Schema, OverridesSchema)
	}
	sum := sha256.Sum256(data)
	o := &Overrides{
		digest:      hex.EncodeToString(sum[:]),
		comment:     file.Comment,
		models:      map[string]Rates{},
		comments:    map[string]string{},
		aliases:     map[string]string{},
		aliasSource: map[string]string{},
	}
	for _, name := range sortedNames(file.Models) {
		where := fmt.Sprintf("models[%q]", name)
		if err := checkName(where, name); err != nil {
			return nil, err
		}
		rates, err := convertRow(where, file.Models[name])
		if err != nil {
			return nil, err
		}
		o.models[name] = rates
		if c := file.Models[name].Comment; c != "" {
			o.comments[name] = c
		}
	}
	entries := make(map[string]Rates, len(o.models)+len(file.Aliases))
	for name, rates := range o.models {
		entries[name] = rates
	}
	for _, alias := range sortedNames(file.Aliases) {
		where := fmt.Sprintf("aliases[%q]", alias)
		if err := checkName(where, alias); err != nil {
			return nil, err
		}
		if _, clash := o.models[alias]; clash {
			return nil, fmt.Errorf("%s: %q is also a model in this file; a name is either priced or an alias", where, alias)
		}
		target := file.Aliases[alias]
		if strings.TrimSpace(target) == "" {
			return nil, fmt.Errorf("%s: target is empty", where)
		}
		// The target is an exact name: the file's own rows first, as they win everywhere else,
		// then the catalog. Matching it loosely would let one typo price a different model.
		if rates, ok := o.models[target]; ok {
			o.aliasSource[alias] = SourceOverride
			entries[alias] = rates
		} else if rates, ok := catalog.Rates(target); ok {
			o.aliasSource[alias] = SourceCatalog
			entries[alias] = rates
		} else {
			return nil, fmt.Errorf("%s: target %q is neither a model in this file nor a catalog key (beacon pricing list prints the catalog keys)", where, target)
		}
		o.aliases[alias] = target
	}
	o.table = newTable(entries)
	return o, nil
}

func checkName(where, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s: the name is empty", where)
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("%s: the name has leading or trailing white space", where)
	}
	return nil
}

func convertRow(where string, row overrideRowJSON) (Rates, error) {
	base, err := convertSet(where, row.overrideRatesJSON, nil)
	if err != nil {
		return Rates{}, err
	}
	if base.Input == 0 {
		return Rates{}, fmt.Errorf("%s.input_usd_per_mtok: required", where)
	}
	if base.Output == 0 {
		return Rates{}, fmt.Errorf("%s.output_usd_per_mtok: required", where)
	}
	rates := Rates{Provider: strings.TrimSpace(row.Provider), RateSet: base}
	for i, b := range row.Bands {
		bwhere := fmt.Sprintf("%s.bands[%d]", where, i)
		above, err := convertAbove(bwhere+".above_tokens", b.AboveTokens)
		if err != nil {
			return Rates{}, err
		}
		set, err := convertSet(bwhere, b.overrideRatesJSON, &base)
		if err != nil {
			return Rates{}, err
		}
		rates.Bands = append(rates.Bands, Band{AboveTokens: above, RateSet: set})
	}
	// Cost walks bands in increasing order, so the file may list them in any order, but two
	// bands at one threshold are two answers to one question.
	sort.SliceStable(rates.Bands, func(i, j int) bool { return rates.Bands[i].AboveTokens < rates.Bands[j].AboveTokens })
	for i := 1; i < len(rates.Bands); i++ {
		if rates.Bands[i].AboveTokens == rates.Bands[i-1].AboveTokens {
			return Rates{}, fmt.Errorf("%s.bands: two bands above %d tokens", where, rates.Bands[i].AboveTokens)
		}
	}
	if err := validateRates(rates); err != nil {
		return Rates{}, fmt.Errorf("%s: %w", where, err)
	}
	return rates, nil
}

// convertSet converts one rate set. A band (inherit != nil) takes every field it leaves out
// from the row's base rates, so a band need only restate what changes above its threshold; a
// field unpublished in the base stays unpublished in the band.
func convertSet(where string, raw overrideRatesJSON, inherit *RateSet) (RateSet, error) {
	var set RateSet
	if inherit != nil {
		set = *inherit
	}
	fields := []struct {
		name string
		raw  json.RawMessage
		dst  *int64
	}{
		{"input_usd_per_mtok", raw.Input, &set.Input},
		{"output_usd_per_mtok", raw.Output, &set.Output},
		{"cache_read_usd_per_mtok", raw.CacheRead, &set.CacheRead},
		{"cache_write_usd_per_mtok", raw.CacheWrite, &set.CacheWrite},
		{"cache_write_1h_usd_per_mtok", raw.CacheWrite1h, &set.CacheWrite1h},
	}
	for _, f := range fields {
		v, present, err := convertRate(where+"."+f.name, f.raw)
		if err != nil {
			return RateSet{}, err
		}
		if present {
			*f.dst = v
		}
	}
	return set, nil
}

var (
	microsPerUSD   = big.NewRat(microdollarsPerDollar, 1)
	maxRateMicros  = new(big.Rat).SetInt64(MaxOverrideUSDPerMTok * microdollarsPerDollar)
	maxAboveTokRat = new(big.Rat).SetInt64(maxAboveTokens)
)

// convertRate turns a USD-per-MTok JSON number into integer microdollars per MTok exactly. The
// decimal text is read as a rational, so 0.30 is 300000 with no float rounding; a value finer
// than one microdollar per MTok cannot be represented exactly and is refused rather than
// rounded.
func convertRate(where string, raw json.RawMessage) (int64, bool, error) {
	r, present, err := numberLiteral(where, raw)
	if err != nil || !present {
		return 0, present, err
	}
	switch r.Sign() {
	case -1:
		return 0, false, fmt.Errorf("%s: %s is negative", where, raw)
	case 0:
		// A zero in the catalog means "unpublished", never "free"; an override keeps that rule,
		// so a zero cannot quietly drop tokens from an estimate.
		return 0, false, fmt.Errorf("%s: 0 is not a price; leave the field out when the rate is unpublished", where)
	}
	micros := new(big.Rat).Mul(r, microsPerUSD)
	if micros.Cmp(maxRateMicros) > 0 {
		return 0, false, fmt.Errorf("%s: %s is above the %d USD per million tokens limit; rates are per million tokens", where, raw, MaxOverrideUSDPerMTok)
	}
	if !micros.IsInt() {
		return 0, false, fmt.Errorf("%s: %s is finer than one microdollar per million tokens (6 decimal places)", where, raw)
	}
	return micros.Num().Int64(), true, nil
}

func convertAbove(where string, raw json.RawMessage) (int64, error) {
	r, present, err := numberLiteral(where, raw)
	if err != nil {
		return 0, err
	}
	if !present {
		return 0, fmt.Errorf("%s: required", where)
	}
	if !r.IsInt() || r.Sign() <= 0 {
		return 0, fmt.Errorf("%s: %s is not a positive whole number of tokens", where, raw)
	}
	if r.Cmp(maxAboveTokRat) > 0 {
		return 0, fmt.Errorf("%s: %s is above %d tokens", where, raw, int64(maxAboveTokens))
	}
	return r.Num().Int64(), nil
}

// numberLiteral reads a JSON number literal as an exact rational. An absent field or a null is
// "not present". A quoted number is refused: the file is hand-written, and a string where a
// number belongs is more often a pasted price with a currency sign than a deliberate choice.
func numberLiteral(where string, raw json.RawMessage) (*big.Rat, bool, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return nil, false, nil
	}
	if c := text[0]; c != '-' && (c < '0' || c > '9') {
		return nil, false, fmt.Errorf("%s: %s is not a JSON number", where, text)
	}
	// encoding/json has already checked this is one valid JSON value, and a value starting
	// with a digit or '-' is a number, so big.Rat sees only decimal and exponent forms. An
	// exponent is bounded first: big.Rat would otherwise build 1e999999999 in full before any
	// range check could refuse it.
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exp, err := strconv.Atoi(strings.TrimPrefix(text[i+1:], "+"))
		if err != nil || exp > 30 || exp < -30 {
			return nil, false, fmt.Errorf("%s: %s is out of range", where, text)
		}
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, false, fmt.Errorf("%s: %s is not a number", where, text)
	}
	return r, true, nil
}

// checkDuplicateKeys refuses an object that names one key twice. encoding/json keeps the last
// silently, which in a price list means one of two rates the author wrote is ignored without a
// word. Syntax errors are left to the real decode, which reports them with better context.
func checkDuplicateKeys(data []byte) error {
	type frame struct {
		object    bool
		keys      map[string]bool
		path      string
		expectKey bool
		key       string
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var stack []*frame
	// valueDone records that a value inside the top frame ended, so an object's next token is
	// a key again.
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		var top *frame
		if n := len(stack); n > 0 {
			top = stack[n-1]
		}
		if top != nil && top.object && top.expectKey {
			if tok == json.Delim('}') {
				stack = stack[:len(stack)-1]
				valueDone()
				continue
			}
			key, _ := tok.(string)
			if top.keys[key] {
				where := top.path
				if where == "" {
					where = "document"
				}
				return fmt.Errorf("%s: key %q appears twice", where, key)
			}
			top.keys[key] = true
			top.key = key
			top.expectKey = false
			continue
		}
		var path string
		switch {
		case top == nil:
		case top.object && top.path == "":
			path = top.key
		case top.object:
			path = fmt.Sprintf("%s[%q]", top.path, top.key)
		default:
			path = top.path + "[]"
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{object: true, keys: map[string]bool{}, path: path, expectKey: true})
		case json.Delim('['):
			stack = append(stack, &frame{path: path})
		case json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			valueDone()
		}
	}
}
