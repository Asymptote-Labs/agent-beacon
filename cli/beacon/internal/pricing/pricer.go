package pricing

import (
	"errors"
	"os"
)

// Pricer resolves models against an overrides file first and the catalog second. It is what
// every report prices through; a Pricer with no overrides is the catalog alone.
type Pricer struct {
	catalog   *Catalog
	overrides *Overrides
	// overridesPath and overridesErr record an overrides file that was asked for and could not
	// be used, so a report that carried on without it can say so.
	overridesPath string
	overridesErr  error
}

// NewPricer combines a catalog with optional overrides (nil for none).
func NewPricer(catalog *Catalog, overrides *Overrides) *Pricer {
	return &Pricer{catalog: catalog, overrides: overrides}
}

// LoadPricer prices from catalog plus the overrides file at path.
//
// A missing file is not an error unless required: the default location usually holds nothing,
// and a report should not fail for want of a file nobody wrote. A file named by the user
// (required) must exist. An invalid file is always an error; the returned Pricer is still
// usable, prices from the catalog alone, and reports the error through OverridesError, so a
// reader that must not fail (the dashboard) can carry on and say why its figures are list
// prices. An empty path means no overrides.
func LoadPricer(catalog *Catalog, path string, required bool) (*Pricer, error) {
	p := NewPricer(catalog, nil)
	if path == "" {
		return p, nil
	}
	o, err := ReadOverridesFile(path, catalog)
	if err != nil {
		if !required && errors.Is(err, os.ErrNotExist) {
			return p, nil
		}
		p.overridesPath = path
		p.overridesErr = err
		return p, err
	}
	p.overrides = o
	return p, nil
}

// Catalog is the catalog the Pricer falls back to.
func (p *Pricer) Catalog() *Catalog { return p.catalog }

// Overrides is the loaded overrides file, or nil.
func (p *Pricer) Overrides() *Overrides { return p.overrides }

// OverridesError is the reason an overrides file that was asked for is not in use, with the
// path it was read from; nil when there is none.
func (p *Pricer) OverridesError() (string, error) { return p.overridesPath, p.overridesErr }

// Lookup resolves model to a price.
//
// The overrides file is tried first, with the same ladder Catalog.Lookup runs (exact, then the
// looser spellings, then stripping trailing decorations), and if any of its names match, it
// decides: a row there replaces the catalog's price for every spelling that reaches it, which
// is what a negotiated rate for claude-sonnet-4-5 means for claude-sonnet-4-5-20250929 too. A
// tie between differently priced names in the file leaves the model unpriced rather than
// falling through to a list price the file was written to replace. Only a model no override
// name reaches is looked up in the catalog.
func (p *Pricer) Lookup(model string) (Resolution, bool) {
	if o := p.overrides; o != nil {
		res, ok := o.table.lookup(model)
		if ok || len(res.Candidates) > 0 {
			res.Source = SourceOverride
			if target, isAlias := o.aliases[res.Key]; ok && isAlias {
				res.Alias = res.Key
				res.Key = target
				res.Source = o.aliasSource[res.Alias]
			}
			return res, ok
		}
	}
	return p.catalog.Lookup(model)
}
