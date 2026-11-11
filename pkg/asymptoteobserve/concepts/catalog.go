// Package concepts holds definitions that threat rules share instead of each writing its own: a
// catalog is a reviewed list of named things, and every target that needs the definition (a CEL
// expression, a KQL expression, a documentation table) is generated from it.
//
// The first catalog is the credential stores: where credentials are kept and how they are got
// out. A store is either files, named by path, or commands that hand a credential over (the
// keychain, a signed-in CLI, a key agent). Each store says what it matches, how sure a match is
// (its tier), and proves it with examples, which Decode checks. The expressions generated from
// the catalog are checked against the same examples, so a store means the same thing wherever it
// is used.
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

// Tier is how sure it is that what a store matches holds or yields a credential.
type Tier string

const (
	// TierDefinite is a file that exists to hold a credential (~/.aws/credentials, an SSH
	// private key), or a command that prints one (gh auth token).
	TierDefinite Tier = "definite"
	// TierLikely is a file that often holds one and often does not (~/.kube/config,
	// .env.development, a file named credentials.json), or a command that shows what a store
	// holds without the secret itself (ssh-add -l).
	TierLikely Tier = "likely"
)

// includes reports whether a selection that asks for at least the tier takes a store of tier t
// in. Definite is the stronger tier: a selection of likely stores takes the definite ones too.
func (least Tier) includes(t Tier) bool {
	return least == TierLikely || t == TierDefinite
}

// Catalog is a list of stores.
type Catalog struct {
	// Name identifies the catalog (credential-stores), and Version counts its revisions.
	Name    string `yaml:"catalog"`
	Version int    `yaml:"version"`
	// Spec is the Threat Rules spec version the file needs, read by the rule engine before it
	// decodes the rest. This package does not interpret it.
	Spec        string `yaml:"spec,omitempty"`
	Description string `yaml:"description"`
	// Predicates are the names a rule calls the catalog by.
	Predicates Predicates `yaml:"predicates,omitempty"`
	// Kinds are the groups a store may belong to, in the order they are documented.
	Kinds  []Kind  `yaml:"kinds"`
	Stores []Store `yaml:"stores"`
}

// Predicates names the predicates a rule pack's rules call a catalog by. Each takes what it is
// asked about, then optionally a tier and kinds, as Select does:
// credential_path(e.file.path), credential_path(f.path, "definite"),
// credential_command(c, "likely", "keychain"), credential_mention(e.command.command).
type Predicates struct {
	// Path is the predicate over a path: whether it is a store of files.
	Path string `yaml:"path,omitempty"`
	// Command is the predicate over a parsed command: whether it reads a store of commands.
	Command string `yaml:"command,omitempty"`
	// Mention is the predicate over a text: whether it names a store of files.
	Mention string `yaml:"mention,omitempty"`
	// Redacted is the predicate over a command line as a log holds it: whether it reads a store
	// of commands by a command whose name redaction replaced.
	Redacted string `yaml:"redacted,omitempty"`
}

// predicateName is the form of a predicate's name: a CEL identifier in lower case.
var predicateName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Kind is a group of stores a rule can select on its own: the Kubernetes ones, the browser ones.
type Kind struct {
	ID    string `yaml:"id"`
	Title string `yaml:"title"`
}

