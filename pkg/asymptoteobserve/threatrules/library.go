package threatrules

import (
	"bytes"
	"fmt"
	"os"
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

// DefinitionFile is one file of a pack's definitions.
type DefinitionFile struct {
	// Name is the file's name or path; its suffix says which kind of definitions it holds.
	Name string
	Data []byte
	// head is what the file declares about itself, set by ReadDefinitionFile so that NewLibrary
	// does not read it again; NewLibrary reads it of a file built without.
	head *Head
}

// ReadDefinitionFile reads what the definitions file name, whose content is data, declares about
// itself (ReadHead), once: the file it returns carries it to NewLibrary.
func ReadDefinitionFile(name string, data []byte) (DefinitionFile, Head, error) {
	head, err := ReadHead(name, data)
	if err != nil {
		return DefinitionFile{}, head, err
	}
	return DefinitionFile{Name: name, Data: data, head: &head}, head, nil
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
	// Source is the definitions file it comes from, and Catalog the catalog that provides it, ""
	// for a predicate a *.predicates.yaml file defines.
	Source, Catalog string
}

// Library is the predicates of one rule pack's definitions, and the CEL environment its rules
// compile against. A nil Library has no predicates: it is the engine's environment alone, which
// is what a pack without definitions gets. A Library's definitions never change, and it is safe
// for concurrent use; it remembers the expressions it has compiled (matches).
type Library struct {
	predicates map[string]*predicate
	// order lists the predicate names, sorted.
	order []string
	env   *cel.Env
	tests []predicateTest
	info  []Predicate
	// catalogs are the pack's catalogs, in the order of their files' names, and sources the files.
	catalogs []*concepts.Catalog
	sources  []string
	// later are the predicates that need a newer spec than this engine supports: those of a
	// definitions file it set aside, and those whose body calls one. What calls one is set aside
	// with it (Rule.Validate returns an *UnsupportedSpecError).
	later   map[string]laterPredicate
	matches matchCache
}

// laterPredicate is a predicate set aside for a newer spec: the predicates from the one called
// to the one a file set aside defines ("a -> b"), that file, and the spec it declares.
type laterPredicate struct {
	chain, source, required string
}

// predicateTest is one fixture of a predicate definition, which Check runs.
type predicateTest struct {
	predicate, source string
	test              PredicateTest
}

// DefinitionResult is the outcome of one check Library.Check runs: a fixture of a predicate
// definition, or a catalog held to its examples (concepts.Catalog.Check).
type DefinitionResult struct {
	// Catalog is set for a catalog's examples. Name is then the catalog's name, and otherwise
	// the predicate's. Source is the definitions file.
	Catalog      bool
	Name, Source string
	// Fixture is a predicate's fixture, and Expected and Actual its verdicts.
	Fixture          string
	Expected, Actual Verdict
	Err              error
}

// OK reports whether the check passed: the catalog's examples hold, or the fixture produced its
// declared verdict without error.
func (r DefinitionResult) OK() bool {
	return r.Err == nil && (r.Catalog || r.Actual == r.Expected)
}

func (r DefinitionResult) String() string {
	switch {
	case r.Catalog && r.Err != nil:
		return fmt.Sprintf("catalog %s (%s): %v", r.Name, r.Source, r.Err)
	case r.Catalog:
		return fmt.Sprintf("catalog %s (%s)", r.Name, r.Source)
	case r.Err != nil:
		return fmt.Sprintf("predicate %s/%s: error: %v", r.Name, r.Fixture, r.Err)
	}
	return fmt.Sprintf("predicate %s/%s: expected %s, got %s", r.Name, r.Fixture, r.Expected, r.Actual)
}

// NewLibrary builds a library from definition files. It checks what every load must be able to
// rely on: each file's shape, a catalog's patterns, a predicate's name and body, a predicate
// defined twice, predicates that call each other in a circle. It does not run a catalog's
// examples or a predicate's fixtures: Check does, as CheckRule runs a rule's. A file that
// declares a spec newer than SupportedSpec is left out and returned in skipped, so that an older
// engine keeps the rest of a newer pack; a rule that calls one of its predicates is skipped with
// it.
//
// It returns a nil Library, which is valid, when there are no definitions: no file, or none this
// engine loads.
func NewLibrary(files []DefinitionFile) (lib *Library, skipped []SkippedRule, err error) {
	files = slices.Clone(files)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	l := &Library{predicates: map[string]*predicate{}, later: map[string]laterPredicate{}}
	var definitions []PredicateDefinition
	add := func(p *predicate, info Predicate) error {
		switch {
		case !concepts.PredicateNamePattern.MatchString(p.name):
			return fmt.Errorf("%s: predicate %q: want a name like credential_read", p.source, p.name)
		case celsOwn(p.name):
			return fmt.Errorf("%s: predicate %q: the name is CEL's own", p.source, p.name)
		case l.predicates[p.name] != nil:
			return fmt.Errorf("%s: predicate %s is already defined in %s", p.source, p.name, l.predicates[p.name].source)
		}
		l.predicates[p.name] = p
		info.Name, info.Source = p.name, p.source
		l.info = append(l.info, info)
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
		kind, ok := kindOf(file.Name)
		if !ok || !kind.Definitions() {
			return nil, nil, fmt.Errorf("%s: not a definitions file (want %s or %s)", file.Name, CatalogFileSuffix, PredicatesFileSuffix)
		}
		// Predicates exist since v1.3, so that is what a definitions file needs at least.
		spec := specV1_3
		head := file.head
		if head == nil {
			if read, err := readHead(kind, file.Data); err == nil {
				head = &read
			}
		}
		if head != nil {
			declaredSpec, err := parseSpec(head.Spec)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", file.Name, err)
			}
			if unsupported := head.Unsupported(); unsupported != nil {
				skipped = append(skipped, SkippedRule{Path: file.Name, Err: unsupported})
				for _, name := range declaredPredicates(file.Data) {
					if _, dup := l.later[name]; !dup {
						l.later[name] = laterPredicate{chain: name, source: file.Name, required: head.Spec}
					}
				}
				continue
			}
			spec = newest(spec, declaredSpec)
		}
		switch kind {
		case CatalogFile:
			catalog, err := concepts.Decode(file.Data)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", file.Name, err)
			}
			if err := declare("catalog", catalog.Name, file.Name); err != nil {
				return nil, nil, err
			}
			l.catalogs = append(l.catalogs, catalog)
			l.sources = append(l.sources, file.Name)
			for _, form := range catalog.PredicateForms() {
				if form.Name == "" {
					continue
				}
				if err := add(&predicate{name: form.Name, source: file.Name, spec: spec, catalog: catalog, render: form.Render}, Predicate{
					Signature:   form.Name + "(" + subjectParam + "[, tier[, kind...]])",
					Description: fmt.Sprintf("True for %s (%s): of either tier, or of the tier and the kinds given after it.", form.About, catalog.Name),
					Catalog:     catalog.Name,
				}); err != nil {
					return nil, nil, err
				}
			}
		case PredicatesFile:
			decoded, err := decodePredicatesFile(file.Data)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", file.Name, err)
			}
			if err := declare("library", decoded.Name, file.Name); err != nil {
				return nil, nil, err
			}
			for _, d := range decoded.Definitions {
				p := &predicate{name: d.Name, source: file.Name, spec: spec, params: d.Params, text: d.Body}
				if err := add(p, Predicate{Signature: d.Name + "(" + strings.Join(d.Params, ", ") + ")", Description: strings.TrimSpace(d.Description)}); err != nil {
					return nil, nil, err
				}
				definitions = append(definitions, d)
			}
		}
	}
	base, err := Env()
	if err != nil {
		return nil, nil, fmt.Errorf("build CEL env: %w", err)
	}
	if definitions, err = l.setAside(definitions); err != nil {
		return nil, nil, err
	}
	if len(l.predicates) == 0 && len(l.catalogs) == 0 && len(l.later) == 0 {
		return nil, skipped, nil
	}
	for name := range l.predicates {
		l.order = append(l.order, name)
	}
	sort.Strings(l.order)
	sort.Slice(l.info, func(i, j int) bool { return l.info[i].Name < l.info[j].Name })

	if l.env, err = l.extend(base); err != nil {
		return nil, nil, fmt.Errorf("build CEL env: %w", err)
	}
	if err := l.resolve(definitions); err != nil {
		return nil, nil, err
	}
	for _, d := range definitions {
		for _, test := range d.Tests {
			if test.Match == "" {
				test.Match = d.Name + "(" + eventVar + ")"
			}
			l.tests = append(l.tests, predicateTest{predicate: d.Name, source: l.predicates[d.Name].source, test: test})
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
	case !concepts.IDPattern.MatchString(f.Name):
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

// declaredPredicates reads, leniently, the names of the predicates a definitions file provides:
// what this engine knows of a file it does not decode, so that what calls one is set aside with
// the file rather than failing to compile.
func declaredPredicates(data []byte) []string {
	var head struct {
		Predicates  map[string]any   `yaml:"predicates"`
		Definitions []map[string]any `yaml:"definitions"`
	}
	_ = yaml.Unmarshal(data, &head) // what does not read as these shapes is not known
	var names []string
	for _, name := range head.Predicates {
		if s, ok := name.(string); ok {
			names = append(names, s)
		}
	}
	for _, d := range head.Definitions {
		if s, ok := d["name"].(string); ok {
			names = append(names, s)
		}
	}
	return names
}

// setAside finds the predicates each definition's body calls, and sets aside, with the files
// this engine left out, every definition that calls a predicate of one of them: its body cannot
// be expanded here. It returns the definitions that remain.
func (l *Library) setAside(definitions []PredicateDefinition) ([]PredicateDefinition, error) {
	for name := range l.later {
		if l.predicates[name] != nil {
			delete(l.later, name) // a file this engine loads defines it
		}
	}
	called := make(map[string][]string, len(definitions))
	for _, d := range definitions {
		p := l.predicates[d.Name]
		// As written, a call to a predicate stays a call, which is how the ones a body uses
		// are found.
		written, err := asWritten(p.text)
		if err != nil {
			return nil, fmt.Errorf("%s: predicate %s: body: %w", p.source, p.name, err)
		}
		p.written = written
		called[d.Name] = calls(written)
	}
	for more := len(l.later) > 0; more; {
		more = false
		for _, d := range definitions {
			if l.predicates[d.Name] == nil {
				continue
			}
			for _, name := range called[d.Name] {
				if later, ok := l.later[name]; ok {
					l.later[d.Name] = laterPredicate{chain: d.Name + " -> " + later.chain, source: later.source, required: later.required}
					delete(l.predicates, d.Name)
					more = true
					break
				}
			}
		}
	}
	definitions = slices.DeleteFunc(definitions, func(d PredicateDefinition) bool { return l.predicates[d.Name] == nil })
	l.info = slices.DeleteFunc(l.info, func(p Predicate) bool { return l.predicates[p.Name] == nil })
	for _, d := range definitions {
		p := l.predicates[d.Name]
		for _, name := range called[d.Name] {
			if l.predicates[name] != nil {
				p.uses = append(p.uses, name)
			}
		}
	}
	return definitions, nil
}

// resolve parses every definition's body, in an order that puts a predicate after the ones it
// calls, so that each call in a body expands to a body already prepared. A body may call only a
// predicate whose definitions need no newer spec than its own: an engine that reads the one file
// and not the other could not expand it.
func (l *Library) resolve(definitions []PredicateDefinition) error {
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
			callee := l.predicates[name]
			if err := visit(callee, append(path, p.name)); err != nil {
				return err
			}
			if callee.spec.newerThan(p.spec) {
				return fmt.Errorf("%s: predicate %s calls %s (%s), which requires spec: %s: declare it in %s too", p.source, p.name, name, callee.source, callee.spec, p.source)
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
		free, binds := hygienic(p.name, body, p.params)
		for _, name := range free {
			if !slices.Contains(p.params, name) {
				return fmt.Errorf("%s: predicate %s: body reads %q, which is not one of its params (%s): a predicate reads only what it is given", p.source, p.name, name, strings.Join(p.params, ", "))
			}
			if slices.Contains(binds, name) {
				return fmt.Errorf("%s: predicate %s: param %q is bound by the body and also read outside that binding: a name the predicate binds for its caller means nothing outside it", p.source, p.name, name)
			}
		}
		for _, param := range p.params {
			if !slices.Contains(free, param) && !slices.Contains(binds, param) {
				return fmt.Errorf("%s: predicate %s: param %q is not used", p.source, p.name, param)
			}
		}
		p.body, p.binds = body, binds
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

// explain adds, to the error of an expression that calls a function neither the engine nor the
// library defines, what is most likely wrong: the rule was loaded without its pack's definitions.
// The function is found in the expression as written, a name a predicate could have.
func (l *Library) explain(expr string, err error) error {
	written, perr := asWritten(expr)
	env, eerr := Env()
	if perr != nil || eerr != nil {
		return err
	}
	for _, name := range calls(written) {
		if concepts.PredicateNamePattern.MatchString(name) && !env.HasFunction(name) && (l == nil || l.predicates[name] == nil) {
			return fmt.Errorf("%w\n%s is not a predicate of the definitions this rule was loaded with: a pack's definitions are its %s and %s files, found when the pack's directory is loaded", err, name, CatalogFileSuffix, PredicatesFileSuffix)
		}
	}
	return err
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

// Catalogs returns the pack's catalogs, in the order of their files' names.
func (l *Library) Catalogs() []*concepts.Catalog {
	if l == nil {
		return nil
	}
	return slices.Clone(l.catalogs)
}

// Definitions returns the definitions the rule was loaded with: those of its pack. It is nil
// for a rule decoded on its own, and for one of a pack that has none.
func (r *Rule) Definitions() *Library { return r.lib }

// uses returns the predicates expr calls by name, or nil when it calls none or does not parse. A
// call to a predicate this engine set aside for a newer spec makes the rule ruleID need that
// spec: it returns an *UnsupportedSpecError, so that the rule is set aside with the predicate.
func (l *Library) uses(ruleID, expr string) ([]*predicate, error) {
	if l == nil {
		return nil, nil
	}
	written, err := asWritten(expr)
	if err != nil {
		return nil, nil
	}
	var used []*predicate
	for _, name := range calls(written) {
		if later, ok := l.later[name]; ok {
			return nil, &UnsupportedSpecError{RuleID: ruleID, Required: later.required, Predicate: later.chain, Source: later.source}
		}
		if p := l.predicates[name]; p != nil {
			used = append(used, p)
		}
	}
	return used, nil
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
	return l.decodeValid(path, data)
}

// decodeValid decodes and validates the rule file at path, whose content is data.
func (l *Library) decodeValid(path string, data []byte) (*Rule, error) {
	rule, err := l.DecodeRule(data)
	if err != nil {
		return nil, fmt.Errorf("%s: decode: %w", path, err)
	}
	if err := rule.Validate(); err != nil {
		return nil, fmt.Errorf("%s: validate: %w", path, err)
	}
	return rule, nil
}

// Check runs the library's checks and returns one result for each: first each catalog held to
// its examples (concepts.Catalog.Check), whether or not it provides predicates, in the order of
// their files; then every fixture of every predicate definition, in the order of the predicates'
// names. A fixture whose expression does not compile against the event schema is an error of
// that fixture, which is how a definition's body is held to the schema. It is to a pack's
// definitions what CheckRule is to a rule.
func (l *Library) Check() []DefinitionResult {
	if l == nil {
		return nil
	}
	results := make([]DefinitionResult, 0, len(l.catalogs)+len(l.tests))
	for i, catalog := range l.catalogs {
		results = append(results, DefinitionResult{Catalog: true, Name: catalog.Name, Source: l.sources[i], Err: catalog.Check()})
	}
	fixtures := make([]DefinitionResult, 0, len(l.tests))
	for _, t := range l.tests {
		res := DefinitionResult{Name: t.predicate, Source: t.source, Fixture: t.test.Name, Expected: t.test.Verdict, Actual: VerdictNoMatch}
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
		fixtures = append(fixtures, res)
	}
	sort.SliceStable(fixtures, func(i, j int) bool { return fixtures[i].Name < fixtures[j].Name })
	return append(results, fixtures...)
}
