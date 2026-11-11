package concepts

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// A pattern names files by the last components of their path, so it is anchored by construction:
// ".aws/credentials" is the file credentials in a directory .aws, wherever that is, and is not
// "docs/.aws/credentials.md" or "my.aws/credentials". Components are separated by "/", and a
// path's by "/" or "\". Letters match without regard to case.
//
// Within a component, "*" is any run of characters, also none, and "{a,b,}" is one of the
// alternatives, which may be empty and may hold "*". A component that is "**" is any number of
// whole components, also none. A pattern that ends in "/**" names everything under a directory
// instead of a file: ".gnupg/private-keys-v1.d/**".
//
// There is nothing else: no character classes, no "?", no escapes, no negation. What a pattern
// must not match is said by a store's exclude patterns.
type pattern struct {
	source string
	parts  []component
	// under is set for a pattern that ends in "/**".
	under bool
}

// component is one path component of a pattern: "**" (any), or a sequence of items.
type component struct {
	any   bool
	items []item
}

// item is a literal run, a "*", or an alternation of item sequences.
type item struct {
	star bool
	// lit is as written, for the regular expression; folded is what the matcher compares.
	lit, folded string
	alts        [][]item
}

// separators is the regular expression for one path separator, and notSeparator for a character
// that is not one.
const (
	separator    = `[/\\]`
	notSeparator = `[^/\\]`
)

func parsePattern(source string) (pattern, error) {
	p := pattern{source: source}
	if source == "" {
		return p, fmt.Errorf("empty pattern")
	}
	names := strings.Split(source, "/")
	for i, name := range names {
		switch {
		case name == "":
			return p, fmt.Errorf("pattern %q: empty component (a pattern names the last components of a path, so it has no leading, trailing or doubled /)", source)
		case name == "**" && i == 0:
			return p, fmt.Errorf("pattern %q: a leading ** adds nothing, a pattern already matches under any directory", source)
		case name == "**" && i == len(names)-1:
			p.under = true
		case name == "**":
			if names[i-1] == "**" || names[i+1] == "**" {
				return p, fmt.Errorf("pattern %q: ** next to **", source)
			}
			p.parts = append(p.parts, component{any: true})
		default:
			items, err := parseItems(name, true)
			if err != nil {
				return p, fmt.Errorf("pattern %q: %w", source, err)
			}
			p.parts = append(p.parts, component{items: items})
		}
	}
	return p, nil
}

// parseItems reads one component, or one alternative of a {..} when braces is false.
func parseItems(s string, braces bool) ([]item, error) {
	var items []item
	literal := func(text string) {
		if text != "" {
			items = append(items, item{lit: text, folded: fold(text)})
		}
	}
	start := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '*':
			literal(s[start:i])
			if len(items) > 0 && items[len(items)-1].star {
				return nil, fmt.Errorf("** inside a component (it is a component of its own)")
			}
			items = append(items, item{star: true})
			start = i + 1
		case c == '{':
			if !braces {
				return nil, fmt.Errorf("{ inside {..}")
			}
			literal(s[start:i])
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("{ without }")
			}
			var alts [][]item
			for _, alt := range strings.Split(s[i+1:i+end], ",") {
				parsed, err := parseItems(alt, false)
				if err != nil {
					return nil, err
				}
				alts = append(alts, parsed)
			}
			if len(alts) < 2 {
				return nil, fmt.Errorf("{..} with one alternative")
			}
			items = append(items, item{alts: alts})
			i += end
			start = i + 1
		case c == '}':
			return nil, fmt.Errorf("} without {")
		case strings.IndexByte(`?[]\!`, c) >= 0, c < 0x20, c == 0x7f:
			return nil, fmt.Errorf("%q is not part of the pattern syntax (only *, ** and {a,b})", c)
		}
	}
	literal(s[start:])
	return items, nil
}

// matches reports whether the pattern names path.
func (p *pattern) matches(path string) bool {
	return p.matchesComponents(splitPath(path))
}

