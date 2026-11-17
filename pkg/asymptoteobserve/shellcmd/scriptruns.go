package shellcmd

import (
	"slices"
	"strings"
)

// This file reads the commands an interpreter's inline script runs: the command line of
// os.system("..") and subprocess.run([..]) in Python, child_process.execSync("..") and
// spawn("git", [..]) in JavaScript, system(".."), backticks and %x(..) in Ruby, and exec("..")
// and its relatives in PHP, when the command is written out as string literals.
//
// It does not make the interpreter something a caller can see into. A script is code: it can
// build a command from parts, read one from a file, or run one through a call this file does
// not know, and none of that is on the command line. So an interpreter never counts as read
// (RunsRead), whatever is found here. What is found is still what runs, and a caller that asks
// a command by its name sees the rm of `python3 -c "os.system('rm -rf /')"` as it sees any other.

// runCalls are a language's calls that run a command, as written before the parenthesis.
type runCalls struct {
	calls []string
	// only, when set, is text the script must hold for the calls to count: `exec` is a method
	// of every regular expression in JavaScript, and runs a command only as child_process's.
	only string
	// exact is set when a call counts only as written, with nothing before it: in Ruby and
	// PHP `conn.exec("..")` and `$db->exec("..")` run SQL, and the bare `exec("..")` a command.
	// Otherwise a call without a dot is also matched as a method (sp.Popen), as isCall does.
	exact bool
	// ticks is set when `..` runs a command, and percentX when %x(..) does.
	ticks, percentX bool
}

var scriptRunCalls = map[*language]runCalls{
	&python: {calls: []string{
		"os.system", "os.popen", "subprocess.run", "subprocess.call", "subprocess.check_call",
		"subprocess.check_output", "subprocess.getoutput", "subprocess.getstatusoutput", "sp.run", "sp.call",
		"Popen", "check_output", "check_call", "getoutput", "getstatusoutput",
	}},
	&javascript: {
		calls: []string{"execSync", "execFileSync", "spawnSync", "exec", "execFile", "spawn"},
		only:  "child_process",
	},
	&deno: {calls: []string{"execSync", "execFileSync", "spawnSync", "exec", "execFile", "spawn"}, only: "child_process"},
	&ruby: {
		calls: []string{"system", "exec", "spawn", "IO.popen", "Process.spawn", "Open3.capture2", "Open3.capture2e", "Open3.capture3", "Open3.popen3"},
		exact: true, ticks: true, percentX: true,
	},
	&php: {
		calls: []string{"system", "exec", "shell_exec", "passthru", "popen", "proc_open"},
		exact: true, ticks: true,
	},
}

// unknownWord stands for an argument of a call that is not a string literal: a word, or
// words, of the command whose value the script computes.
const unknownWord = "\x00"

// scriptRuns returns the command lines the interpreter's inline script runs, one per line, when
// the command is an interpreter with an inline script and the script runs any that can be read,
// and whether the script is the interpreter's standard input (inlineScript).
func (c Command) scriptRuns() (lines string, stdin, ok bool) {
	name, args := c.program()
	l, ok := scriptLanguage(name)
	if !ok {
		return "", false, false
	}
	script, stdin := inlineScript(l, &c, args)
	if script == "" {
		return "", false, false
	}
	commands := scriptCommands(l, script)
	if len(commands) == 0 {
		return "", false, false
	}
	return strings.Join(commands, "\n"), stdin, true
}

// scriptCommands returns the command lines a script runs through one of its language's calls
// that run a command, outside strings and comments, in the order they are written.
//
// The command is the call's first argument: a string literal, which a shell reads, or a list
// of string literals, which are the command's words. After a string literal, further string
// literals and a list of them are words too (Ruby's system("git", "push"), JavaScript's
// spawn("git", ["push"])). An argument or a list element that is not a literal is a word whose
// value is unknown, so that the command is not taken for one with fewer words; a name=value
// or a {..} after the command is an option of the call and no word. A literal that the
// language fills in (an f-string, a template with ${..}, #{..}) or that is one operand of an
// expression (`"rm " + path`, `"rm %s" % path`) is not read, and neither is that call.
func scriptCommands(l *language, script string) []string {
	runs, ok := scriptRunCalls[l]
	if !ok || runs.only != "" && !strings.Contains(script, runs.only) {
		return nil
	}
	var commands []string
	for i := 0; i < len(script); {
		c := script[i]
		switch {
		case c == '`' && runs.ticks:
			text, end := stringLiteral(l, script, i)
			if !interpolates(text, '`') && strings.TrimSpace(text) != "" {
				commands = append(commands, unescapeLiteral(text))
			}
			i = end
		case c == '%' && runs.percentX && strings.HasPrefix(script[i:], "%x") && i+2 < len(script):
			closer := strings.IndexByte("({[<", script[i+2])
			end := -1
			if closer >= 0 {
				end = strings.IndexByte(script[i+3:], ")}]>"[closer])
			}
			if end < 0 {
				i++
				continue
			}
			if text := script[i+3 : i+3+end]; !interpolates(text, '`') && strings.TrimSpace(text) != "" {
				commands = append(commands, text)
			}
			i += 3 + end + 1
		case c == '\'' || c == '"' || c == '`' && l.backtick:
			_, i = stringLiteral(l, script, i)
		case c == '#' && l.hash, c == '/' && l.slashes && strings.HasPrefix(script[i:], "//"):
			if end := strings.IndexByte(script[i:], '\n'); end >= 0 {
				i += end
			} else {
				i = len(script)
			}
		case isNameByte(c) && (i == 0 || !isNameByte(script[i-1])):
			start, end := i, i
			for end < len(script) && isNameByte(script[end]) {
				end++
			}
			name := script[start:end]
			i = end
			if !slices.ContainsFunc(runs.calls, func(call string) bool {
				if runs.exact {
					return name == call && (start == 0 || script[start-1] != '>')
				}
				return isCall(name, call)
			}) {
				continue
			}
			j := skipSpaces(script, end)
			if j < len(script) && script[j] == '(' {
				j = skipBlank(script, j+1)
			} else if !l.bare || j == end {
				continue
			}
			if command, ok := l.commandArgument(script, j); ok {
				commands = append(commands, command)
			}
		default:
			i++
		}
	}
	return commands
}

