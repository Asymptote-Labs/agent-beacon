// Package concepts holds definitions that threat rules share instead of each writing its own: a
// catalog is a reviewed list of named things, and every target that needs the definition (a CEL
// expression, a KQL expression, a documentation table) is generated from it.
//
// The first catalog is the credential stores: the files that hold credentials, by path. Each
// store says which paths it is, which it is not, how sure a match is (its tier), and proves it
// with example paths, which Decode checks. The expressions generated from the catalog are
// checked against the same examples, so a store means the same path set wherever it is used.
package concepts

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Tier is how sure it is that a path a store matches holds a credential.
type Tier string

const (
	// TierDefinite is a file that exists to hold a credential: ~/.aws/credentials, an SSH
	// private key.
	TierDefinite Tier = "definite"
	// TierLikely is a file that often holds one and often does not: ~/.kube/config,
	// .env.development, a file named credentials.json.
	TierLikely Tier = "likely"
)

// includes reports whether a selection that asks for at least the tier takes a store of tier t
// in. Definite is the stronger tier: a selection of likely stores takes the definite ones too.
func (least Tier) includes(t Tier) bool {
	return least == TierLikely || t == TierDefinite
}

// Catalog is a list of stores that name files by path.
type Catalog struct {
	// Name identifies the catalog (credential-stores), and Version counts its revisions.
	Name        string `yaml:"catalog"`
	Version     int    `yaml:"version"`
	Description string `yaml:"description"`
	// Kinds are the groups a store may belong to, in the order they are documented.
	Kinds  []Kind  `yaml:"kinds"`
	Stores []Store `yaml:"stores"`
}

// Kind is a group of stores a rule can select on its own: the Kubernetes ones, the browser ones.
type Kind struct {
	ID    string `yaml:"id"`
	Title string `yaml:"title"`
}

