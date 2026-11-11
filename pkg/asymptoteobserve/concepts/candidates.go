package concepts

import (
	"regexp"
	"sort"
	"strings"
)

// A catalog is a reviewed list, so it is only as complete as what its reviewers have seen. The
// files agents actually read say what it is missing: a file that is named like something that
// holds a credential, is read, and is no store is a candidate for the catalog. Candidates are for
// a person to review, never a store on their own: the names are a guess.

// Candidate is a file name agents read that the catalog does not have and that is named like a
// credential's.
type Candidate struct {
	// Name is the file's name, in lower case.
	Name string
	// Reads counts the paths with that name among the ones given, and Example is the first.
	Reads   int
	Example string
}

var (
	// credentialName is what a file that holds a credential is often called.
	credentialName = regexp.MustCompile(`(?i)(secret|credential|passw(or)?d|passphrase|token|api[_-]?key|private[_-]?key|service[_-]?account|oauth|htpasswd|wallet|keystore|` +
		`\.(pem|p12|pfx|jks|key|kdb|ovpn|tfvars|asc)$|^id_[a-z0-9_]+$|^auth\.json$)`)
	// notAStore is what such a name is on a file that describes or uses a credential rather than
	// holding one: source code, documentation, a template, a public key, a lock file, a threat
	// rule or its definitions.
	notAStore = regexp.MustCompile(`(?i)(\.(go|py|rb|js|jsx|mjs|cjs|ts|tsx|java|kt|rs|c|cc|cpp|h|hpp|cs|php|swift|scala|ex|exs|sh|md|mdx|rst|txt|html|css|scss|svg|png|jpg|jpeg|gif|` +
		`lock|sum|pub|example|sample|template|dist|tmpl|schema)$|[._-](test|spec)\.|\.d\.ts$|\.(rule|catalog|predicates)\.ya?ml$)`)
	// lineSelector is what an agent's file tool may add to a path to name lines of the file:
	// notes.txt:10-20.
	lineSelector = regexp.MustCompile(`(:(raw|[0-9][0-9,+-]*))+$`)
)

// Candidates returns the names, among the paths given, of files the catalog does not have and
// that are named like a credential's, most read first. A path is counted once however often it
// is given.
func (c *Catalog) Candidates(paths []string) []Candidate {
	byName := map[string]*Candidate{}
	seen := map[string]bool{}
	for _, path := range paths {
		path = lineSelector.ReplaceAllString(path, "")
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		name := path[strings.LastIndexAny(path, `/\`)+1:]
		if !credentialName.MatchString(name) || notAStore.MatchString(name) {
			continue
		}
		if _, ok := c.Lookup(path); ok {
			continue
		}
		key := strings.ToLower(name)
		candidate := byName[key]
		if candidate == nil {
			candidate = &Candidate{Name: key, Example: path}
			byName[key] = candidate
		}
		candidate.Reads++
	}
	candidates := make([]Candidate, 0, len(byName))
	for _, candidate := range byName {
		candidates = append(candidates, *candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Reads != candidates[j].Reads {
			return candidates[i].Reads > candidates[j].Reads
		}
		return candidates[i].Name < candidates[j].Name
	})
	return candidates
}
