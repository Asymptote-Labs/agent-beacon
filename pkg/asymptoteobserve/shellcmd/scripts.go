package shellcmd

import (
	"slices"
	"strings"
)

// language is how FileReads reads a script an interpreter is given on its command line or its
// standard input: where the script is, and which calls in it open a file for reading. It follows
// a call whose path is a string literal, outside strings and comments, and nothing else: not a
// path a script computes, and not what the script does with the file.
type language struct {
	// eval are the short options whose value is the script (python -c, node -e), values the ones
	// that take some other value, and module the ones that make the interpreter run something
	// that is not an inline script (python -m). evalLong are the long forms of eval, and
	// subcommand the word that takes the script instead (deno eval).
	eval, values, module string
	evalLong             []string
	subcommand           string
	// calls are the functions that open a file for reading, as written before the parenthesis.
	// A call written as a method (fs.readFileSync) matches by its last name, and a name with a
	// dot in it (File.read) only in full.
	calls []string
	// then, for a call it is set for, is what must follow the call for it to read the file:
	// Path("x") alone only names one.
	then map[string][]string
	// modes is set when a second string argument is the mode the file is opened in.
	modes bool
	// bare is set when a call may be written without parentheses (ruby: File.read ".env").
	bare bool
	// prefixes are the letters a string literal may begin with (python: r"..", b"..").
	prefixes string
	// backtick, triple, hash and slashes say which strings and comments the language has beyond
	// '..' and "..": `..`, '''..''' and """..""", # to the end of the line, and // to it.
	backtick, triple, hash, slashes bool
}

var (
	python = language{
		eval: "c", values: "WXQ", module: "m",
		calls:    []string{"open", "Path"},
		then:     map[string][]string{"Path": {".read_text", ".read_bytes", ".open"}},
		modes:    true,
		prefixes: "rbuRBU",
		triple:   true,
		hash:     true,
	}
	javascript = language{
		eval: "ep", values: "r", evalLong: []string{"--eval", "--print"},
		calls: []string{
			"readFileSync", "readFile", "createReadStream", "readTextFileSync", "readTextFile", "Bun.file",
		},
		backtick: true,
		slashes:  true,
	}
	deno = func() language {
		l := javascript
		l.eval, l.values, l.evalLong, l.subcommand = "", "", nil, "eval"
		return l
	}()
	ruby = language{
		eval: "e", values: "IrCEF",
		calls: []string{
			"File.read", "File.readlines", "File.binread", "File.foreach", "File.open",
			"IO.read", "IO.readlines", "IO.binread", "IO.foreach",
		},
		modes: true,
		bare:  true,
		hash:  true,
	}
	php = language{
		eval: "r", values: "dcz",
		calls:   []string{"file_get_contents", "fopen", "readfile", "file"},
		modes:   true,
		hash:    true,
		slashes: true,
	}
)

// scriptLanguage returns the language of the interpreter a command runs, by its base name.
func scriptLanguage(name string) (*language, bool) {
	switch {
	case strings.HasPrefix(name, "python"), strings.HasPrefix(name, "pypy"):
		return &python, true
	case name == "node", name == "nodejs", name == "bun":
		return &javascript, true
	case name == "deno":
		return &deno, true
	case name == "ruby":
		return &ruby, true
	case name == "php":
		return &php, true
	}
	return nil, false
}

// inlineScript returns the script the interpreter is given as text: the value of its eval option,
// or, when it is given no script file, the here-document or here-string on its standard input.
// The text is as written, so a part the shell would expand is still its source. It is "" when the
// interpreter runs a file, a module, or what a pipe sends it.
func inlineScript(l *language, c *Command, args []Word) string {
	for i := 0; i < len(args); i++ {
		t := args[i].Unquoted
		switch {
		case l.subcommand != "":
			switch {
			case i == 0 && t != l.subcommand:
				return ""
			case i > 0 && !isOption(args[i]):
				return t
			}
			continue
		case t == "-":
			return stdinScript(c)
		case strings.HasPrefix(t, "--"):
			name, value, attached := strings.Cut(t, "=")
			if !slices.Contains(l.evalLong, name) {
				continue
			}
			if !attached && i+1 < len(args) {
				value = args[i+1].Unquoted
			}
			return value
		case !isOption(args[i]):
			return "" // a script file
		}
	cluster:
		for j := 1; j < len(t); j++ {
			switch {
			case strings.IndexByte(l.eval, t[j]) >= 0:
				if j+1 < len(t) {
					return t[j+1:]
				}
				if i+1 < len(args) {
					return args[i+1].Unquoted
				}
				return ""
			case strings.IndexByte(l.module, t[j]) >= 0:
				return ""
			case strings.IndexByte(l.values, t[j]) >= 0:
				if j+1 == len(t) {
					i++ // the value is the next word
				}
				break cluster
			}
		}
	}
	if l.subcommand != "" {
		return ""
	}
	return stdinScript(c)
}

