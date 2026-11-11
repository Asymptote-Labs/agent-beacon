package concepts

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/shellcmd"
)

// CommandMatch names commands that get a credential out of a store that is not a file: the
// keychain, a signed-in CLI, a key agent. It is asked of one parsed command: its name after
// sudo and the like, and the words after the name.
type CommandMatch struct {
	// Name is the command's name: one pattern component, so `gpg{,2}` is gpg or gpg2.
	Name string `yaml:"name"`
	// Words are words that follow each other in this order somewhere after the name, each a
	// pattern component: [auth, token] is `gh auth token` and `gh --hostname h auth token`,
	// and is not `gh token auth` or a quoted "auth token".
	Words []string `yaml:"words,omitempty"`
	// Flags are options of which one must be there, anywhere after the name. A single letter
	// or digit is a short option, alone or in a cluster (w is -w and -aw), in its own case.
	// Anything else is a pattern component for a whole option word, which may carry =value.
	Flags []string `yaml:"flags,omitempty"`
}

// String renders the match the way the reference shows it: `security find-generic-password -w|-g`.
func (m CommandMatch) String() string {
	parts := append([]string{m.Name}, m.Words...)
	if len(m.Flags) > 0 {
		flags := make([]string, len(m.Flags))
		for i, f := range m.Flags {
			if isShortFlag(f) {
				f = "-" + f
			}
			flags[i] = f
		}
		parts = append(parts, strings.Join(flags, "|"))
	}
	return strings.Join(parts, " ")
}

// commandPattern is a parsed CommandMatch.
type commandPattern struct {
	name  []item
	words [][]item
	// short are the letters of the short options, and long the patterns of the option words.
	short string
	long  [][]item
}

// notNewline is the regular expression for a character of a word: a parsed command's arguments
// are matched as text with one word per line.
const notNewline = `[^\n]`

func isShortFlag(flag string) bool {
	return len(flag) == 1 && isAlnum(flag[0])
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func parseCommandMatch(m CommandMatch) (commandPattern, error) {
	var p commandPattern
	var err error
	if p.name, err = parseWord(m.Name); err != nil {
		return p, fmt.Errorf("name: %w", err)
	}
	for _, word := range m.Words {
		items, err := parseWord(word)
		if err != nil {
			return p, fmt.Errorf("words: %w", err)
		}
		p.words = append(p.words, items)
	}
	for _, flag := range m.Flags {
		switch {
		case isShortFlag(flag):
			if strings.Contains(p.short, flag) {
				return p, fmt.Errorf("flags: %q listed twice", flag)
			}
			p.short += flag
		case !strings.HasPrefix(flag, "-") && !strings.HasPrefix(flag, "/"):
			return p, fmt.Errorf("flags: %q: want one letter for a short option, or an option word that begins with - or /", flag)
		default:
			items, err := parseWord(flag)
			if err != nil {
				return p, fmt.Errorf("flags: %w", err)
			}
			p.long = append(p.long, items)
		}
	}
	return p, nil
}

// parseWord reads a pattern for one word: a pattern component (see pattern).
func parseWord(word string) ([]item, error) {
	if word == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	items, err := parseItems(word, true)
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", word, err)
	}
	return items, nil
}

// matches reports whether the pattern names the command with this name and these words after it.
func (p *commandPattern) matches(name string, args []string) bool {
	if !matchItems(p.name, fold(name)) {
		return false
	}
	folded := make([]string, len(args))
	for i, arg := range args {
		folded[i] = fold(arg)
	}
	return p.hasWords(folded) && p.hasFlag(args, folded)
}

func (p *commandPattern) hasWords(folded []string) bool {
	if len(p.words) == 0 {
		return true
	}
	for i := 0; i+len(p.words) <= len(folded); i++ {
		all := true
		for k, word := range p.words {
			all = all && matchItems(word, folded[i+k])
		}
		if all {
			return true
		}
	}
	return false
}

func (p *commandPattern) hasFlag(args, folded []string) bool {
	if p.short == "" && len(p.long) == 0 {
		return true
	}
	for i, arg := range args {
		if p.short != "" && isCluster(arg) && strings.ContainsAny(arg, p.short) {
			return true
		}
		// An option word, whole or up to an = that begins its value.
		for end := 0; end <= len(folded[i]); end++ {
			if end < len(folded[i]) && folded[i][end] != '=' {
				continue
			}
			if slices.ContainsFunc(p.long, func(option []item) bool { return matchItems(option, folded[i][:end]) }) {
				return true
			}
		}
	}
	return false
}

// isCluster reports whether arg is short options written together: -w, -aw.
func isCluster(arg string) bool {
	if len(arg) < 2 || arg[0] != '-' {
		return false
	}
	for i := 1; i < len(arg); i++ {
		if !isAlnum(arg[i]) {
			return false
		}
	}
	return true
}

// CommandTerm is one alternative of a selection's commands, as regular expressions over a
// parsed command: Name over its name, and Words and Flags, each "" when it asks nothing, over
// its arguments as text with one word per line (e.command.parsed_commands[].args).
type CommandTerm struct {
	Name, Words, Flags string
}

