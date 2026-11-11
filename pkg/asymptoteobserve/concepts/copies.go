package concepts

import (
	"regexp"
	"slices"
	"strings"
)

// A rule that carries its own regular expression for what a catalog says will drift from the
// catalog. Such a copy is told by what it spells out: the names the catalog's stores go by
// (".aws/credentials", ".netrc", "gh auth token"). A name is a literal run of a store's
// pattern, long enough to mean something and belonging to that store alone.

// minName is the least length of a name: ".env" is one, ".db" is not.
const minName = 4

// Named returns the IDs of the stores a regular expression spells out by name, in the catalog's
// order. An expression of a rule that names two stores or more says again what the catalog
// says: the rule should call the catalog instead.
func (c *Catalog) Named(expression string) []string {
	text := plainExpression(expression)
	var named []string
	for i := range c.Stores {
		s := &c.Stores[i]
		if slices.ContainsFunc(s.names, func(name string) bool { return strings.Contains(text, name) }) {
			named = append(named, s.ID)
		}
	}
	return named
}

// Defined returns the IDs of the stores a regular expression defines on its own: it spells out
// the store's name and accepts every example the store gives, each as the catalog writes it (a
// path, or a command line). Naming one store is not copying it: a rule may name ".netrc" to
// say something else about it. Accepting everything the store is shown to be is saying what
// the store is. An expression that does not compile defines nothing.
func (c *Catalog) Defined(expression string) []string {
	named := c.Named(expression)
	if len(named) == 0 {
		return nil
	}
	re, err := regexp.Compile(expression)
	if err != nil {
		return nil
	}
	var defined []string
	for i := range c.Stores {
		s := &c.Stores[i]
		if slices.Contains(named, s.ID) && !slices.ContainsFunc(s.Examples, func(example string) bool { return !re.MatchString(example) }) {
			defined = append(defined, s.ID)
		}
	}
	return defined
}

var (
	// expressionSeparator is how a regular expression writes a path separator of either kind,
	// and expressionSpace how it writes the space between two words.
	expressionSeparator = regexp.MustCompile(`\[(?:/\\\\|\\\\/)\]`)
	expressionSpace     = regexp.MustCompile(`(?:\\s|\[ \\t\]| )[+*]?`)
	// expressionAssertion is what matches no character, and expressionEscape an escaped
	// character that stands for itself.
	expressionAssertion = regexp.MustCompile(`\\[bB]|\(\?[a-zA-Z-]*[:)]`)
	expressionEscape    = regexp.MustCompile(`\\([^A-Za-z0-9])`)
)

// plainExpression returns a regular expression as the text it spells: lower case, "/" for a
// separator, a space for each run of whitespace it asks for, and a character for its escape.
func plainExpression(expression string) string {
	text := expressionSeparator.ReplaceAllString(expression, "/")
	text = expressionAssertion.ReplaceAllString(text, "")
	text = expressionSpace.ReplaceAllString(text, " ")
	text = expressionEscape.ReplaceAllString(text, "$1")
	return strings.ToLower(text)
}

// nameStores gives each store the names that are its alone. A name that is one plain word
// ("credentials", "token") is no name: prose and other rules say it for other reasons. Nor is a
// word with the "." that an extension follows ("credentials."), unless it is a dot file's
// (".env.").
func (c *Catalog) nameStores() {
	owners := map[string]int{}
	candidates := make([][]string, len(c.Stores))
	for i := range c.Stores {
		s := &c.Stores[i]
		var names []string
		for k := range s.match {
			names = append(names, pathNames(&s.match[k])...)
		}
		for k := range s.commands {
			names = append(names, commandNames(&s.commands[k])...)
		}
		names = slices.DeleteFunc(names, func(name string) bool {
			word := name
			if !strings.HasPrefix(name, ".") {
				word = strings.TrimSuffix(name, ".")
			}
			return !strings.ContainsFunc(word, func(r rune) bool { return r < 'a' || r > 'z' })
		})
		slices.Sort(names)
		candidates[i] = slices.Compact(names)
		for _, name := range candidates[i] {
			owners[name]++
		}
	}
	for i := range c.Stores {
		c.Stores[i].names = slices.DeleteFunc(candidates[i], func(name string) bool { return owners[name] > 1 })
	}
}