// Store is one thing that holds credentials: files, by Match, or what commands hand over, by
// Commands. It has one or the other.
type Store struct {
	ID    string `yaml:"id"`
	Title string `yaml:"title"`
	Kind  string `yaml:"kind"`
	Tier  Tier   `yaml:"tier"`
	// Match are the patterns (see pattern) of the paths that are this store, and Exclude the
	// patterns of the ones among them that are not: .env.example is no dotenv file.
	Match   []string `yaml:"match,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
	// Commands are the commands that read this store.
	Commands []CommandMatch `yaml:"commands,omitempty"`
	// Examples are what the store is: paths for a store of files, at least one for each Match
	// pattern, and command lines for a store of commands, at least one for each of Commands.
	// Counterexamples look like the store and are no store of the catalog at all, with at least
	// one for each Exclude pattern.
	Examples        []string `yaml:"examples"`
	Counterexamples []string `yaml:"counterexamples,omitempty"`

	match, exclude []pattern
	commands       []commandPattern
	// names are what a regular expression that copies the store spells out (see Named).
	names []string
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Load reads and decodes the catalog file at path. It does not run the catalog's examples:
// that is Check.
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

// Decode strictly decodes a catalog and checks its shape and its patterns. It is what a rule
// engine does each time it loads a pack, so it does not run the catalog's examples: Check does,
// when a catalog is linted, tested or installed, as a rule's fixtures are run then and not on
// every load.
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
	return &c, nil
}

// Check holds the catalog to its examples: every store matches its own examples, no store
// matches a counterexample, and every pattern is shown at work by one. A catalog that passes
// means what its examples say.
func (c *Catalog) Check() error {
	for i := range c.Stores {
		s := &c.Stores[i]
		check := c.checkPaths
		if len(s.commands) > 0 {
			check = c.checkCommands
		}
		if err := check(s); err != nil {
			return fmt.Errorf("store %s: %w", s.ID, err)
		}
	}
	return nil
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
		case len(s.Match) == 0 && len(s.Commands) == 0:
			return fmt.Errorf("store %s: no match patterns and no commands", s.ID)
		case len(s.Commands) > 0 && (len(s.Match) > 0 || len(s.Exclude) > 0):
			return fmt.Errorf("store %s: both paths and commands: a store is files or commands, so make it two", s.ID)
		}
		ids[s.ID] = true
		var err error
		if s.match, err = parsePatterns(s.Match); err != nil {
			return fmt.Errorf("store %s: match: %w", s.ID, err)
		}
		if s.exclude, err = parsePatterns(s.Exclude); err != nil {
			return fmt.Errorf("store %s: exclude: %w", s.ID, err)
		}
		for k, m := range s.Commands {
			p, err := parseCommandMatch(m)
			if err != nil {
				return fmt.Errorf("store %s: commands[%d]: %w", s.ID, k, err)
			}
			s.commands = append(s.commands, p)
		}
	}
	seen := map[string]string{}
	for _, p := range []struct {
		form, name string
		commands   bool
	}{{"path", c.Predicates.Path, false}, {"command", c.Predicates.Command, true}, {"mention", c.Predicates.Mention, false}, {"redacted", c.Predicates.Redacted, true}} {
		form, name := p.form, p.name
		has := slices.ContainsFunc(c.Stores, func(s Store) bool { return p.commands == (len(s.commands) > 0) })
		switch {
		case name == "":
			continue
		case !predicateName.MatchString(name):
			return fmt.Errorf("predicates.%s %q: want a name like credential_path", form, name)
		case !has:
			return fmt.Errorf("predicates.%s %q: the catalog has no store it would be about", form, name)
		case seen[name] != "":
			return fmt.Errorf("predicates: %s and %s are both %q", seen[name], form, name)
		}
		seen[name] = form
	}
	c.nameStores()
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

// checkPaths holds a store of files to its examples. An example must be the store's own: the one
// Lookup returns, so two stores that claim the same path are told apart or told off. Every
// pattern must be shown at work by an example, so none is dead or untested.
func (c *Catalog) checkPaths(s *Store) error {
	used := make([]bool, len(s.match))
	for _, example := range s.Examples {
		comps := splitPath(example)
		if !s.matchesComponents(comps) {
			return fmt.Errorf("example %q: not matched", example)
		}
		if found, _ := c.lookup(comps); found != s {
			return fmt.Errorf("example %q: is store %s, which comes first or is the surer tier", example, found.ID)
		}
		for k := range s.match {
			used[k] = used[k] || s.match[k].matchesComponents(comps)
		}
	}
	if k := slices.Index(used, false); k >= 0 {
		return fmt.Errorf("match pattern %q: no example shows it", s.Match[k])
	}
	excluded := make([]bool, len(s.exclude))
	for _, counter := range s.Counterexamples {
		comps := splitPath(counter)
		if found, ok := c.lookup(comps); ok {
			return fmt.Errorf("counterexample %q: is store %s", counter, found.ID)
		}
		if !slices.ContainsFunc(s.match, func(p pattern) bool { return p.matchesComponents(comps) }) {
			continue
		}
		for k := range s.exclude {
			excluded[k] = excluded[k] || s.exclude[k].matchesComponents(comps)
		}
	}
	if k := slices.Index(excluded, false); k >= 0 {
		return fmt.Errorf("exclude pattern %q: no counterexample shows it", s.Exclude[k])
	}
	return nil
}

// checkCommands holds a store of commands to its examples, which are command lines: one of the
// commands an example runs must be this store, and none of the commands a counterexample runs
// may be any store. Every entry of Commands must be shown at work by an example.
func (c *Catalog) checkCommands(s *Store) error {
	used := make([]bool, len(s.commands))
	for _, example := range s.Examples {
		commands, err := invocations(example)
		if err != nil {
			return fmt.Errorf("example %q: %w", example, err)
		}
		own := false
		var other *Store
		for _, inv := range commands {
			found, ok := c.lookupCommand(inv.name, inv.args)
			switch {
			case !ok:
			case found == s:
				own = true
				for k := range s.commands {
					used[k] = used[k] || s.commands[k].matches(inv.name, inv.args)
				}
			default:
				other = found
			}
		}
		switch {
		case !own && other != nil:
			return fmt.Errorf("example %q: is store %s, which comes first or is the surer tier", example, other.ID)
		case !own:
			return fmt.Errorf("example %q: not matched", example)
		}
	}
	if k := slices.Index(used, false); k >= 0 {
		return fmt.Errorf("commands[%d] (%s): no example shows it", k, s.Commands[k])
	}
	for _, counter := range s.Counterexamples {
		commands, err := invocations(counter)
		if err != nil {
			return fmt.Errorf("counterexample %q: %w", counter, err)
		}
		for _, inv := range commands {
			if found, ok := c.lookupCommand(inv.name, inv.args); ok {
				return fmt.Errorf("counterexample %q: is store %s", counter, found.ID)
			}
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

// matchesCommand reports whether a command with this name and these words after it reads the
// store.
func (s *Store) matchesCommand(name string, args []string) bool {
	return slices.ContainsFunc(s.commands, func(p commandPattern) bool { return p.matches(name, args) })
}

// Lookup returns the store of files path is: of the stores that match it, the definite one
// before the likely one, and the one listed first before the others.
func (c *Catalog) Lookup(path string) (*Store, bool) {
	return c.lookup(splitPath(path))
}

func (c *Catalog) lookup(comps []string) (*Store, bool) {
	return c.first(func(s *Store) bool { return s.matchesComponents(comps) })
}

// LookupCommand returns the store a command reads, by the command's name after sudo and the
// like and the words after the name (shellcmd's Name and ArgsText, which are name and args of a
// parsed command in a rule). Among several, it prefers as Lookup does.
func (c *Catalog) LookupCommand(name string, args []string) (*Store, bool) {
	return c.lookupCommand(name, args)
}

func (c *Catalog) lookupCommand(name string, args []string) (*Store, bool) {
	return c.first(func(s *Store) bool { return s.matchesCommand(name, args) })
}

func (c *Catalog) first(matches func(*Store) bool) (*Store, bool) {
	var found *Store
	for i := range c.Stores {
		s := &c.Stores[i]
		if (found == nil || found.Tier != TierDefinite && s.Tier == TierDefinite) && matches(s) {
			found = s
		}
	}
	return found, found != nil
}

// Selection is some of a catalog's stores as regular expressions (RE2 syntax, which Go, CEL's
// matches and Kusto's `matches regex` share). A path is in the selection when it matches the
// Include of one of Paths and not that term's Exclude. A command is in it when it matches every
// expression of one of Commands. A text mentions the selection when it matches the Include of
// one of Mentions and not that term's Exclude. Redacted is "" or one expression over a command
// text: it matches where redaction took the name of one of the selection's commands.
type Selection struct {
	Paths    []PathTerm
	Commands []CommandTerm
	Mentions []PathTerm
	Redacted string
}

// PathTerm is one alternative of a selection's paths. Exclude is "" when it excludes nothing.
type PathTerm struct {
	Include, Exclude string
}

// Select returns the stores of at least the tier min, of the given kinds or, with none given, of
// every kind.
func (c *Catalog) Select(min Tier, kinds ...string) (Selection, error) {
	var selection Selection
	if min != TierDefinite && min != TierLikely {
		return selection, fmt.Errorf("tier %q: want %s or %s", min, TierDefinite, TierLikely)
	}
	for _, kind := range kinds {
		if !slices.ContainsFunc(c.Kinds, func(k Kind) bool { return k.ID == kind }) {
			return selection, fmt.Errorf("kind %q is not one of the catalog's kinds", kind)
		}
	}
	// The stores of files that exclude nothing share one expression; one that excludes gets its
	// own, so that what it leaves out is not taken from a store that keeps it.
	var plain []pattern
	var redacted []string
	for i := range c.Stores {
		s := &c.Stores[i]
		switch {
		case !min.includes(s.Tier) || len(kinds) > 0 && !slices.Contains(kinds, s.Kind):
		case len(s.commands) > 0:
			for k := range s.commands {
				selection.Commands = append(selection.Commands, s.commands[k].term())
				if text := s.commands[k].redactedRegex(); text != "" {
					redacted = append(redacted, text)
				}
			}
		case len(s.exclude) == 0:
			plain = append(plain, s.match...)
		default:
			selection.Paths = append(selection.Paths, PathTerm{Include: patternsRegex(s.match), Exclude: patternsRegex(s.exclude)})
			selection.Mentions = append(selection.Mentions, PathTerm{Include: mentionsRegex(s.match), Exclude: mentionsRegex(s.exclude)})
		}
	}
	if len(plain) > 0 {
		selection.Paths = slices.Insert(selection.Paths, 0, PathTerm{Include: patternsRegex(plain)})
		selection.Mentions = slices.Insert(selection.Mentions, 0, PathTerm{Include: mentionsRegex(plain)})
	}
	if len(redacted) > 0 {
		selection.Redacted = `(?i)(?:` + strings.Join(redacted, "|") + `)`
	}
	return selection, nil
}

// PathCEL returns the selection's paths as a CEL boolean expression over subject, a string
// expression such as e.file.path. It is false for a selection without stores of files.
func (s Selection) PathCEL(subject string) string {
	return termsCEL(s.Paths, subject)
}

// MentionCEL returns a CEL boolean expression over subject, a string expression such as
// e.command.command, that is true when the text names a file of the selection: for a command
// line that does not parse, or a prompt handed to another program. It is coarser than PathCEL
// over a path, by what a text cannot say. A file is named by characters a name is written with,
// so one with a space in it is not found. A file named by one plain word ("credentials",
// "id_rsa") counts only after a directory. And a store that excludes some names is not
// mentioned by a text that also names one of those: ".env.local" beside ".env.example".
func (s Selection) MentionCEL(subject string) string {
	return termsCEL(s.Mentions, subject)
}

// RedactedCEL returns a CEL boolean expression over subject, a string expression for a command
// line (e.command.command), that is true when the text holds one of the selection's commands
// with its name redacted: "TOKEN=[REDACTED] auth token)", which is what a log keeps of
// TOKEN=$(gh auth token). A command told only by its name, or by its name and one-letter
// options, has no such form. It is false for a selection without one.
func (s Selection) RedactedCEL(subject string) string {
	if s.Redacted == "" {
		return "false"
	}
	return subject + ".matches(" + strconv.Quote(s.Redacted) + ")"
}

func termsCEL(terms []PathTerm, subject string) string {
	return anyOf(terms, " || ", func(t PathTerm) string {
		include := subject + ".matches(" + strconv.Quote(t.Include) + ")"
		if t.Exclude == "" {
			return include
		}
		return "(" + include + " && !" + subject + ".matches(" + strconv.Quote(t.Exclude) + "))"
	})
}

// PathKQL returns the selection's paths as a Kusto boolean expression over column, a string
// column. Commands have no Kusto form: they are matched on a command line the rule engine has
// parsed, which a query over the raw events does not have.
func (s Selection) PathKQL(column string) string {
	// In a verbatim string literal a backslash is itself and a double quote is written twice.
	literal := func(regex string) string { return `@"` + strings.ReplaceAll(regex, `"`, `""`) + `"` }
	return anyOf(s.Paths, " or ", func(t PathTerm) string {
		include := column + " matches regex " + literal(t.Include)
		if t.Exclude == "" {
			return include
		}
		return "(" + include + " and not(" + column + " matches regex " + literal(t.Exclude) + "))"
	})
}