// commandArgument reads the command a call is given at i: see scriptCommands.
func (l *language) commandArgument(script string, i int) (string, bool) {
	var words []string
	line := ""
	if i < len(script) && script[i] == '[' {
		var ok bool
		if words, i, ok = l.literalList(script, i); !ok || len(words) == 0 || words[0] == unknownWord {
			return "", false
		}
	} else {
		text, end, ok := l.plainLiteral(script, i)
		if !ok || strings.TrimSpace(text) == "" {
			return "", false
		}
		line, i = text, end
		// A literal that is one operand of an expression is not the command. The end of the
		// line ends a call written without parentheses.
		if j := skipSpaces(script, i); j < len(script) && strings.IndexByte(",)\n\r;", script[j]) < 0 {
			return "", false
		}
	}
	// Further words: string literals, and a list of them.
	for {
		j := skipBlank(script, i)
		if j >= len(script) || script[j] != ',' {
			break
		}
		j = skipBlank(script, j+1)
		if j >= len(script) || script[j] == ')' {
			break // a trailing comma
		}
		if j < len(script) && script[j] == '[' {
			more, end, ok := l.literalList(script, j)
			if !ok {
				words = append(words, unknownWord)
				break
			}
			words, i = append(words, more...), end
			continue
		}
		text, end, ok := l.plainLiteral(script, j)
		if !ok {
			// name=value and {..} are options of the call; anything else may be words.
			if !optionArgument(script, j) {
				words = append(words, unknownWord)
			}
			break
		}
		words, i = append(words, text), end
	}
	if line != "" && len(words) == 0 {
		return line, true // one string: a command line a shell reads
	}
	var b strings.Builder
	if line != "" {
		b.WriteString(shellQuote(line))
	}
	for _, w := range words {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		if w == unknownWord {
			b.WriteString(`"$@"`)
		} else {
			b.WriteString(shellQuote(w))
		}
	}
	return b.String(), true
}

// optionArgument reports whether the argument at i is an option of the call and not a word of
// the command: name=value, name: value or {..}.
func optionArgument(script string, i int) bool {
	if i < len(script) && script[i] == '{' {
		return true
	}
	end := i
	for end < len(script) && isNameByte(script[end]) && script[end] != '.' {
		end++
	}
	j := skipSpaces(script, end)
	return end > i && j < len(script) && (script[j] == '=' && !strings.HasPrefix(script[j:], "==") || script[j] == ':')
}

// literalList reads [ "a", "b" ] at i: the texts of its string literals, unknownWord for an
// element that is anything else, and where the list ends. ok is false when it does not end.
func (l *language) literalList(script string, i int) (words []string, end int, ok bool) {
	i = skipBlank(script, i+1)
	for i < len(script) && script[i] != ']' {
		if text, after, ok := l.plainLiteral(script, i); ok {
			if next := skipBlank(script, after); next < len(script) && (script[next] == ',' || script[next] == ']') {
				words, i = append(words, text), next
			} else {
				words, i = append(words, unknownWord), l.skipElement(script, after)
			}
		} else {
			words, i = append(words, unknownWord), l.skipElement(script, i)
		}
		if i < len(script) && script[i] == ',' {
			i = skipBlank(script, i+1)
		}
	}
	if i >= len(script) {
		return nil, 0, false
	}
	return words, i + 1, true
}

// skipElement returns the index of the comma or the ] that ends the list element at i, past
// the strings and the brackets inside it.
func (l *language) skipElement(script string, i int) int {
	depth := 0
	for i < len(script) {
		switch c := script[i]; {
		case c == '\'' || c == '"' || c == '`' && l.backtick:
			_, i = stringLiteral(l, script, i)
			continue
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == '}' || c == ']' && depth > 0:
			depth--
		case depth == 0 && (c == ',' || c == ']'):
			return i
		}
		i++
	}
	return i
}

// plainLiteral reads a string literal at i that the language does not fill in, and returns its
// value: the text between the quotes with the escapes a command line needs removed.
func (l *language) plainLiteral(script string, i int) (text string, end int, ok bool) {
	start := i
	for start < len(script) && strings.IndexByte(l.prefixes, script[start]) >= 0 {
		start++
	}
	text, end, ok = l.literal(script, i)
	if !ok || interpolates(text, script[start]) {
		return "", 0, false
	}
	return unescapeLiteral(text), end, true
}

// interpolates reports whether a literal in quote holds something its language would replace:
// ${..} in a JavaScript template, and #{..} in a double-quoted or backtick string.
func interpolates(text string, quote byte) bool {
	return quote != '\'' && (strings.Contains(text, "${") || strings.Contains(text, "#{"))
}

var literalEscapes = strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\'`, `'`, `\n`, "\n", `\t`, "\t")

func unescapeLiteral(text string) string {
	if strings.IndexByte(text, '\\') < 0 {
		return text
	}
	return literalEscapes.Replace(text)
}

// shellQuote writes a word so that a shell reads it back as that word.
func shellQuote(word string) string {
	plain := word != ""
	for i := 0; i < len(word) && plain; i++ {
		c := word[i]
		plain = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_@%+=:,./-", c) >= 0
	}
	if plain {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// skipBlank skips spaces, tabs and newlines.
func skipBlank(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}
