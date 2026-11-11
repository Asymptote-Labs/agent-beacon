package concepts

import (
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const smallCatalog = `
catalog: credential-stores
version: 1
kinds:
  - {id: cloud, title: Cloud}
  - {id: dotenv, title: Environment files}
  - {id: generic, title: Generic}
stores:
  - id: aws
    title: AWS shared credentials
    kind: cloud
    tier: definite
    match: [".aws/credentials"]
    examples: ["/home/u/.aws/credentials"]
    counterexamples: ["/home/u/.aws/config"]
  - id: dotenv-variant
    title: Environment file for one environment
    kind: dotenv
    tier: likely
    match: [".env.*"]
    exclude: [".env.example"]
    examples: ["/repo/.env.local"]
    counterexamples: ["/repo/.env.example"]
  - id: credentials-file
    title: A file named credentials
    kind: generic
    tier: likely
    match: ["credentials", "credentials.json"]
    examples: ["/srv/credentials", "/srv/credentials.json"]
`

func TestLookupPrefersTheSurerStore(t *testing.T) {
	c, err := Decode([]byte(smallCatalog))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/home/u/.aws/credentials": "aws", // credentials-file matches it too
		"/srv/app/credentials":     "credentials-file",
		"/repo/.env.production":    "dotenv-variant",
		"/repo/.env.example":       "",
		"/repo/README.md":          "",
	} {
		got := ""
		if s, ok := c.Lookup(path); ok {
			got = s.ID
		}
		if got != want {
			t.Errorf("Lookup(%q) = %q, want %q", path, got, want)
		}
	}
}

// A selection is the stores of a tier and kind, and its two renderings carry the same
// expressions. A store that excludes paths keeps its exclusion to itself.
func TestSelect(t *testing.T) {
	c, err := Decode([]byte(smallCatalog))
	if err != nil {
		t.Fatal(err)
	}
	const (
		aws     = `(?i)(?:^|[/\\])(?:\.aws[/\\]credentials$)`
		generic = `(?i)(?:^|[/\\])(?:credentials$|credentials\.json$)`
		both    = `(?i)(?:^|[/\\])(?:\.aws[/\\]credentials$|credentials$|credentials\.json$)`
		variant = `(?i)(?:^|[/\\])(?:\.env\.[^/\\]*$)`
		example = `(?i)(?:^|[/\\])(?:\.env\.example$)`
	)
	for _, tc := range []struct {
		tier  Tier
		kinds []string
		want  Selection
	}{
		{TierDefinite, nil, Selection{{Include: aws}}},
		{TierLikely, nil, Selection{{Include: both}, {Include: variant, Exclude: example}}},
		{TierLikely, []string{"generic"}, Selection{{Include: generic}}},
		{TierLikely, []string{"dotenv", "cloud"}, Selection{{Include: aws}, {Include: variant, Exclude: example}}},
		{TierDefinite, []string{"dotenv"}, nil},
	} {
		got, err := c.Select(tc.tier, tc.kinds...)
		if err != nil || len(got) != len(tc.want) {
			t.Errorf("Select(%s, %q) = %q, %v; want %q", tc.tier, tc.kinds, got, err, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("Select(%s, %q)[%d] = %q, want %q", tc.tier, tc.kinds, i, got[i], tc.want[i])
			}
		}
	}
	if _, err := c.Select(TierLikely, "kubernetes"); err == nil {
		t.Error("Select with a kind the catalog does not have: want an error")
	}
	if _, err := c.Select("certain"); err == nil {
		t.Error("Select with an unknown tier: want an error")
	}

	all, _ := c.Select(TierLikely)
	if got, want := all.CEL("e.file.path"), `(e.file.path.matches("(?i)(?:^|[/\\\\])(?:\\.aws[/\\\\]credentials$|credentials$|credentials\\.json$)") || `+
		`(e.file.path.matches("(?i)(?:^|[/\\\\])(?:\\.env\\.[^/\\\\]*$)") && !e.file.path.matches("(?i)(?:^|[/\\\\])(?:\\.env\\.example$)")))`; got != want {
		t.Errorf("CEL =\n%s\nwant\n%s", got, want)
	}
	if got, want := all.KQL("FilePath"), `(FilePath matches regex @"`+both+`" or (FilePath matches regex @"`+variant+`" and not(FilePath matches regex @"`+example+`")))`; got != want {
		t.Errorf("KQL =\n%s\nwant\n%s", got, want)
	}
	definite, _ := c.Select(TierDefinite)
	if got, want := definite.KQL("FilePath"), `FilePath matches regex @"`+aws+`"`; got != want {
		t.Errorf("KQL = %s, want %s", got, want)
	}
	none, _ := c.Select(TierDefinite, "dotenv")
	if none.CEL("p") != "false" || none.KQL("p") != "false" {
		t.Errorf("an empty selection = %q, %q; want false", none.CEL("p"), none.KQL("p"))
	}
}