// CommandCEL returns the selection's commands as a CEL boolean expression over subject, an
// expression for one parsed command such as the c of e.command.parsed_commands.exists(c, ..).
// It reads the command's name and args. It is false for a selection without stores of commands.
func (s Selection) CommandCEL(subject string) string {
	terms := anyOf(s.Commands, " || ", func(t CommandTerm) string {
		parts := []string{subject + ".name.matches(" + strconv.Quote(t.Name) + ")"}
		for _, regex := range []string{t.Words, t.Flags} {
			if regex != "" {
				parts = append(parts, subject+".args.matches("+strconv.Quote(regex)+")")
			}
		}
		if len(parts) == 1 {
			return parts[0]
		}
		return "(" + strings.Join(parts, " && ") + ")"
	})
	// A rule asks this of every command an agent runs, and almost none is one of the
	// selection's: one test of the name says so before each term asks again.
	const prefix, suffix = `(?i)^`, `$`
	var names []string
	for _, t := range s.Commands {
		if name := strings.TrimSuffix(strings.TrimPrefix(t.Name, prefix), suffix); !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	if len(s.Commands) < 3 {
		return terms
	}
	return "(" + subject + ".name.matches(" + strconv.Quote(prefix+`(?:`+strings.Join(names, "|")+`)`+suffix) + ") && " + terms + ")"
}

// anyOf joins the rendering of each term with or, and is false for no terms.
func anyOf[T any](terms []T, or string, render func(T) string) string {
	if len(terms) == 0 {
		return "false"
	}
	rendered := make([]string, len(terms))
	for i, t := range terms {
		rendered[i] = render(t)
	}
	if len(rendered) == 1 {
		return rendered[0]
	}
	return "(" + strings.Join(rendered, or) + ")"
}
