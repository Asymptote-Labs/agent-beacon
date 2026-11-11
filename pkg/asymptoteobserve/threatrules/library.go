package threatrules

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
	"gopkg.in/yaml.v3"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/concepts"
)

// The definitions of a rule pack are the files beside its rules that say what its predicates
// mean: catalogs (*.catalog.yaml, see package concepts) and predicate definitions
// (*.predicates.yaml). They are part of the pack: found when its directory is loaded, installed
// into the rule store with its rules, and embedded with the baseline.
const (
	CatalogFileSuffix    = ".catalog.yaml"
	PredicatesFileSuffix = ".predicates.yaml"
)

// IsDefinitionFile reports whether a file name is one of a pack's definitions.
func IsDefinitionFile(name string) bool {
	return (strings.HasSuffix(name, CatalogFileSuffix) || strings.HasSuffix(name, PredicatesFileSuffix)) &&
		!strings.HasPrefix(name[strings.LastIndexAny(name, `/\`)+1:], "._")
}

// DefinitionFile is one file of a pack's definitions.
type DefinitionFile struct {
	// Name is the file's name or path; its suffix says which kind of definitions it holds.
	Name string
	Data []byte
}

// StoreName returns the file name a definitions file is kept under in a flat store: the name the
// file declares (its catalog, or its library of predicates) and its suffix, so that two versions
// of the same definitions are one file, as two versions of a rule are. The declared name admits
// no path separator, so the result is a safe single path component.
func (f DefinitionFile) StoreName() (string, error) {
	key, suffix := "catalog", CatalogFileSuffix
	switch {
	case strings.HasSuffix(f.Name, PredicatesFileSuffix):
		key, suffix = "library", PredicatesFileSuffix
	case !strings.HasSuffix(f.Name, CatalogFileSuffix):
		return "", fmt.Errorf("%s: not a definitions file (want %s or %s)", f.Name, CatalogFileSuffix, PredicatesFileSuffix)
	}
	var head map[string]any
	if err := yaml.Unmarshal(f.Data, &head); err != nil {
		return "", fmt.Errorf("%s: %w", f.Name, err)
	}
	name, _ := head[key].(string)
	if !idPattern.MatchString(name) {
		return "", fmt.Errorf("%s: the definitions have no name (%s: ...)", f.Name, key)
	}
	return name + suffix, nil
}

// predicatesFile is a *.predicates.yaml document.
type predicatesFile struct {
	Name    string `yaml:"library"`
	Version int    `yaml:"version"`
	// Spec is the spec version the file needs; a file that needs a newer one than the engine
	// supports is set aside, as a rule is.
	Spec        string                `yaml:"spec,omitempty"`
	Description string                `yaml:"description,omitempty"`
	Definitions []PredicateDefinition `yaml:"definitions"`
}

// PredicateDefinition is one predicate of a *.predicates.yaml file: a name, the parameters its
// body is written over, and fixtures that show what it is true for.
type PredicateDefinition struct {
	Name        string   `yaml:"name"`
	Params      []string `yaml:"params"`
	Description string   `yaml:"description"`
	// Body is a CEL expression over the parameters and nothing else: a predicate that needs
	// the event takes it as a parameter. It may call other predicates of the pack.
	Body  string          `yaml:"body"`
	Tests []PredicateTest `yaml:"tests"`
}

// PredicateTest is a fixture of a predicate: Match, an expression that calls it, must give
// Verdict over Events, as a single-event rule's match would. Match may be left out for a
// predicate whose one parameter is the event, e: it is then the predicate called with e.
type PredicateTest struct {
	Name    string         `yaml:"name"`
	Match   string         `yaml:"match,omitempty"`
	Verdict Verdict        `yaml:"verdict"`
	Events  []FixtureEvent `yaml:"events"`
}

// Predicate describes one predicate of a library, for listings and documentation.
type Predicate struct {
	Name string
	// Signature is how it is called: credential_read(e), credential_path(subject[, tier[, kind...]]).
	Signature   string
	Description string
	// Source is the definitions file it comes from.
	Source string
}

// Library is the predicates of one rule pack's definitions, and the CEL environment its rules
// compile against. A nil Library has no predicates: it is the engine's environment alone, which
// is what a pack without definitions gets. A Library is immutable and safe for concurrent use.
type Library struct {
	predicates map[string]*predicate
	// order lists the predicate names, sorted.
	order []string
	env   *cel.Env
	tests []predicateTest
	info  []Predicate
	// catalogs are the pack's catalogs, in the order of their files' names.
	catalogs []*concepts.Catalog
}

// predicateTest is one fixture Check runs: a definition's, or a catalog's examples, which stand
// for each predicate the catalog provides.
type predicateTest struct {
	predicate string
	test      PredicateTest
	catalog   *concepts.Catalog
}

// CatalogExamples is the fixture name Check reports a catalog's examples under.
const CatalogExamples = "catalog examples"

// LoadLibraryDir builds the library of the definitions under root, searched recursively.
func LoadLibraryDir(root string) (*Library, []SkippedRule, error) {
	return LoadLibraryFS(os.DirFS(root))
}

// LoadLibraryFS builds the library of the definitions in fsys, searched recursively.
func LoadLibraryFS(fsys fs.FS) (*Library, []SkippedRule, error) {
	var files []DefinitionFile
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !IsDefinitionFile(path) {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		files = append(files, DefinitionFile{Name: path, Data: data})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return NewLibrary(files)
}

// NewLibrary builds a library from definition files. It checks what every load must be able to
// rely on: each file's shape, a catalog's patterns, a predicate's name and body, a predicate
// defined twice, predicates that call each other in a circle. It does not run a catalog's
// examples or a predicate's fixtures: Check does, as CheckRule runs a rule's. A file that
// declares a spec newer than SupportedSpec is left out and returned in skipped, so that an older
// engine keeps the rest of a newer pack; a rule that calls one of its predicates declares that
// spec too and is skipped with it.
//
// It returns a nil Library, which is valid, when the files define no predicate.
func NewLibrary(files []DefinitionFile) (lib *Library, skipped []SkippedRule, err error) {
	files = slices.Clone(files)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	l := &Library{predicates: map[string]*predicate{}}
	var definitions []PredicateDefinition
	add := func(p *predicate, description, signature string) error {
		switch {
		case !predicateNamePattern.MatchString(p.name):
			return fmt.Errorf("%s: predicate %q: want a name like credential_read", p.source, p.name)
		case slices.Contains(reservedNames, p.name):
			return fmt.Errorf("%s: predicate %q: the name is CEL's own", p.source, p.name)
		case l.predicates[p.name] != nil:
			return fmt.Errorf("%s: predicate %s is already defined in %s", p.source, p.name, l.predicates[p.name].source)
		}
		l.predicates[p.name] = p
		l.info = append(l.info, Predicate{Name: p.name, Signature: signature, Description: description, Source: p.source})
		return nil
	}
	// declared maps each catalog and each library of predicates to the file that declares it.
	declared := map[string]string{}
	declare := func(what, name, file string) error {
		if prev, dup := declared[what+" "+name]; dup {
			return fmt.Errorf("%s: %s %s is already declared by %s", file, what, name, prev)
		}
		declared[what+" "+name] = file
		return nil
	}
	for _, file := range files {
		var head struct {
			Spec string `yaml:"spec"`
		}
		if yaml.Unmarshal(file.Data, &head) == nil {
			if err := checkSpecSupported(file.Name, head.Spec); err != nil {
				if unsupported, ok := AsUnsupportedSpec(err); ok {
					skipped = append(skipped, SkippedRule{Path: file.Name, Err: unsupported})
					continue
				}
				return nil, nil, fmt.Errorf("%s: %w", file.Name, err)
			}
		}
		switch {
		case strings.HasSuffix(file.Name, CatalogFileSuffix):
			catalog, err := concepts.Decode(file.Data)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", file.Name, err)
			}
			if err := declare("catalog", catalog.Name, file.Name); err != nil {
				return nil, nil, err
			}
			l.catalogs = append(l.catalogs, catalog)
			for _, p := range []struct {
				name, about string
				render      func(concepts.Selection, string) string
			}{
				{catalog.Predicates.Path, "a path that is one of the catalog's stores of files", concepts.Selection.PathCEL},
				{catalog.Predicates.Command, "a parsed command that reads one of the catalog's stores of commands", concepts.Selection.CommandCEL},
				{catalog.Predicates.Mention, "a text that names one of the catalog's stores of files", concepts.Selection.MentionCEL},
				{catalog.Predicates.Redacted, "a command line in which redaction took the name of a command that reads one of the catalog's stores of commands", concepts.Selection.RedactedCEL},
			} {
				if p.name == "" {
					continue
				}
				description := fmt.Sprintf("True for %s (%s): of either tier, or of the tier and the kinds given after it.", p.about, catalog.Name)
				if err := add(&predicate{name: p.name, source: file.Name, catalog: catalog, render: p.render}, description, p.name+"("+subjectParam+"[, tier[, kind...]])"); err != nil {
					return nil, nil, err
				}
				l.tests = append(l.tests, predicateTest{predicate: p.name, test: PredicateTest{Name: CatalogExamples, Verdict: VerdictMatch}, catalog: catalog})
			}
		case strings.HasSuffix(file.Name, PredicatesFileSuffix):
			decoded, err := decodePredicatesFile(file.Data)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", file.Name, err)
			}
			if err := declare("library", decoded.Name, file.Name); err != nil {
				return nil, nil, err
			}
			for _, d := range decoded.Definitions {
				p := &predicate{name: d.Name, source: file.Name, params: d.Params, text: d.Body}
				if err := add(p, strings.TrimSpace(d.Description), d.Name+"("+strings.Join(d.Params, ", ")+")"); err != nil {
					return nil, nil, err
				}
				definitions = append(definitions, d)
			}
		default:
			return nil, nil, fmt.Errorf("%s: not a definitions file (want %s or %s)", file.Name, CatalogFileSuffix, PredicatesFileSuffix)
		}
	}
	if len(l.predicates) == 0 {
		return nil, skipped, nil
	}
	for name := range l.predicates {
		l.order = append(l.order, name)
	}
	sort.Strings(l.order)
	sort.Slice(l.info, func(i, j int) bool { return l.info[i].Name < l.info[j].Name })

	base, err := Env()
	if err != nil {
		return nil, nil, fmt.Errorf("build CEL env: %w", err)
	}
	if l.env, err = l.extend(base); err != nil {
		return nil, nil, fmt.Errorf("build CEL env: %w", err)
	}
	if err := l.resolve(base, definitions); err != nil {
		return nil, nil, err
	}
	for _, d := range definitions {
		for _, test := range d.Tests {
			if test.Match == "" {
				test.Match = d.Name + "(" + eventVar + ")"
			}
			l.tests = append(l.tests, predicateTest{predicate: d.Name, test: test})
		}
	}
	return l, skipped, nil
}

func decodePredicatesFile(data []byte) (*predicatesFile, error) {
	var f predicatesFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	switch {
	case !idPattern.MatchString(f.Name):
		return nil, fmt.Errorf("library %q: want a name like credentials", f.Name)
	case f.Version < 1:
		return nil, fmt.Errorf("version %d: want 1 or more", f.Version)
	case len(f.Definitions) == 0:
		return nil, fmt.Errorf("no definitions")
	}
	for _, d := range f.Definitions {
		if err := d.validate(); err != nil {
			return nil, fmt.Errorf("predicate %s: %w", d.Name, err)
		}
	}
	return &f, nil
}

func (d PredicateDefinition) validate() error {
	switch {
	case len(d.Params) == 0:
		return fmt.Errorf("no params: a predicate reads only what it is given, so it is given something")
	case strings.TrimSpace(d.Description) == "":
		return fmt.Errorf("no description")
	case strings.TrimSpace(d.Body) == "":
		return fmt.Errorf("no body")
	case len(d.Tests) == 0:
		return fmt.Errorf("at least one test is required")
	}
	for i, param := range d.Params {
		if !parameterPattern.MatchString(param) || slices.Contains(d.Params[:i], param) {
			return fmt.Errorf("param %q: want distinct names like e or path", param)
		}
	}
	names := map[string]bool{}
	for _, test := range d.Tests {
		switch {
		case test.Name == "" || names[test.Name]:
			return fmt.Errorf("test %q: want a name of its own", test.Name)
		case test.Verdict != VerdictMatch && test.Verdict != VerdictNoMatch:
			return fmt.Errorf("test %s: verdict %q: want %s or %s", test.Name, test.Verdict, VerdictMatch, VerdictNoMatch)
		case len(test.Events) == 0:
			return fmt.Errorf("test %s: no events", test.Name)
		case test.Match == "" && !slices.Equal(d.Params, []string{eventVar}):
			return fmt.Errorf("test %s: no match: say how the predicate is called (only a predicate of the event alone, params: [%s], has a default)", test.Name, eventVar)
		}
		names[test.Name] = true
	}
	return nil
}

// resolve parses every definition's body, in an order that puts a predicate after the ones it
// calls, so that each call in a body expands to a body already prepared.
func (l *Library) resolve(base *cel.Env, definitions []PredicateDefinition) error {
	for _, d := range definitions {
		p := l.predicates[d.Name]
		// With the engine's environment alone a call to a predicate stays a call, which is how
		// the ones a body uses are found.
		parsed, iss := base.Parse(p.text)
		if iss != nil && iss.Err() != nil {
			return fmt.Errorf("%s: predicate %s: body: %w", p.source, p.name, iss.Err())
		}
		for _, name := range calls(parsed.NativeRep().Expr()) {
			if l.predicates[name] != nil {
				p.uses = append(p.uses, name)
			}
		}
	}
	const (
		unseen = iota
		open
		done
	)
	state := map[string]int{}
	var visit func(p *predicate, path []string) error
	visit = func(p *predicate, path []string) error {
		switch state[p.name] {
		case done:
			return nil
		case open:
			return fmt.Errorf("%s: predicate %s calls itself: %s", p.source, p.name, strings.Join(append(path, p.name), " -> "))
		}
		state[p.name] = open
		for _, name := range p.uses {
			if err := visit(l.predicates[name], append(path, p.name)); err != nil {
				return err
			}
		}
		state[p.name] = done
		if p.catalog != nil {
			return nil
		}
		parsed, iss := l.env.Parse(p.text)
		if iss != nil && iss.Err() != nil {
			return fmt.Errorf("%s: predicate %s: body: %w", p.source, p.name, iss.Err())
		}
		body := parsed.NativeRep().Expr()
		free := hygienic(p.name, body)
		for _, name := range free {
			if !slices.Contains(p.params, name) {
				return fmt.Errorf("%s: predicate %s: body reads %q, which is not one of its params (%s): a predicate reads only what it is given", p.source, p.name, name, strings.Join(p.params, ", "))
			}
		}
		for _, param := range p.params {
			if !slices.Contains(free, param) {
				return fmt.Errorf("%s: predicate %s: param %q is not used", p.source, p.name, param)
			}
		}
		p.body = body
		return nil
	}
	for _, name := range l.order {
		if err := visit(l.predicates[name], nil); err != nil {
			return err
		}
	}
	return nil
}

// environment returns the CEL environment expressions compile against with this library.
func (l *Library) environment() (*cel.Env, error) {
	if l == nil {
		return Env()
	}
	return l.env, nil
}

// extend returns env with the library's predicates added: each is a macro, expanded where an
// expression calls it.
func (l *Library) extend(env *cel.Env) (*cel.Env, error) {
	if l == nil {
		return env, nil
	}
	macros := make([]cel.Macro, len(l.order))
	for i, name := range l.order {
		macros[i] = l.predicates[name].macro()
	}
	return env.Extend(cel.Macros(macros...))
}

// undeclared finds the name in the checker's report of a call to a function it does not know.
var undeclared = regexp.MustCompile(`undeclared reference to '([a-z][a-z0-9_]*)'`)

// explain adds, to the error of an expression that calls something the library does not define,
// what is most likely wrong: the rule was loaded without its pack's definitions.
func (l *Library) explain(err error) error {
	m := undeclared.FindStringSubmatch(err.Error())
	if m == nil || l != nil && l.predicates[m[1]] != nil {
		return err
	}
	return fmt.Errorf("%w\n%s is not a predicate of the definitions this rule was loaded with: a pack's definitions are its %s and %s files, found when the pack's directory is loaded", err, m[1], CatalogFileSuffix, PredicatesFileSuffix)
}

// CompileMatch is the package's CompileMatch for an expression that may call the library's
// predicates.
func (l *Library) CompileMatch(expr string) (cel.Program, error) {
	prog, _, err := l.compileMatch(expr)
	return prog, err
}

// Predicates lists the library's predicates by name.
func (l *Library) Predicates() []Predicate {
	if l == nil {
		return nil
	}
	return slices.Clone(l.info)
}

// uses returns the predicates expr calls by name, or nil when it calls none or does not parse.
func (l *Library) uses(expr string) []string {
	if l == nil {
		return nil
	}
	base, err := Env()
	if err != nil {
		return nil
	}
	parsed, iss := base.Parse(expr)
	if iss != nil && iss.Err() != nil {
		return nil
	}
	var used []string
	for _, name := range calls(parsed.NativeRep().Expr()) {
		if l.predicates[name] != nil {
			used = append(used, name)
		}
	}
	return used
}

// DecodeRule is the package's DecodeRule for a rule of this library's pack: the rule's
// expressions may call the library's predicates.
func (l *Library) DecodeRule(data []byte) (*Rule, error) {
	rule, err := DecodeRule(data)
	if err != nil {
		return nil, err
	}
	rule.lib = l
	return rule, nil
}

// LoadRule is the package's LoadRule for a rule of this library's pack.
func (l *Library) LoadRule(path string) (*Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rule, err := l.DecodeRule(data)
	if err != nil {
		return nil, fmt.Errorf("%s: decode: %w", path, err)
	}
	if err := rule.Validate(); err != nil {
		return nil, fmt.Errorf("%s: validate: %w", path, err)
	}
	return rule, nil
}

// Check runs the library's fixtures and returns one result per fixture, in the order of the
// predicates' names: every fixture of every predicate definition, and for each predicate a
// catalog provides, the catalog's examples (CatalogExamples). A fixture whose expression does
// not compile against the event schema is an error of that fixture, which is how a definition's
// body is held to the schema. It is to a pack's definitions what CheckRule is to a rule.
func (l *Library) Check() []FixtureResult {
	if l == nil {
		return nil
	}
	results := make([]FixtureResult, 0, len(l.tests))
	examples := map[*concepts.Catalog]error{}
	for _, t := range l.tests {
		res := FixtureResult{RuleID: t.predicate, Fixture: t.test.Name, Expected: t.test.Verdict, Actual: VerdictNoMatch}
		if t.catalog != nil {
			err, checked := examples[t.catalog]
			if !checked {
				err = t.catalog.Check()
				examples[t.catalog] = err
			}
			res.Err = err
			res.Actual = VerdictMatch
			results = append(results, res)
			continue
		}
		prog, _, err := l.compileMatch(t.test.Match)
		if err != nil {
			res.Err = err
		}
		for _, event := range t.test.Events {
			if res.Err != nil {
				break
			}
			matched, err := EvalMatch(prog, event.Event)
			if err != nil {
				res.Err = err
				break
			}
			if matched {
				res.Actual = VerdictMatch
				break
			}
		}
		results = append(results, res)
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].RuleID < results[j].RuleID })
	return results
}
