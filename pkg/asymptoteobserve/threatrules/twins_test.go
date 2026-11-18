package threatrules

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestPackCopiesAgree holds equal the copies of a list or a pattern the pack has to write more
// than once, because a regular expression fragment cannot be shared by calling a predicate:
//
//   - each path predicate X and its text twin X_redirect (a text that redirects or tees into a
//     path X accepts), which is X's pattern turned into a pattern over text by pathTwin;
//   - the two agent-surface lists and every rule text pattern that spells them;
//   - the options git takes before a subcommand, which only git_subcommand may spell;
//   - the 656-character "a command starts here" prefix of the folded text patterns;
//   - the "any host but the default registry" expansions, which anyHostBut generates.
//
// A list changed in one place and not the other fails here, naming both.
func TestPackCopiesAgree(t *testing.T) {
	lib, _, err := LoadLibraryDir(rulesDir(t))
	if err != nil {
		t.Fatal(err)
	}
	var rulePatterns []string
	for _, r := range corpusRules(t) {
		rulePatterns = append(rulePatterns, celPatterns(strings.Join(ruleExpressions(r), " "))...)
	}
	// Every pattern of the pack: the rules' and the definitions'.
	allPatterns := slices.Clone(rulePatterns)
	for _, p := range lib.predicates {
		allPatterns = append(allPatterns, celPatterns(p.text)...)
	}

	t.Run("path twins", func(t *testing.T) {
		twins := 0
		for name, twin := range lib.predicates {
			path, ok := strings.CutSuffix(name, "_redirect")
			if !ok || lib.predicates[path] == nil {
				continue
			}
			twins++
			want := lib.predicates[path].text
			for _, lit := range celLiterals.FindAllStringSubmatch(want, -1) {
				want = strings.Replace(want, lit[0], "text.matches("+celQuote(pathTwin(celUnquote(lit[2])))+")", 1)
			}
			if got := strings.Join(strings.Fields(twin.text), " "); got != strings.Join(strings.Fields(want), " ") {
				t.Errorf("%s is not %s written over text; want the body\n%s\ngot\n%s", name, path, want, twin.text)
			}
		}
		if twins < 10 {
			t.Errorf("found %d path twins, want the ten of sensitive-files.predicates.yaml", twins)
		}
	})

	t.Run("agent surface lists", func(t *testing.T) {
		for _, name := range []string{"agent_control_surface", "agent_instruction_surface"} {
			pats := celPatterns(lib.predicates[name].text)
			if len(pats) != 1 {
				t.Fatalf("%s: want one pattern, got %q", name, pats)
			}
			// ^(.*[/\\])?((LIST1)|(LIST2)([/\\].*)?)$
			inner, ok := strings.CutPrefix(pats[0], `(?is)^(.*[/\\])?(`)
			inner, ok2 := strings.CutSuffix(inner, `)$`)
			alts := splitTop(inner)
			if !ok || !ok2 || len(alts) != 2 {
				t.Fatalf("%s: the pattern is not the two lists it was: %q", name, pats[0])
			}
			lists := []string{alts[0], strings.TrimSuffix(alts[1], `([/\\].*)?`)}
			for _, list := range lists {
				head := list[:24]
				copies := 0
				for _, p := range allPatterns {
					if n := strings.Count(p, head); n != strings.Count(p, list) {
						t.Errorf("%s: a rule pattern spells the list that begins %q other than the predicate does:\n%s\nwant\n%s", name, head, p, list)
					} else {
						copies += n
					}
				}
				if copies < 2 {
					t.Errorf("%s: no rule text spells its list %q; if the copies are gone, drop this check", name, head)
				}
			}
		}
	})

	t.Run("git options", func(t *testing.T) {
		const options = `(?:-C|-c|--git-dir|--work-tree|--namespace)`
		// The body as written: git_subcommand joins the options to its caller's rest with +.
		for name, p := range lib.predicates {
			if name != "git_subcommand" && strings.Contains(p.text, options) {
				t.Errorf("%s spells git's options; call git_subcommand", name)
			}
		}
		if !strings.Contains(lib.predicates["git_subcommand"].text, options) {
			t.Fatalf("git_subcommand no longer spells %s; update this check", options)
		}
		for i, p := range rulePatterns {
			if strings.Contains(p, options) {
				t.Errorf("a rule pattern spells git's options; call git_subcommand:\n%s", rulePatterns[i])
			}
		}
	})

	t.Run("command prefix", func(t *testing.T) {
		// The prefix runs from the command boundary to the optional directory of the program.
		const start, end = `(^|[;&|\n])[ \t]*(?:(?:then|do|else)[ \t]+)?`, "(?:[^\\s;&|<>#\\x27\"`=]*/)?"
		var first string
		copies := 0
		for _, p := range allPatterns {
			for off := 0; ; {
				i := strings.Index(p[off:], start)
				if i < 0 {
					break
				}
				i += off
				j := strings.Index(p[i:], end)
				if j < 0 {
					t.Errorf("a pattern opens the command prefix and never closes it:\n%s", p)
					break
				}
				prefix := p[i : i+j+len(end)]
				if first == "" {
					first = prefix
				} else if prefix != first {
					t.Errorf("the command prefix differs between copies:\n%s\n%s", first, prefix)
				}
				copies++
				off = i + j + len(end)
			}
		}
		// 656 characters as the files write it, with \\ and \" for one character each.
		if written := len(celQuote(first)) - 2; copies < 5 || written != 656 {
			t.Errorf("found %d copies of a %d-character prefix; want the five folded patterns' 656", copies, written)
		}
	})

	t.Run("hosts but the default registry", func(t *testing.T) {
		const open = `https://([^\s/:'\"?#`
		want := map[string]int{
			"https://" + anyHostBut("pypi.org"):                                   2,
			"https://" + anyHostBut("registry.npmjs.org", "registry.yarnpkg.com"): 2,
		}
		got := map[string]int{}
		for _, p := range allPatterns {
			for off := 0; ; {
				i := strings.Index(p[off:], open)
				if i < 0 {
					break
				}
				i += off
				found := false
				for expansion := range want {
					if strings.HasPrefix(p[i:], expansion) {
						got[expansion]++
						found = true
					}
				}
				if !found {
					t.Errorf("an \"any host but\" expansion is not one anyHostBut generates; regenerate it from its host list:\n%s", p[i:])
				}
				off = i + len(open)
			}
		}
		for expansion, n := range want {
			if got[expansion] != n {
				t.Errorf("found %d copies of %s, want %d", got[expansion], expansion, n)
			}
		}
	})
}