// stdinScript returns the here-document or here-string that is the command's standard input.
func stdinScript(c *Command) string {
	for k := len(c.Redirects) - 1; k >= 0; k-- {
		r := &c.Redirects[k]
		if r.Fd != "" && r.Fd != "0" {
			continue
		}
		switch r.Op {
		case "<<", "<<-":
			return r.Body
		case "<<<":
			return r.Target.Unquoted
		case "<", "<>", "<&":
			return ""
		}
	}
	return ""
}

// scriptReads returns the files a script opens for reading by a literal path.
func scriptReads(l *language, script string) []string {
	var files []string
	for i := 0; i < len(script); {
		c := script[i]
		switch {
		case c == '\'' || c == '"' || c == '`' && l.backtick:
			_, i = stringLiteral(l, script, i)
		case c == '#' && l.hash, c == '/' && l.slashes && strings.HasPrefix(script[i:], "//"):
			if end := strings.IndexByte(script[i:], '\n'); end >= 0 {
				i += end
			} else {
				i = len(script)
			}
		case isNameByte(c) && (i == 0 || !isNameByte(script[i-1])):
			// A name with the names it is qualified by: fs.promises.readFile, Path, .read.
			end := i
			for end < len(script) && isNameByte(script[end]) {
				end++
			}
			if path, after, ok := l.opens(script, i, end); ok {
				files = append(files, path)
				end = after
			}
			i = end
		default:
			i++
		}
	}
	return files
}

// opens reports whether the name at script[i:end] is one of the language's calls, with a literal
// path as its first argument that it reads, and returns the path and where that argument ends. A
// call without a dot in it is also matched as a method (io.open, fs.promises.readFile).
func (l *language) opens(script string, i, end int) (path string, after int, ok bool) {
	name := script[i:end]
	for _, call := range l.calls {
		if name != call && (strings.Contains(call, ".") || !strings.HasSuffix(name, call) || name[len(name)-len(call)-1] != '.') {
			continue
		}
		j := skipSpaces(script, end)
		if j < len(script) && script[j] == '(' {
			j = skipSpaces(script, j+1)
		} else if !l.bare || j == end {
			return "", 0, false
		}
		path, j, ok = l.literal(script, j)
		if !ok || path == "" || strings.Contains(path, "://") || strings.ContainsAny(path, "\n{") {
			return "", 0, false
		}
		if l.modes && l.writes(script, j) {
			return "", 0, false
		}
		if follow, needed := l.then[call]; needed {
			after := skipSpaces(script, j)
			if after >= len(script) || script[after] != ')' {
				return "", 0, false
			}
			after = skipSpaces(script, after+1)
			if !slices.ContainsFunc(follow, func(method string) bool { return strings.HasPrefix(script[after:], method) }) {
				return "", 0, false
			}
		}
		return path, j, true
	}
	return "", 0, false
}

// literal reads a string literal at i, after any prefix letters, and returns its text as written
// between the quotes and where it ends.
func (l *language) literal(script string, i int) (text string, end int, ok bool) {
	for i < len(script) && strings.IndexByte(l.prefixes, script[i]) >= 0 {
		i++
	}
	if i >= len(script) || script[i] != '\'' && script[i] != '"' && !(script[i] == '`' && l.backtick) {
		return "", 0, false
	}
	text, end = stringLiteral(l, script, i)
	return text, end, true
}

// writes reports whether the argument after a path, starting at i, is a mode that writes: a
// second string argument or mode="..", with w, a or x in it.
func (l *language) writes(script string, i int) bool {
	i = skipSpaces(script, i)
	if i >= len(script) || script[i] != ',' {
		return false
	}
	i = skipSpaces(script, i+1)
	if strings.HasPrefix(script[i:], "mode") {
		i = skipSpaces(script, i+len("mode"))
		if i >= len(script) || script[i] != '=' {
			return false
		}
		i = skipSpaces(script, i+1)
	}
	mode, _, ok := l.literal(script, i)
	return ok && strings.ContainsAny(mode, "wax")
}

// stringLiteral returns the text of the string that opens at i and the index after its closing
// quote, or the end of the script when it is not closed.
func stringLiteral(l *language, script string, i int) (text string, end int) {
	quote := script[i : i+1]
	if l.triple && strings.HasPrefix(script[i:], quote+quote+quote) {
		quote = script[i : i+3]
	}
	start := i + len(quote)
	for j := start; j < len(script); j++ {
		switch {
		case script[j] == '\\':
			j++
		case strings.HasPrefix(script[j:], quote):
			return script[start:j], j + len(quote)
		}
	}
	return script[start:], len(script)
}

func skipSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// isNameByte reports whether c can be part of a call's name: a letter, a digit, _ or the dot of a
// qualified one.
func isNameByte(c byte) bool {
	return c == '_' || c == '.' || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z'
}