// A catalog that decodes means what its examples say: Decode refuses one whose shape is wrong or
// whose examples disagree with its patterns.
func TestDecodeRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		old, new string
		want     string
	}{
		"unknown field":                  {"tier: definite", "tier: definite\n    severity: high", "field severity not found"},
		"unknown tier":                   {"tier: definite", "tier: certain", `tier "certain"`},
		"undeclared kind":                {"kind: cloud\n", "kind: clouds\n", `kind "clouds" is not one of the catalog's kinds`},
		"duplicate store":                {"id: dotenv-variant", "id: aws", "store aws: defined twice"},
		"duplicate kind":                 {"{id: dotenv, title: Environment files}", "{id: cloud, title: Again}", `kind "cloud"`},
		"bad id":                         {"id: aws", "id: AWS", `id "AWS"`},
		"no title":                       {"    title: AWS shared credentials\n", "", "store aws: no title"},
		"no patterns":                    {`match: [".aws/credentials"]`, "match: []", "store aws: no match patterns"},
		"bad pattern":                    {`match: [".aws/credentials"]`, `match: ["/.aws/credentials"]`, "store aws: match: pattern"},
		"bad exclude pattern":            {`exclude: [".env.example"]`, `exclude: [".env.[e]xample"]`, "store dotenv-variant: exclude: pattern"},
		"pattern listed twice":           {`match: ["credentials", "credentials.json"]`, `match: ["credentials", "credentials"]`, "listed twice"},
		"example not matched":            {`examples: ["/home/u/.aws/credentials"]`, `examples: ["/home/u/.aws/config"]`, `store aws: example "/home/u/.aws/config": not matched`},
		"example excluded":               {`examples: ["/repo/.env.local"]`, `examples: ["/repo/.env.example"]`, `store dotenv-variant: example "/repo/.env.example": not matched`},
		"example is another store":       {`examples: ["/srv/credentials", "/srv/credentials.json"]`, `examples: ["/srv/.aws/credentials", "/srv/credentials.json"]`, `store credentials-file: example "/srv/.aws/credentials": is store aws`},
		"pattern without an example":     {`examples: ["/srv/credentials", "/srv/credentials.json"]`, `examples: ["/srv/credentials"]`, `store credentials-file: match pattern "credentials.json": no example shows it`},
		"counterexample is a store":      {`counterexamples: ["/home/u/.aws/config"]`, `counterexamples: ["/home/u/credentials"]`, `store aws: counterexample "/home/u/credentials": is store credentials-file`},
		"exclude without counterexample": {`counterexamples: ["/repo/.env.example"]`, `counterexamples: ["/repo/README.md"]`, `store dotenv-variant: exclude pattern ".env.example": no counterexample shows it`},
		"no stores":                      {smallCatalog[strings.Index(smallCatalog, "stores:"):], "stores: []\n", "no stores"},
		"bad version":                    {"version: 1", "version: 0", "version 0"},
		"bad name":                       {"catalog: credential-stores", "catalog: Credential Stores", `catalog "Credential Stores"`},
		"name is not a string":           {"catalog: credential-stores", "catalog: [a]", "cannot unmarshal"},
	} {
		if !strings.Contains(smallCatalog, tc.old) {
			t.Fatalf("%s: the catalog does not contain %q", name, tc.old)
		}
		_, err := Decode([]byte(strings.Replace(smallCatalog, tc.old, tc.new, 1)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Decode = %v, want an error about %q", name, err, tc.want)
		}
	}
}

