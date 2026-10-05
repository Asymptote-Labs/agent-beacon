package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

//go:embed catalog.json
var embeddedCatalog []byte

// SchemaVersion is the catalog.json layout this package reads. A layout change bumps it, and a
// catalog with any other version is refused rather than half-read.
const SchemaVersion = 1

// RateUnit names the unit every rate in the catalog is quoted in.
const RateUnit = "microdollars per million tokens"

// SourceInfo is the provenance of a catalog: whose list it is and how old it is.
type SourceInfo struct {
	// Name and License describe the upstream list.
	Name    string `json:"name"`
	License string `json:"license"`
	// URL is the list's canonical location; Commit is the upstream commit the catalog was
	// generated from ("" when generated from a local copy of unknown origin).
	URL    string `json:"url"`
	Commit string `json:"commit,omitempty"`
	// FetchedAt is the date (YYYY-MM-DD, UTC) the list was read.
	FetchedAt string `json:"fetched_at"`
}

// CatalogFile is the catalog.json layout. The generator writes it; LoadCatalog reads it.
type CatalogFile struct {
	SchemaVersion int        `json:"schema_version"`
	Source        SourceInfo `json:"source"`
	// GeneratedAt is the date (YYYY-MM-DD, UTC) the catalog was generated.
	GeneratedAt string           `json:"generated_at"`
	Unit        string           `json:"unit"`
	Models      map[string]Rates `json:"models"`
}

// Catalog is a loaded, validated price list with its lookup indexes.
type Catalog struct {
	file CatalogFile
	keys []string
	// index[r] maps a key spelled at rung r to every catalog key with that spelling.
	index [numRungs]map[string][]string
}

var (
	defaultOnce    sync.Once
	defaultCatalog *Catalog
)

// Default returns the catalog embedded in the binary. It panics if that catalog does not load,
// which TestEmbeddedCatalogLoads makes a build-time failure rather than a runtime one.
func Default() *Catalog {
	defaultOnce.Do(func() {
		c, err := LoadCatalog(embeddedCatalog)
		if err != nil {
			panic("pricing: embedded catalog.json is invalid: " + err.Error())
		}
		defaultCatalog = c
	})
	return defaultCatalog
}

// Lookup resolves model against the embedded catalog. See Catalog.Lookup.
func Lookup(model string) (Resolution, bool) {
	return Default().Lookup(model)
}

// LoadCatalog parses and validates a catalog.json document.
func LoadCatalog(data []byte) (*Catalog, error) {
	var file CatalogFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}
	if file.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("catalog schema_version %d, want %d", file.SchemaVersion, SchemaVersion)
	}
	if file.Unit != RateUnit {
		return nil, fmt.Errorf("catalog unit %q, want %q", file.Unit, RateUnit)
	}
	if len(file.Models) == 0 {
		return nil, fmt.Errorf("catalog has no models")
	}
	c := &Catalog{file: file}
	for key, rates := range file.Models {
		if err := validateRates(rates); err != nil {
			return nil, fmt.Errorf("model %q: %w", key, err)
		}
		c.keys = append(c.keys, key)
	}
	sort.Strings(c.keys)
	for r := range c.index {
		c.index[r] = make(map[string][]string, len(c.keys))
	}
	for _, key := range c.keys {
		for r := rung(0); r < numRungs; r++ {
			spelled := spell(r, key)
			if spelled == "" {
				continue
			}
			c.index[r][spelled] = append(c.index[r][spelled], key)
		}
	}
	return c, nil
}

func validateRates(r Rates) error {
	if err := validateSet(r.RateSet); err != nil {
		return err
	}
	var prev int64
	for i, b := range r.Bands {
		if b.AboveTokens <= prev {
			return fmt.Errorf("band %d threshold %d is not above %d", i, b.AboveTokens, prev)
		}
		prev = b.AboveTokens
		if err := validateSet(b.RateSet); err != nil {
			return fmt.Errorf("band above %d: %w", b.AboveTokens, err)
		}
	}
	return nil
}

func validateSet(s RateSet) error {
	if s.Input <= 0 || s.Output <= 0 {
		return fmt.Errorf("input and output rates must be positive (input %d, output %d)", s.Input, s.Output)
	}
	if s.CacheRead < 0 || s.CacheWrite < 0 || s.CacheWrite1h < 0 {
		return fmt.Errorf("negative cache rate")
	}
	return nil
}

// Source returns the catalog's provenance.
func (c *Catalog) Source() SourceInfo { return c.file.Source }

// GeneratedAt returns the date the catalog was generated.
func (c *Catalog) GeneratedAt() string { return c.file.GeneratedAt }

// Keys returns every priced model key, sorted.
func (c *Catalog) Keys() []string { return append([]string(nil), c.keys...) }

// Rates returns the entry for an exact catalog key.
func (c *Catalog) Rates(key string) (Rates, bool) {
	r, ok := c.file.Models[key]
	return r, ok
}