// celLiterals finds x.matches("...") calls in CEL source; celPatterns returns their patterns.
var celLiterals = regexp.MustCompile(`(\w+)\.matches\("((?:[^"\\]|\\.)*)"\)`)

func celPatterns(expr string) []string {
	var out []string
	for _, m := range celLiterals.FindAllStringSubmatch(expr, -1) {
		out = append(out, celUnquote(m[2]))
	}
	return out
}

// celUnquote reads the escapes the pack's string literals use.
func celUnquote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func celQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// twinWriter is what writes a file in a text twin: a redirection, whose target is the next word,
// or tee, whose targets are its words after it; then a quote and the directories of the path.
// twinStop is what ends a name in text.
const (
	twinWriter = `(>>?\s*|\btee\b([ \t]+[^\s;&|<>]+)*[ \t]+)["']?([^\s;&|"'<>]*/)?`
	twinStop   = `\s;&|"'`
)

// pathTwin turns a path predicate's pattern into its twin's: each alternative after the writer,
// with its directory boundary (^|/) left to the writer, an alternative with no boundary allowed
// anywhere in a name, its end ($) a quote, a space or the end of the text, and a class of any
// name ([^/], .*) stopped where a name stops in text.
func pathTwin(pattern string) string {
	flags := regexp.MustCompile(`^\(\?[a-z]+\)`).FindString(pattern)
	var alts []string
	for _, a := range splitTop(strings.TrimPrefix(pattern, flags)) {
		if rest, ok := strings.CutPrefix(a, "(^|/)"); ok {
			a = rest
		} else {
			a = "[^" + twinStop + "]*" + a
		}
		if rest, ok := strings.CutSuffix(a, "$"); ok && !strings.HasSuffix(rest, `\`) {
			a = rest
		} else {
			a += "[^" + twinStop + "]*"
		}
		a = strings.ReplaceAll(a, "[^/]", "[^/"+twinStop+"]")
		a = strings.ReplaceAll(a, ".*", "[^"+twinStop+"]*")
		alts = append(alts, a)
	}
	core := strings.Join(alts, "|")
	if len(alts) != 1 || !enclosedGroup(core) {
		core = "(" + core + ")"
	}
	return flags + twinWriter + core + `(["'\s]|$)`
}

// splitTop splits a pattern at the | that are outside every group and class.
func splitTop(s string) []string {
	var alts []string
	depth, start, class := 0, 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
		case class:
			class = c != ']'
		case c == '[':
			class = true
			if strings.HasPrefix(s[i+1:], "^") {
				i++
			}
			if strings.HasPrefix(s[i+1:], "]") {
				i++
			}
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == '|' && depth == 0:
			alts = append(alts, s[start:i])
			start = i + 1
		}
	}
	return append(alts, s[start:])
}

// enclosedGroup reports whether s is one group, from its first character to its last.
func enclosedGroup(s string) bool {
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		return false
	}
	depth, class := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
		case class:
			class = c != ']'
		case c == '[':
			class = true
			if strings.HasPrefix(s[i+1:], "^") {
				i++
			}
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 && i < len(s)-1 {
				return false
			}
		}
	}
	return true
}

// anyHostBut is a pattern for a URL host of at least one character that is none of hosts, in
// lower case (the patterns are case-insensitive): RE2 has no negative lookahead, so it is the
// trie of the hosts with, at each node, a character that leaves the trie, or the end of the
// host where a trusted host goes on.
func anyHostBut(hosts ...string) string {
	const name = `[^\s/:'\"?#`
	quote := func(c byte) string {
		if c == '.' {
			return `\.`
		}
		return string(c)
	}
	var gen func(suffixes []string, root bool) string
	gen = func(suffixes []string, root bool) string {
		var next []byte
		for _, s := range suffixes {
			if s != "" && !slices.Contains(next, s[0]) {
				next = append(next, s[0])
			}
		}
		slices.Sort(next)
		var alts []string
		if !root && !slices.Contains(suffixes, "") {
			alts = append(alts, "")
		}
		leave := name
		for _, c := range next {
			leave += quote(c)
		}
		alts = append(alts, leave+"]"+name+"]*")
		for _, c := range next {
			var rest []string
			for _, s := range suffixes {
				if s != "" && s[0] == c {
					rest = append(rest, s[1:])
				}
			}
			alts = append(alts, quote(c)+"("+gen(rest, false)+")")
		}
		return strings.Join(alts, "|")
	}
	return "(" + gen(hosts, true) + ")"
}