// namer collects the literal runs of a pattern: what it writes out between what varies.
type namer struct {
	runs []run
	text strings.Builder
	// varied is set once something that varies has been passed: a run after it is a suffix.
	varied, suffix bool
}

type run struct {
	text   string
	suffix bool
}

func (n *namer) literal(s string) {
	if n.text.Len() == 0 {
		n.suffix = n.varied
	}
	n.text.WriteString(strings.ToLower(s))
}

// vary ends the current run at a "*" (no alternatives) or a "{..}". A run that ends in an
// alternation is also named with each alternative written out ("id_" with "rsa" is "id_rsa"),
// and what an alternative writes out is a run of its own ("google-chrome").
func (n *namer) vary(alternatives [][]item) {
	head := strings.TrimLeft(n.text.String(), "/ ")
	n.end()
	n.varied = true
	for _, alt := range alternatives {
		if head != "" && len(alt) == 1 && alt[0].alts == nil && !alt[0].star {
			n.runs = append(n.runs, run{text: head + strings.ToLower(alt[0].lit), suffix: n.suffix})
		}
		n.items(alt)
		n.end()
	}
}

func (n *namer) end() {
	if text := strings.Trim(n.text.String(), "/ "); text != "" {
		n.runs = append(n.runs, run{text: text, suffix: n.suffix})
	}
	n.text.Reset()
}

func (n *namer) items(items []item) {
	for _, it := range items {
		switch {
		case it.star:
			n.vary(nil)
		case it.alts != nil:
			n.vary(it.alts)
		default:
			n.literal(it.lit)
		}
	}
}

// pathNames returns the names a path pattern goes by.
func pathNames(pattern *pattern) []string {
	var n namer
	for i, part := range pattern.parts {
		if part.any {
			n.vary(nil)
			continue
		}
		if i > 0 && n.text.Len() > 0 {
			n.literal("/")
		}
		n.items(part.items)
	}
	n.end()
	var names, extensions []string
	for _, r := range n.runs {
		switch {
		case len(r.text) < minName:
		case r.suffix && strings.HasPrefix(r.text, ".") && !strings.Contains(r.text, "/"):
			// An extension after a wildcard (".sqlite" of "Bitwarden*.sqlite") is many files'.
			extensions = append(extensions, r.text)
		default:
			names = append(names, r.text)
			// A long directory is a name too: an expression may write the file after it another
			// way ("serviceaccount/(token|ca\.crt)").
			if dir := r.text[:strings.LastIndexByte(r.text, '/')+1]; strings.Count(dir, "/") >= 2 && len(dir) > 3*minName {
				names = append(names, strings.TrimSuffix(dir, "/"))
			}
		}
	}
	if len(names) == 0 && len(n.runs) == 1 {
		return extensions // the pattern is nothing but an extension: "*.kdbx"
	}
	return names
}

// commandNames returns the names a command match goes by: the command with the words after
// it ("gh auth token"), an option written in full ("--export-secret-k"), and the command alone
// when it is told by nothing else or is not a plain word ("secret-tool"). A command that is a
// plain word and takes words is not named by itself: "security" and "gcloud" run much that
// reads no credential.
func commandNames(match *commandPattern) []string {
	var alone namer
	alone.items(match.name)
	alone.end()
	var n namer
	n.items(match.name)
	for _, word := range match.words {
		if n.text.Len() > 0 {
			n.literal(" ")
		}
		n.items(word)
	}
	n.end()
	var names []string
	for _, r := range n.runs {
		byName := slices.ContainsFunc(alone.runs, func(a run) bool { return a.text == r.text })
		switch {
		case len(r.text) < minName:
		case r.suffix && (strings.HasPrefix(r.text, "-") || strings.HasPrefix(r.text, ".")):
			// The tail of a word ("-password" of "find-{generic,internet}-password", ".exe") is
			// many words'.
		case byName && len(match.words) > 0 && plainWord(match.name):
		default:
			names = append(names, r.text)
		}
	}
	for _, option := range match.long {
		var o namer
		o.items(option)
		o.end()
		if len(o.runs) > 0 && !o.runs[0].suffix && len(o.runs[0].text) > minName+1 {
			names = append(names, o.runs[0].text)
		}
	}
	return names
}