// credentialStores loads the catalog the rule pack ships.
func credentialStores(t *testing.T) *Catalog {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	c, err := Load(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "rules", "concepts", "credential-stores.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// variants are paths near path: the ones a careless pattern or expression gets wrong.
func variants(path string) []string {
	base := path[strings.LastIndexAny(path, `/\`)+1:]
	dir := path[:len(path)-len(base)]
	return []string{
		path,
		"/mnt/backup" + "/" + strings.TrimLeft(path, `/~`),
		strings.ReplaceAll(path, "/", `\`),
		strings.ReplaceAll(path, `\`, "/"),
		strings.ToUpper(path),
		strings.ToLower(path),
		path + ".bak",
		path + "~",
		path + "/",
		path + "/child",
		dir + "x" + base,
		dir + base + "x",
		dir + "sub/" + base,
		strings.Replace(path, "/", "//", 1),
		strings.Replace(path, ".", "x", 1),
		strings.TrimLeft(base, "."),
		base,
		"docs/" + base + ".md",
	}
}

// TestCredentialStoresExpressionsAgree is the conformance check of the generated expressions:
// for every selection a rule can ask for, the regular expressions accept exactly the paths the
// catalog's own matcher does, over every example and counterexample and the paths near them.
func TestCredentialStoresExpressionsAgree(t *testing.T) {
	c := credentialStores(t)
	var paths []string
	for _, s := range c.Stores {
		for _, p := range append(append([]string(nil), s.Examples...), s.Counterexamples...) {
			paths = append(paths, variants(p)...)
		}
	}
	selections := [][]string{nil}
	for _, k := range c.Kinds {
		selections = append(selections, []string{k.ID})
	}
	accepted := 0
	for _, tier := range []Tier{TierDefinite, TierLikely} {
		for _, kinds := range selections {
			selection, err := c.Select(tier, kinds...)
			if err != nil {
				t.Fatal(err)
			}
			type compiled struct{ include, exclude *regexp.Regexp }
			terms := make([]compiled, len(selection))
			for i, term := range selection {
				terms[i].include = regexp.MustCompile(term.Include)
				if term.Exclude != "" {
					terms[i].exclude = regexp.MustCompile(term.Exclude)
				}
			}
			for _, path := range paths {
				want := false
				comps := splitPath(path)
				for i := range c.Stores {
					s := &c.Stores[i]
					if tier.includes(s.Tier) && (kinds == nil || kinds[0] == s.Kind) && s.matchesComponents(comps) {
						want = true
					}
				}
				got := false
				for _, term := range terms {
					if term.include.MatchString(path) && (term.exclude == nil || !term.exclude.MatchString(path)) {
						got = true
					}
				}
				if got != want {
					t.Errorf("%s stores of kinds %q: the expression says %v for %q, the catalog %v", tier, kinds, got, path, want)
				}
				if got {
					accepted++
				}
			}
		}
	}
	// The check is of little worth if the paths near the examples were all rejected, or all taken.
	asked := 2 * len(selections) * len(paths)
	t.Logf("%d paths, %d selections: %d of %d answers were yes", len(paths), 2*len(selections), accepted, asked)
	if accepted == 0 || accepted == asked {
		t.Errorf("%d of %d answers were yes: the variants do not exercise the expressions", accepted, asked)
	}
}

// Every kind the catalog declares has a store, and the definite tier is not empty: a selection a
// rule asks for by kind or tier is never silently nothing.
func TestCredentialStoresKindsAndTiersAreUsed(t *testing.T) {
	c := credentialStores(t)
	for _, k := range c.Kinds {
		selection, err := c.Select(TierLikely, k.ID)
		if err != nil || len(selection) == 0 {
			t.Errorf("kind %s: no store (%v)", k.ID, err)
		}
	}
	if selection, _ := c.Select(TierDefinite); len(selection) == 0 {
		t.Error("no definite store")
	}
}
