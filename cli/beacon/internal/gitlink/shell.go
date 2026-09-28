package gitlink

import (
	"path/filepath"
	"strings"
)

// ShellWrittenPaths returns the files a shell command line evidently writes: the operands of rm,
// touch, truncate, mv, cp, install, tee, `git mv`/`git rm`, in-place sed/perl, and the targets of
// output redirections. It is deliberately shallow. Anything it cannot read plainly -- variables,
// subshells, a script that writes files itself -- contributes nothing, because a wrong path would
// link a session to a commit it had no part in, and a missed one only leaves a link out.
func ShellWrittenPaths(command string) []string {
	var paths []string
	moved := false
	for _, segment := range splitShellSegments(command) {
		words, redirected := stripRedirections(shellWords(segment))
		found := append(redirected, commandTargets(words)...)
		if moved {
			// After a cd, a relative path is relative to somewhere this parser does not track.
			found = absoluteOnly(found)
		}
		paths = append(paths, found...)
		if len(words) > 0 && (words[0] == "cd" || words[0] == "pushd" || words[0] == "popd") {
			moved = true
		}
	}
	return dedupe(paths)
}

func commandTargets(words []string) []string {
	// Skip environment assignments and the wrappers that run the real command.
	for len(words) > 0 {
		w := words[0]
		if strings.Contains(w, "=") && !strings.HasPrefix(w, "=") && !strings.HasPrefix(w, "-") {
			words = words[1:]
			continue
		}
		if w == "sudo" || w == "command" || w == "exec" || w == "nohup" || w == "time" {
			words = words[1:]
			continue
		}
		break
	}
	if len(words) == 0 {
		return nil
	}
	name := filepath.Base(words[0])
	args := words[1:]
	if name == "git" && len(args) > 0 && (args[0] == "mv" || args[0] == "rm") {
		name, args = "git-"+args[0], args[1:]
	}
	operands, flags := splitFlags(args)
	switch name {
	case "rm", "unlink", "touch", "truncate", "git-rm":
		return operands
	case "mv", "git-mv":
		// Every operand: the sources stop existing and the destination starts to.
		return operands
	case "cp", "install", "ln":
		if len(operands) >= 2 {
			return operands[len(operands)-1:]
		}
	case "tee":
		return operands
	case "sed", "perl", "gsed":
		if hasInPlaceFlag(flags) && len(operands) >= 2 {
			// The first operand is the script, unless it came in with -e.
			return operands[1:]
		}
	}
	return nil
}

func hasInPlaceFlag(flags []string) bool {
	for _, f := range flags {
		if f == "--in-place" || strings.HasPrefix(f, "--in-place=") {
			return true
		}
		if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") && strings.Contains(f[1:], "i") {
			return true
		}
	}
	return false
}

// splitFlags separates operands from flags. Everything after `--` is an operand.
func splitFlags(args []string) (operands, flags []string) {
	for i, a := range args {
		if a == "--" {
			return append(operands, args[i+1:]...), flags
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			continue
		}
		operands = append(operands, a)
	}
	return operands, flags
}

// stripRedirections removes output redirections from words and returns the remaining words and
// the redirections' targets. Only an unquoted word can be a redirection: `echo '> x'` prints.
func stripRedirections(words []shellWord) (rest, targets []string) {
	for i := 0; i < len(words); i++ {
		w := words[i]
		op, target := "", ""
		if !w.quoted {
			op, target = redirection(w.text)
		}
		if op == "" {
			if !w.expands {
				rest = append(rest, w.text)
			}
			continue
		}
		if target == "" && i+1 < len(words) {
			i++
			if words[i].expands {
				continue
			}
			target = words[i].text
		}
		if target != "" && !strings.HasPrefix(target, "&") && target != "/dev/null" && !strings.HasPrefix(target, "/dev/") {
			targets = append(targets, target)
		}
	}
	return rest, targets
}

// redirection recognizes `>`, `>>`, `>|`, `1>`, `2>`, `&>`, with the target attached or not. Input
// redirection is not a write and is left in place.
func redirection(word string) (op, target string) {
	for _, prefix := range []string{"&>>", "&>", "1>>", "2>>", "1>", "2>", ">>", ">|", ">"} {
		if strings.HasPrefix(word, prefix) {
			return prefix, word[len(prefix):]
		}
	}
	return "", ""
}

// splitShellSegments splits a command line into simple commands at ;, &&, ||, |, & and newlines,
// outside quotes.
func splitShellSegments(command string) []string {
	var segments []string
	var cur strings.Builder
	quote := byte(0)
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segments = append(segments, s)
		}
		cur.Reset()
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(command) {
				cur.WriteByte(c)
				i++
				c = command[i]
			}
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			quote = c
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(command):
			cur.WriteByte(c)
			i++
			cur.WriteByte(command[i])
		case c == '|' && i > 0 && command[i-1] == '>':
			// `>|` is a clobbering redirection, not a pipe.
			cur.WriteByte(c)
		case c == ';' || c == '\n' || c == '|':
			flush()
		case c == '&':
			// `&>` is a redirection, not a separator.
			if i+1 < len(command) && command[i+1] == '>' {
				cur.WriteByte(c)
				continue
			}
			if i > 0 && command[i-1] == '>' {
				cur.WriteByte(c)
				continue
			}
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return segments
}

// shellWord is one word of a simple command. quoted records that some of it was quoted, which
// stops it being read as an operator; expands records that its value depends on an expansion ($,
// `, a glob), which makes it unknown here -- such a word is never reported as a path.
type shellWord struct {
	text    string
	quoted  bool
	expands bool
}

// shellWords splits a simple command into words, honoring quotes and backslashes.
func shellWords(segment string) []shellWord {
	var words []shellWord
	var cur strings.Builder
	inWord, expands, quoted := false, false, false
	quote := byte(0)
	flush := func() {
		if inWord {
			words = append(words, shellWord{text: cur.String(), quoted: quoted, expands: expands})
		}
		cur.Reset()
		inWord, expands, quoted = false, false, false
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case quote == '"':
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(segment):
				i++
				cur.WriteByte(segment[i])
			case c == '$' || c == '`':
				expands = true
				cur.WriteByte(c)
			default:
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, inWord, quoted = c, true, true
		case c == '\\' && i+1 < len(segment):
			i++
			cur.WriteByte(segment[i])
			inWord, quoted = true, true
		case c == ' ' || c == '\t':
			flush()
		case c == '(' || c == ')' || c == '{' || c == '}':
			flush()
		default:
			if c == '$' || c == '`' || c == '*' || c == '?' || c == '[' || c == '~' {
				expands = true
			}
			cur.WriteByte(c)
			inWord = true
		}
	}
	flush()
	return words
}

// PatchPaths returns the files an apply_patch envelope touches: Add, Update, Delete, and the
// destination of a Move.
func PatchPaths(patch string) []string {
	var paths []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Add File:", "*** Update File:", "*** Delete File:", "*** Move to:"} {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				if p := strings.TrimSpace(rest); p != "" {
					paths = append(paths, p)
				}
			}
		}
	}
	return dedupe(paths)
}

func absoluteOnly(paths []string) []string {
	var out []string
	for _, p := range paths {
		if filepath.IsAbs(p) {
			out = append(out, p)
		}
	}
	return out
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