// matchesComponents is matches for a path already split and folded by splitPath.
func (p *pattern) matchesComponents(comps []string) bool {
	for i := range comps {
		if !p.under {
			if matchComponents(p.parts, comps[i:]) {
				return true
			}
			continue
		}
		// Under a directory: the parts name some run of components that another follows.
		for j := i + 1; j < len(comps); j++ {
			if matchComponents(p.parts, comps[i:j]) {
				return true
			}
		}
	}
	return false
}

// splitPath returns path's components, folded. An empty component stays one: a//b is three.
func splitPath(path string) []string {
	folded := fold(path)
	var comps []string
	start := 0
	for i := 0; i < len(folded); i++ {
		if folded[i] == '/' || folded[i] == '\\' {
			comps = append(comps, folded[start:i])
			start = i + 1
		}
	}
	return append(comps, folded[start:])
}

// matchComponents reports whether parts match all of comps.
func matchComponents(parts []component, comps []string) bool {
	if len(parts) == 0 {
		return len(comps) == 0
	}
	if !parts[0].any {
		return len(comps) > 0 && matchItems(parts[0].items, comps[0]) && matchComponents(parts[1:], comps[1:])
	}
	for n := 0; n <= len(comps); n++ {
		if n > 0 && comps[n-1] == "" {
			break // ** is whole components, and an empty one is not one
		}
		if matchComponents(parts[1:], comps[n:]) {
			return true
		}
	}
	return false
}

// matchItems reports whether items match all of s, a folded path component.
func matchItems(items []item, s string) bool {
	if len(items) == 0 {
		return s == ""
	}
	first, rest := items[0], items[1:]
	switch {
	case first.star:
		for i := 0; i <= len(s); i++ {
			if matchItems(rest, s[i:]) {
				return true
			}
		}
		return false
	case first.alts != nil:
		for _, alt := range first.alts {
			if matchItems(append(slices.Clip(alt), rest...), s) {
				return true
			}
		}
		return false
	default:
		return strings.HasPrefix(s, first.folded) && matchItems(rest, s[len(first.folded):])
	}
}

// regex returns the pattern as an RE2 regular expression to follow a separator or the start of
// the path, under (?i): it ends with $ for a file and with a separator for a directory.
func (p *pattern) regex() string {
	var b strings.Builder
	for i, part := range p.parts {
		if part.any {
			b.WriteString(`(?:` + notSeparator + `+` + separator + `)*`)
			continue
		}
		writeItems(&b, part.items, notSeparator)
		if i < len(p.parts)-1 || p.under {
			b.WriteString(separator)
		}
	}
	if !p.under {
		b.WriteString(`$`)
	}
	return b.String()
}

// writeItems writes items as a regular expression. within is the class of the characters a *
// runs over: the ones that do not end what the items are matched against.
func writeItems(b *strings.Builder, items []item, within string) {
	for _, it := range items {
		switch {
		case it.star:
			b.WriteString(within + `*`)
		case it.alts != nil:
			b.WriteString(`(?:`)
			for i, alt := range it.alts {
				if i > 0 {
					b.WriteByte('|')
				}
				writeItems(b, alt, within)
			}
			b.WriteByte(')')
		default:
			b.WriteString(regexp.QuoteMeta(it.lit))
		}
	}
}

// patternsRegex returns one RE2 regular expression that matches a path any of the patterns names.
func patternsRegex(patterns []pattern) string {
	alternatives := make([]string, len(patterns))
	for i := range patterns {
		alternatives[i] = patterns[i].regex()
	}
	return `(?i)(?:^|` + separator + `)(?:` + strings.Join(alternatives, "|") + `)`
}

// fold maps s to the form two strings share exactly when RE2 matches one against the other under
// (?i): each character becomes the smallest of the characters it folds to.
func fold(s string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			smallest = min(smallest, f)
		}
		return smallest
	}, s)
}