// Store is one thing that holds credentials, as the paths it is found at.
type Store struct {
	ID    string `yaml:"id"`
	Title string `yaml:"title"`
	Kind  string `yaml:"kind"`
	Tier  Tier   `yaml:"tier"`
	// Match are the patterns (see pattern) of the paths that are this store, and Exclude the
	// patterns of the ones among them that are not: .env.example is no dotenv file.
	Match   []string `yaml:"match"`
	Exclude []string `yaml:"exclude,omitempty"`
	// Examples are paths that are this store, at least one for each Match pattern.
	// Counterexamples are paths that look like it and are no store of the catalog at all, at
	// least one for each Exclude pattern.
	Examples        []string `yaml:"examples"`
	Counterexamples []string `yaml:"counterexamples,omitempty"`

	match, exclude []pattern
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Load reads and decodes the catalog file at path.
func Load(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Decode strictly decodes a catalog and checks it: its shape, its patterns, and that every store
// matches its examples and no store matches a counterexample. A catalog that decodes means what
// its examples say.
func Decode(data []byte) (*Catalog, error) {
	var c Catalog
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if err := c.compile(); err != nil {
		return nil, err
	}
	if err := c.checkExamples(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Catalog) compile() error {
	if !idPattern.MatchString(c.Name) {
		return fmt.Errorf("catalog %q: want a name like credential-stores", c.Name)
	}
	if c.Version < 1 {
		return fmt.Errorf("version %d: want 1 or more", c.Version)
	}
	kinds := map[string]bool{}
	for _, k := range c.Kinds {
		if !idPattern.MatchString(k.ID) || k.Title == "" || kinds[k.ID] {
			return fmt.Errorf("kind %q: want a unique id like cloud, and a title", k.ID)
		}
		kinds[k.ID] = true
	}
	if len(c.Stores) == 0 {
		return fmt.Errorf("no stores")
	}
	ids := map[string]bool{}
	for i := range c.Stores {
		s := &c.Stores[i]
		switch {
		case !idPattern.MatchString(s.ID):
			return fmt.Errorf("stores[%d]: id %q: want lowercase letters, digits and hyphens", i, s.ID)
		case ids[s.ID]:
			return fmt.Errorf("store %s: defined twice", s.ID)
		case s.Title == "":
			return fmt.Errorf("store %s: no title", s.ID)
		case !kinds[s.Kind]:
			return fmt.Errorf("store %s: kind %q is not one of the catalog's kinds", s.ID, s.Kind)
		case s.Tier != TierDefinite && s.Tier != TierLikely:
			return fmt.Errorf("store %s: tier %q: want %s or %s", s.ID, s.Tier, TierDefinite, TierLikely)
		case len(s.Match) == 0:
			return fmt.Errorf("store %s: no match patterns", s.ID)
		}
		ids[s.ID] = true
		var err error
		if s.match, err = parsePatterns(s.Match); err != nil {
			return fmt.Errorf("store %s: match: %w", s.ID, err)
		}
		if s.exclude, err = parsePatterns(s.Exclude); err != nil {
			return fmt.Errorf("store %s: exclude: %w", s.ID, err)
		}
	}
	return nil
}

func parsePatterns(sources []string) ([]pattern, error) {
	patterns := make([]pattern, len(sources))
	for i, source := range sources {
		if slices.Contains(sources[:i], source) {
			return nil, fmt.Errorf("pattern %q: listed twice", source)
		}
		p, err := parsePattern(source)
		if err != nil {
			return nil, err
		}
		patterns[i] = p
	}
	return patterns, nil
}

// checkExamples holds every store to its examples. An example must be the store's own: the one
// Lookup returns, so two stores that claim the same path are told apart or told off. Every
// pattern must be shown at work by an example, so none is dead or untested.
func (c *Catalog) checkExamples() error {
	for i := range c.Stores {
		s := &c.Stores[i]
		used := make([]bool, len(s.match))
		for _, example := range s.Examples {
			comps := splitPath(example)
			found, ok := c.lookup(comps)
			switch {
			case !s.matchesComponents(comps):
				return fmt.Errorf("store %s: example %q: not matched", s.ID, example)
			case !ok || found != s:
				return fmt.Errorf("store %s: example %q: is store %s, which comes first or is the surer tier", s.ID, example, found.ID)
			}
			for k := range s.match {
				used[k] = used[k] || s.match[k].matchesComponents(comps)
			}
		}
		if k := slices.Index(used, false); k >= 0 {
			return fmt.Errorf("store %s: match pattern %q: no example shows it", s.ID, s.Match[k])
		}
		excluded := make([]bool, len(s.exclude))
		for _, counter := range s.Counterexamples {
			comps := splitPath(counter)
			if found, ok := c.lookup(comps); ok {
				return fmt.Errorf("store %s: counterexample %q: is store %s", s.ID, counter, found.ID)
			}
			if !slices.ContainsFunc(s.match, func(p pattern) bool { return p.matchesComponents(comps) }) {
				continue
			}
			for k := range s.exclude {
				excluded[k] = excluded[k] || s.exclude[k].matchesComponents(comps)
			}
		}
		if k := slices.Index(excluded, false); k >= 0 {
			return fmt.Errorf("store %s: exclude pattern %q: no counterexample shows it", s.ID, s.Exclude[k])
		}
	}
	return nil
}

// matchesComponents reports whether a path, split by splitPath, is the store.
func (s *Store) matchesComponents(comps []string) bool {
	in := func(patterns []pattern) bool {
		return slices.ContainsFunc(patterns, func(p pattern) bool { return p.matchesComponents(comps) })
	}
	return in(s.match) && !in(s.exclude)
}

// Lookup returns the store path is: of the stores that match it, the definite one before the
// likely one, and the one listed first before the others.
func (c *Catalog) Lookup(path string) (*Store, bool) {
	return c.lookup(splitPath(path))
}

func (c *Catalog) lookup(comps []string) (*Store, bool) {
	var found *Store
	for i := range c.Stores {
		s := &c.Stores[i]
		if (found == nil || found.Tier != TierDefinite && s.Tier == TierDefinite) && s.matchesComponents(comps) {
			found = s
		}
	}
	return found, found != nil
}

// Selection is some of a catalog's stores as regular expressions (RE2 syntax, which Go, CEL's
// matches and Kusto's `matches regex` share): a path is in the selection when it matches the
// Include of one of the terms and not that term's Exclude.
type Selection []Term

// Term is one alternative of a Selection. Exclude is "" when the term excludes nothing.
type Term struct {
	Include, Exclude string
}

// Select returns the stores of at least the tier min, of the given kinds or, with none given, of
// every kind.
func (c *Catalog) Select(min Tier, kinds ...string) (Selection, error) {
	if min != TierDefinite && min != TierLikely {
		return nil, fmt.Errorf("tier %q: want %s or %s", min, TierDefinite, TierLikely)
	}
	for _, kind := range kinds {
		if !slices.ContainsFunc(c.Kinds, func(k Kind) bool { return k.ID == kind }) {
			return nil, fmt.Errorf("kind %q is not one of the catalog's kinds", kind)
		}
	}
	// The stores that exclude nothing share one expression; one that excludes gets its own, so
	// that what it leaves out is not taken from a store that keeps it.
	var plain []pattern
	var terms Selection
	for i := range c.Stores {
		s := &c.Stores[i]
		switch {
		case !min.includes(s.Tier) || len(kinds) > 0 && !slices.Contains(kinds, s.Kind):
		case len(s.exclude) == 0:
			plain = append(plain, s.match...)
		default:
			terms = append(terms, Term{Include: patternsRegex(s.match), Exclude: patternsRegex(s.exclude)})
		}
	}
	if len(plain) > 0 {
		terms = slices.Insert(terms, 0, Term{Include: patternsRegex(plain)})
	}
	return terms, nil
}

// CEL returns the selection as a CEL boolean expression over subject, a string expression such
// as e.file.path.
func (s Selection) CEL(subject string) string {
	return s.expression("false", " || ", func(t Term) string {
		include := subject + ".matches(" + strconv.Quote(t.Include) + ")"
		if t.Exclude == "" {
			return include
		}
		return "(" + include + " && !" + subject + ".matches(" + strconv.Quote(t.Exclude) + "))"
	})
}

// KQL returns the selection as a Kusto boolean expression over column, a string column.
func (s Selection) KQL(column string) string {
	// In a verbatim string literal a backslash is itself and a double quote is written twice.
	literal := func(regex string) string { return `@"` + strings.ReplaceAll(regex, `"`, `""`) + `"` }
	return s.expression("false", " or ", func(t Term) string {
		include := column + " matches regex " + literal(t.Include)
		if t.Exclude == "" {
			return include
		}
		return "(" + include + " and not(" + column + " matches regex " + literal(t.Exclude) + "))"
	})
}

func (s Selection) expression(empty, or string, term func(Term) string) string {
	if len(s) == 0 {
		return empty
	}
	terms := make([]string, len(s))
	for i, t := range s {
		terms[i] = term(t)
	}
	if len(terms) == 1 {
		return terms[0]
	}
	return "(" + strings.Join(terms, or) + ")"
}
