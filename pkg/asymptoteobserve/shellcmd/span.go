package shellcmd

import "strings"

// Spans returns, for each command of a parsed command line, the part of the line that can
// decide what the command runs: its own words, redirections and here-documents, and whatever
// of the line before it can reach it. The result is parallel to commands. writes[i] says
// whether command i writes a file's content.
//
// A program this package cannot see into (an interpreter, a project's script, a tool nobody
// listed) runs what it is given, and on a command line that is text: its words, its standard
// input, a file it reads, a variable. Spans is for the caller that must ask whether such a
// command could be running something, and wants to ask it of the text that could reach the
// command and not of the whole line, where the same words in a commit message three commands
// away would answer for it.
//
// The span of a command starts at its own first word, or earlier:
//
//   - at the first earlier stage of its pipeline, whose output it reads;
//   - at the first command before it that writes a file, which it may then read or run
//     (`cat > x.sh <<EOF .. EOF; sh x.sh`, `echo .. > Makefile; make`);
//   - at the start of the line when one of its words, redirections or NAME=value prefixes
//     takes a variable's value, unless the variable is one the environment sets (HOME, PWD,
//     PATH and the like) and the line does not assign it;
//   - at the start of the outermost loop around it, and it then runs to that loop's end,
//     because a later statement of the body comes before it the next time round;
//   - at the start of the line, running to its end, inside a function's body, which runs
//     wherever the function is called.
//
// It ends where the command's words and redirections end, followed by the bodies of the
// here-documents of the commands in it, which a command line holds after the line they are
// named on. A substitution in a word is part of the word.
//
// A command WithScripts added (one of a shell's script, or of what a carrier runs) has the
// span of the command of the line itself that it came from: that command's words hold the
// script, and what reaches it reaches them.
//
// The span is an over-approximation, on purpose. It is not followed across a background job
// that writes a file while a later command runs, or a file the line names nowhere.
func Spans(commands []Command, writes []bool) []string {
	out := make([]string, len(commands))
	// wrote[i], for a command of the line itself, is whether it or a command it handed on
	// writes a file.
	wrote := make([]bool, len(commands))
	for i := range commands {
		if i < len(writes) && writes[i] {
			top := i
			for commands[top].shell != 0 {
				top = int(commands[top].shell) - 1
			}
			wrote[top] = true
		}
	}
	for i := range commands {
		c := &commands[i]
		if c.shell != 0 {
			out[i] = out[c.shell-1]
			continue
		}
		start, end := c.Offset, max(c.end, c.Offset)
		if c.scopeEnd > 0 {
			start, end = min(start, c.scopeStart), max(end, c.scopeEnd)
		}
		if takesVariable(commands, i) {
			start = 0
		} else {
			for j := range commands {
				d := &commands[j]
				if d.shell != 0 || d.Offset >= c.Offset {
					continue
				}
				if wrote[j] || (d.Pipeline == c.Pipeline && d.Stage < c.Stage) {
					start = min(start, d.Offset)
				}
			}
		}
		if start > end || end > len(c.source) {
			continue
		}
		span := c.source[start:end]
		var b *strings.Builder
		for j := range commands {
			d := &commands[j]
			if d.shell != 0 || d.Offset < start || d.Offset > c.Offset {
				continue
			}
			for _, h := range d.hdocs {
				if h[0] < end || h[1] >= len(d.Redirects) {
					continue // inside the span already
				}
				if b == nil {
					b = &strings.Builder{}
					b.WriteString(span)
				}
				b.WriteByte('\n')
				b.WriteString(d.Redirects[h[1]].Body)
			}
		}
		if b != nil {
			span = b.String()
		}
		out[i] = span
	}
	return out
}

// ambientVariables are set by the shell or the environment it starts in, not by text on the
// command line, unless the line assigns them.
var ambientVariables = map[string]bool{
	"HOME": true, "PWD": true, "OLDPWD": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TMPDIR": true, "HOSTNAME": true, "PATH": true, "LANG": true, "TERM": true, "UID": true,
	"EUID": true, "PPID": true, "RANDOM": true, "SECONDS": true, "LINENO": true, "OSTYPE": true,
	"HOSTTYPE": true, "?": true, "$": true, "#": true, "!": true,
}

// takesVariable reports whether a word, redirection or prefix assignment of command i takes
// the value of a variable that text on the command line may have set.
func takesVariable(commands []Command, i int) bool {
	c := &commands[i]
	for _, ref := range c.refs {
		if ref.name == "" || (ref.place != refArg && ref.place != refRedirect && ref.place != refEnv) {
			continue
		}
		if !ambientVariables[ref.name] || assignedBefore(commands, i, ref.name) {
			return true
		}
	}
	return false
}

// assignedBefore reports whether a statement of the line itself, at or before command i,
// assigns name: NAME=value on its own, or a declaration.
func assignedBefore(commands []Command, i int, name string) bool {
	c := &commands[i]
	for j := range commands {
		d := &commands[j]
		if d.shell != 0 || d.Offset > c.Offset {
			continue
		}
		for _, ref := range d.refs {
			if ref.place == refDeclares && ref.name == name {
				return true
			}
		}
		if len(d.Argv) == 0 && d.Offset < c.Offset {
			for _, as := range d.Env {
				if as.Name == name {
					return true
				}
			}
		}
	}
	return false
}
