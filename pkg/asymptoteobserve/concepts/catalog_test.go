package concepts

import (
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
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
  - {id: keychain, title: Keychains}
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
  - id: aws-export
    title: AWS credentials exported by the CLI
    kind: cloud
    tier: definite
    commands:
      - {name: aws, words: [configure, export-credentials]}
    examples: ["aws configure export-credentials"]
    counterexamples: ["aws configure list"]
  - id: macos-keychain
    title: macOS keychain password
    kind: keychain
    tier: definite
    commands:
      - {name: security, words: ["find-{generic,internet}-password"], flags: [w, g]}
    examples: ["security find-generic-password -s GitHub -w"]
    counterexamples: ["security find-generic-password -s GitHub"]
  - id: keychain-listing
    title: macOS keychain items listed
    kind: keychain
    tier: likely
    commands:
      - {name: security, flags: ["--list*"]}
      - {name: "secret-tool{,.exe}"}
    examples: ["security --list-all", "secret-tool search x y"]
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
		"aws":                      "", // a command store is no file
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

// A command is matched by its name, by words that follow each other after it, and by an option
// of which one must be there.
func TestLookupCommand(t *testing.T) {
	c, err := Decode([]byte(smallCatalog))
	if err != nil {
		t.Fatal(err)
	}
	for command, want := range map[string]string{
		"aws configure export-credentials":                 "aws-export",
		"aws --profile prod configure export-credentials":  "aws-export",
		"AWS Configure Export-Credentials --format env":    "aws-export",
		"aws configure list export-credentials":            "", // the words do not follow each other
		"aws export-credentials configure":                 "",
		"aws configure export-credentials-now":             "",
		"awsx configure export-credentials":                "",
		"security find-generic-password -s GitHub -w":      "macos-keychain",
		"security find-internet-password -g -s github.com": "macos-keychain",
		"security find-generic-password -aw me":            "macos-keychain", // w in a cluster
		"security -w find-generic-password":                "macos-keychain", // anywhere after the name
		"security find-generic-password -s GitHub":         "",
		"security find-generic-password -s GitHub -W":      "", // a short option keeps its case
		"security find-generic-password -s GitHub --w":     "",
		"security find-generic-password -s w":              "",
		"security find-generic-password -s x-w":            "",
		"security find-certificate -w":                     "",
		"security --list-all":                              "keychain-listing",
		"security --LIST=all":                              "keychain-listing", // an option word, with a value
		"security --lis":                                   "",
		"security list --x=--list":                         "",
		"security --list-all find-generic-password -g":     "macos-keychain", // the definite store first
		"secret-tool":                "keychain-listing",
		"secret-tool.exe lookup a b": "keychain-listing",
		"secret-tools":               "",
	} {
		words := strings.Fields(command)
		got := ""
		if s, ok := c.LookupCommand(words[0], words[1:]); ok {
			got = s.ID
		}
		if got != want {
			t.Errorf("LookupCommand(%q) = %q, want %q", command, got, want)
		}
	}
}

// A selection is the stores of a tier and kind, and its renderings carry the same expressions. A
// store that excludes paths keeps its exclusion to itself.
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
	var (
		export   = CommandTerm{Name: `(?i)^aws$`, Words: `(?im)^configure\nexport-credentials$`}
		keychain = CommandTerm{Name: `(?i)^security$`, Words: `(?im)^find-(?:generic|internet)-password$`, Flags: `(?m)^(?:-[A-Za-z0-9]*[wg][A-Za-z0-9]*)$`}
		listing  = CommandTerm{Name: `(?i)^security$`, Flags: `(?m)^(?:(?i:--list[^\n]*)(?:=[^\n]*)?)$`}
		tool     = CommandTerm{Name: `(?i)^secret-tool(?:|\.exe)$`}
	)
	for _, tc := range []struct {
		tier  Tier
		kinds []string
		want  Selection
	}{
		{TierDefinite, nil, Selection{Paths: []PathTerm{{Include: aws}}, Commands: []CommandTerm{export, keychain}}},
		{TierLikely, nil, Selection{Paths: []PathTerm{{Include: both}, {Include: variant, Exclude: example}}, Commands: []CommandTerm{export, keychain, listing, tool}}},
		{TierLikely, []string{"generic"}, Selection{Paths: []PathTerm{{Include: generic}}}},
		{TierLikely, []string{"dotenv", "cloud"}, Selection{Paths: []PathTerm{{Include: aws}, {Include: variant, Exclude: example}}, Commands: []CommandTerm{export}}},
		{TierDefinite, []string{"keychain"}, Selection{Commands: []CommandTerm{keychain}}},
		{TierDefinite, []string{"dotenv"}, Selection{}},
	} {
		if got, err := c.Select(tc.tier, tc.kinds...); err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Select(%s, %q) =\n%q, %v; want\n%q", tc.tier, tc.kinds, got, err, tc.want)
		}
	}
	if _, err := c.Select(TierLikely, "kubernetes"); err == nil {
		t.Error("Select with a kind the catalog does not have: want an error")
	}
	if _, err := c.Select("certain"); err == nil {
		t.Error("Select with an unknown tier: want an error")
	}

	all, _ := c.Select(TierLikely)
	if got, want := all.PathCEL("e.file.path"), `(e.file.path.matches("(?i)(?:^|[/\\\\])(?:\\.aws[/\\\\]credentials$|credentials$|credentials\\.json$)") || `+
		`(e.file.path.matches("(?i)(?:^|[/\\\\])(?:\\.env\\.[^/\\\\]*$)") && !e.file.path.matches("(?i)(?:^|[/\\\\])(?:\\.env\\.example$)")))`; got != want {
		t.Errorf("PathCEL =\n%s\nwant\n%s", got, want)
	}
	if got, want := all.PathKQL("FilePath"), `(FilePath matches regex @"`+both+`" or (FilePath matches regex @"`+variant+`" and not(FilePath matches regex @"`+example+`")))`; got != want {
		t.Errorf("PathKQL =\n%s\nwant\n%s", got, want)
	}
	if got, want := all.CommandCEL("c"), `((c.name.matches("(?i)^aws$") && c.args.matches("(?im)^configure\\nexport-credentials$")) || `+
		`(c.name.matches("(?i)^security$") && c.args.matches("(?im)^find-(?:generic|internet)-password$") && c.args.matches("(?m)^(?:-[A-Za-z0-9]*[wg][A-Za-z0-9]*)$")) || `+
		`(c.name.matches("(?i)^security$") && c.args.matches("(?m)^(?:(?i:--list[^\\n]*)(?:=[^\\n]*)?)$")) || `+
		`c.name.matches("(?i)^secret-tool(?:|\\.exe)$"))`; got != want {
		t.Errorf("CommandCEL =\n%s\nwant\n%s", got, want)
	}
	definite, _ := c.Select(TierDefinite)
	if got, want := definite.PathKQL("FilePath"), `FilePath matches regex @"`+aws+`"`; got != want {
		t.Errorf("PathKQL = %s, want %s", got, want)
	}
	none, _ := c.Select(TierDefinite, "dotenv")
	if none.PathCEL("p") != "false" || none.PathKQL("p") != "false" || none.CommandCEL("c") != "false" {
		t.Errorf("an empty selection = %q, %q, %q; want false", none.PathCEL("p"), none.PathKQL("p"), none.CommandCEL("c"))
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
		"no patterns":                    {`match: [".aws/credentials"]`, "match: []", "store aws: no match patterns and no commands"},
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

		"paths and commands":              {"      - {name: aws, words: [configure, export-credentials]}\n", "      - {name: aws, words: [configure, export-credentials]}\n    match: [\".aws/x\"]\n", "store aws-export: both paths and commands"},
		"command without a name":          {"{name: aws, words: [configure, export-credentials]}", "{words: [configure, export-credentials]}", "store aws-export: commands[0]: name: empty pattern"},
		"bad command name":                {"{name: aws, words: [configure, export-credentials]}", "{name: \"aws[2]\"}", "store aws-export: commands[0]: name: pattern"},
		"bad word":                        {"words: [configure, export-credentials]", "words: [configure, \"export-{credentials\"]", "store aws-export: commands[0]: words: pattern"},
		"flag that is no option":          {"flags: [w, g]", "flags: [w, generic]", `store macos-keychain: commands[0]: flags: "generic": want one letter`},
		"flag listed twice":               {"flags: [w, g]", "flags: [w, w]", `flags: "w" listed twice`},
		"unknown command field":           {"{name: aws, words: [configure, export-credentials]}", "{name: aws, subcommand: configure}", "field subcommand not found"},
		"command example not matched":     {`examples: ["aws configure export-credentials"]`, `examples: ["aws configure list"]`, `store aws-export: example "aws configure list": not matched`},
		"command example does not parse":  {`examples: ["aws configure export-credentials"]`, `examples: ["aws configure 'export-credentials"]`, `store aws-export: example "aws configure 'export-credentials"`},
		"command example is another":      {`examples: ["security --list-all", "secret-tool search x y"]`, `examples: ["security --list-all find-generic-password -w", "secret-tool search x y"]`, `store keychain-listing: example "security --list-all find-generic-password -w": is store macos-keychain`},
		"command without an example":      {`examples: ["security --list-all", "secret-tool search x y"]`, `examples: ["security --list-all"]`, `store keychain-listing: commands[1] (secret-tool{,.exe}): no example shows it`},
		"command counterexample is one":   {`counterexamples: ["aws configure list"]`, `counterexamples: ["echo $(security find-generic-password -w)"]`, `store aws-export: counterexample "echo $(security find-generic-password -w)": is store macos-keychain`},
		"path example for a command":      {`examples: ["security find-generic-password -s GitHub -w"]`, `examples: ["/home/u/Library/Keychains/login.keychain-db"]`, "not matched"},
		"command counterexample no parse": {`counterexamples: ["security find-generic-password -s GitHub"]`, `counterexamples: ["security 'x"]`, `store macos-keychain: counterexample "security 'x"`},
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

// selections are the selections a rule can ask for by kind: every kind, and each one.
func selections(c *Catalog) [][]string {
	all := [][]string{nil}
	for _, k := range c.Kinds {
		all = append(all, []string{k.ID})
	}
	return all
}

// inSelection reports whether a store is one a selection takes.
func inSelection(s *Store, tier Tier, kinds []string) bool {
	return tier.includes(s.Tier) && (kinds == nil || kinds[0] == s.Kind)
}

// pathVariants are paths near path: the ones a careless pattern or expression gets wrong.
func pathVariants(path string) []string {
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

// TestCredentialStoresPathExpressionsAgree is the conformance check of the expressions generated
// for files: for every selection a rule can ask for, the regular expressions accept exactly the
// paths the catalog's own matcher does, over every example and counterexample and the paths near
// them.
func TestCredentialStoresPathExpressionsAgree(t *testing.T) {
	c := credentialStores(t)
	var paths []string
	for _, s := range c.Stores {
		if len(s.Commands) > 0 {
			continue
		}
		for _, p := range slices.Concat(s.Examples, s.Counterexamples) {
			paths = append(paths, pathVariants(p)...)
		}
	}
	accepted, asked := 0, 0
	for _, tier := range []Tier{TierDefinite, TierLikely} {
		for _, kinds := range selections(c) {
			selection, err := c.Select(tier, kinds...)
			if err != nil {
				t.Fatal(err)
			}
			type compiled struct{ include, exclude *regexp.Regexp }
			terms := make([]compiled, len(selection.Paths))
			for i, term := range selection.Paths {
				terms[i].include = regexp.MustCompile(term.Include)
				if term.Exclude != "" {
					terms[i].exclude = regexp.MustCompile(term.Exclude)
				}
			}
			for _, path := range paths {
				want := false
				comps := splitPath(path)
				for i := range c.Stores {
					if s := &c.Stores[i]; inSelection(s, tier, kinds) && s.matchesComponents(comps) {
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
				asked++
				if got {
					accepted++
				}
			}
		}
	}
	// The check is of little worth if the paths near the examples were all rejected, or all taken.
	t.Logf("%d paths: %d of %d answers were yes", len(paths), accepted, asked)
	if accepted == 0 || accepted == asked {
		t.Errorf("%d of %d answers were yes: the variants do not exercise the expressions", accepted, asked)
	}
}

// commandVariants are commands near one: the ones a careless pattern or expression gets wrong.
func commandVariants(inv invocation) []invocation {
	out := []invocation{
		inv,
		{strings.ToUpper(inv.name), inv.args},
		{inv.name + "x", inv.args},
		{"x" + inv.name, inv.args},
		{inv.name, nil},
		{inv.name, []string{strings.Join(inv.args, " ")}}, // the words as one quoted word
	}
	reversed := slices.Clone(inv.args)
	slices.Reverse(reversed)
	out = append(out, invocation{inv.name, reversed})
	for i, arg := range inv.args {
		without := slices.Delete(slices.Clone(inv.args), i, i+1)
		changed := func(to string) []string {
			args := slices.Clone(inv.args)
			args[i] = to
			return args
		}
		out = append(out,
			invocation{inv.name, without},
			invocation{inv.name, slices.Insert(slices.Clone(inv.args), i, "--other")},
			invocation{inv.name, slices.Insert(slices.Clone(inv.args), i, "")}, // a word of unknown value
			invocation{inv.name, changed(strings.ToUpper(arg))},
			invocation{inv.name, changed(arg + "x")},
			invocation{inv.name, changed("x" + arg)},
			invocation{inv.name, changed(arg + "=value")},
			invocation{inv.name, changed("-" + arg)},
			invocation{inv.name, changed(strings.TrimLeft(arg, "-"))},
			invocation{inv.name, changed(arg + " " + arg)},
		)
	}
	return out
}

// TestCredentialStoresCommandExpressionsAgree is the same check for commands: the expressions
// over a command's name and its arguments as text accept exactly the commands the catalog's own
// matcher does, over the commands of every example and counterexample and the ones near them.
func TestCredentialStoresCommandExpressionsAgree(t *testing.T) {
	c := credentialStores(t)
	var commands []invocation
	for _, s := range c.Stores {
		if len(s.Commands) == 0 {
			continue
		}
		for _, line := range slices.Concat(s.Examples, s.Counterexamples) {
			parsed, err := invocations(line)
			if err != nil {
				t.Fatal(err)
			}
			for _, inv := range parsed {
				commands = append(commands, commandVariants(inv)...)
			}
		}
	}
	accepted, asked := 0, 0
	for _, tier := range []Tier{TierDefinite, TierLikely} {
		for _, kinds := range selections(c) {
			selection, err := c.Select(tier, kinds...)
			if err != nil {
				t.Fatal(err)
			}
			type compiled struct{ name, words, flags *regexp.Regexp }
			terms := make([]compiled, len(selection.Commands))
			for i, term := range selection.Commands {
				terms[i].name = regexp.MustCompile(term.Name)
				if term.Words != "" {
					terms[i].words = regexp.MustCompile(term.Words)
				}
				if term.Flags != "" {
					terms[i].flags = regexp.MustCompile(term.Flags)
				}
			}
			for _, inv := range commands {
				want := false
				for i := range c.Stores {
					if s := &c.Stores[i]; inSelection(s, tier, kinds) && s.matchesCommand(inv.name, inv.args) {
						want = true
					}
				}
				// The arguments as a rule reads them: one word per line.
				text := strings.Join(inv.args, "\n")
				got := false
				for _, term := range terms {
					if term.name.MatchString(inv.name) && (term.words == nil || term.words.MatchString(text)) && (term.flags == nil || term.flags.MatchString(text)) {
						got = true
					}
				}
				if got != want {
					t.Errorf("%s stores of kinds %q: the expression says %v for %s %q, the catalog %v", tier, kinds, got, inv.name, inv.args, want)
				}
				asked++
				if got {
					accepted++
				}
			}
		}
	}
	t.Logf("%d commands: %d of %d answers were yes", len(commands), accepted, asked)
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
		if err != nil || len(selection.Paths)+len(selection.Commands) == 0 {
			t.Errorf("kind %s: no store (%v)", k.ID, err)
		}
	}
	if selection, _ := c.Select(TierDefinite); len(selection.Paths) == 0 || len(selection.Commands) == 0 {
		t.Error("no definite store of files, or none of commands")
	}
}