func (p *commandPattern) term() CommandTerm {
	var b strings.Builder
	b.WriteString(`(?i)^`)
	writeItems(&b, p.name, notNewline)
	b.WriteString(`$`)
	t := CommandTerm{Name: b.String()}

	if len(p.words) > 0 {
		b.Reset()
		b.WriteString(`(?im)^`)
		for i, word := range p.words {
			if i > 0 {
				b.WriteString(`\n`)
			}
			writeItems(&b, word, notNewline)
		}
		b.WriteString(`$`)
		t.Words = b.String()
	}

	if p.short != "" || len(p.long) > 0 {
		b.Reset()
		b.WriteString(`(?m)^(?:`)
		if p.short != "" {
			b.WriteString(`-[A-Za-z0-9]*[` + p.short + `][A-Za-z0-9]*`)
		}
		if len(p.long) > 0 {
			if p.short != "" {
				b.WriteByte('|')
			}
			b.WriteString(`(?i:`)
			for i, option := range p.long {
				if i > 0 {
					b.WriteByte('|')
				}
				writeItems(&b, option, notNewline)
			}
			b.WriteString(`)(?:=` + notNewline + `*)?`)
		}
		b.WriteString(`)$`)
		t.Flags = b.String()
	}
	return t
}

// A log does not always hold a command line as it ran. Redaction replaces a value that looks
// like a secret, and the value of TOKEN=$(gh auth token) or of "Bearer $(gcloud auth
// print-access-token)" starts with the command's name: what the log holds is
// "TOKEN=[REDACTED] auth token)", which no longer reads as bash and names no command. The
// redacted form of a command match finds what is left: the redaction mark, then the match's
// words and one of its options within the same command.
const (
	redactionMark = `\[REDACTED\]`
	// textWordChar is a character of a word in a command text, and textWordEnd what ends one.
	textWordChar = `[^\s;&|'"()]`
	textWordEnd  = `(?:$|[\s;&|)'"])`
	// textWordsBefore is words of the same command before the ones asked for. A quoted word is
	// not passed: what follows it may be inside the quotes.
	textWordsBefore = `(?:[^;&|\n'"]*\s)?`
	// textWordsBetween is the rest of the same command up to an option, quoted values included.
	textWordsBetween = `(?:[^;&|\n]*\s)?`
	// textOptionsBefore is options of the same command, and nothing else, before an option.
	textOptionsBefore = `(?:-` + textWordChar + `*\s+)*`
)

// redactedRegex returns the match as an RE2 regular expression, under (?i), for a command text
// in which redaction took the command's name, or "" when what would be left does not tell the
// command from another: a match with two words or more, or with a word and options, or with a
// word that is more than letters (auth:token), or with a whole option word (--export-secret-keys)
// has a redacted form; one told by its name, by one plain word (lookup), or by one-letter
// options has none.
func (p *commandPattern) redactedRegex() string {
	options := p.short != "" || len(p.long) > 0
	var b strings.Builder
	b.WriteString(redactionMark + `\s+`)
	switch {
	case len(p.words) >= 2, len(p.words) == 1 && (options || !plainWord(p.words[0])):
		b.WriteString(textWordsBefore)
		for i, word := range p.words {
			if i > 0 {
				b.WriteString(`\s+`)
			}
			writeItems(&b, word, textWordChar)
		}
		b.WriteString(textWordEnd)
		if !options {
			return b.String()
		}
		b.WriteString(textWordsBetween + `(?:`)
		if p.short != "" {
			b.WriteString(`(?-i:-[A-Za-z0-9]*[` + p.short + `][A-Za-z0-9]*)`)
			if len(p.long) > 0 {
				b.WriteByte('|')
			}
		}
	case len(p.words) == 0 && len(p.long) > 0:
		b.WriteString(textOptionsBefore + `(?:`)
	default:
		return ""
	}
	if len(p.long) > 0 {
		b.WriteString(`(?:`)
		for i, option := range p.long {
			if i > 0 {
				b.WriteByte('|')
			}
			writeItems(&b, option, textWordChar)
		}
		b.WriteString(`)(?:=` + textWordChar + `*)?`)
	}
	b.WriteString(`)` + textWordEnd)
	return b.String()
}

// plainWord reports whether a word pattern is letters and digits only: a word many commands take.
func plainWord(word []item) bool {
	for _, it := range word {
		if strings.ContainsFunc(it.lit, func(r rune) bool { return r > unicode.MaxASCII || !isAlnum(byte(r)) }) {
			return false
		}
		if slices.ContainsFunc(it.alts, func(alt []item) bool { return !plainWord(alt) }) {
			return false
		}
	}
	return true
}

// invocation is one command of a command line as a command store sees it.
type invocation struct {
	name string
	args []string
}

// invocations reads a command line as bash and returns the commands it runs, as the rule engine
// presents them: name and args of each entry of e.command.parsed_commands.
func invocations(commandLine string) ([]invocation, error) {
	commands, err := shellcmd.Parse(commandLine)
	if err != nil {
		return nil, err
	}
	var out []invocation
	for _, c := range commands {
		if name := c.Name(); name != "" {
			out = append(out, invocation{name: name, args: strings.Split(c.ArgsText(), "\n")})
		}
	}
	return out, nil
}
