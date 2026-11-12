package shellcmd

import (
	"slices"
	"strings"
)

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
// A command's own piece of the line runs from its first word, or from the first earlier stage
// of its pipeline, whose output it reads, to the end of its words and redirections. Inside a
// loop it is the whole of the outermost loop, because a later statement of the body comes
// before the command the next time round; inside a function's body it is the whole line,
// because the body runs wherever the function is called.
//
// The span is that piece, preceded by the own piece of every command before it that writes a
// file, which it may then read or run without naming it (`cat > x.sh <<EOF .. EOF; sh x.sh`,
// `echo .. > Makefile; make`). Pieces are in the order of the line, a newline between two
// that do not touch, and each is followed by the bodies of the here-documents of its
// commands, which a command line holds after the line they are named on. What lies between
// two pieces (a commit, a test run) is not in the span.
//
// When a word, a redirection or a NAME=value prefix of the command, or of one of those
// writers, takes a variable's value, the span is instead everything from the start of the
// line to the end of its last piece: the variable may hold anything the line said before.
// A variable the environment sets (HOME, PWD, PATH and the like) does not count unless the
// line assigns it.
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
	var pieces []piece
	for i := range commands {
		c := &commands[i]
		if c.shell != 0 {
			out[i] = out[c.shell-1]
			continue
		}
		pieces = append(pieces[:0], ownPiece(commands, i))
		fromStart := takesVariable(commands, i)
		for j := range commands {
			if d := &commands[j]; d.shell == 0 && d.Offset < c.Offset && wrote[j] {
				pieces = append(pieces, ownPiece(commands, j))
				fromStart = fromStart || takesVariable(commands, j)
			}
		}
		slices.SortFunc(pieces, func(a, b piece) int { return a.start - b.start })
		if fromStart {
			last := pieces[0].end
			for _, p := range pieces {
				last = max(last, p.end)
			}
			pieces = append(pieces[:0], piece{0, last})
		}
		// Pieces that overlap or touch are one.
		merged := pieces[:1]
		for _, p := range pieces[1:] {
			if last := &merged[len(merged)-1]; p.start <= last.end {
				last.end = max(last.end, p.end)
			} else {
				merged = append(merged, p)
			}
		}
		out[i] = spanText(commands, c.source, merged)
	}
	return out
}

// piece is a byte range of a command line.
type piece struct{ start, end int }

// ownPiece is the range of the line that command i, a command of the line itself, covers by
// itself: see Spans.
func ownPiece(commands []Command, i int) piece {
	c := &commands[i]
	p := piece{c.Offset, max(c.end, c.Offset)}
	if c.scopeEnd > 0 {
		p.start, p.end = min(p.start, c.scopeStart), max(p.end, c.scopeEnd)
	}
	for j := range commands {
		if d := &commands[j]; d.shell == 0 && d.Offset < c.Offset && d.Pipeline == c.Pipeline && d.Stage < c.Stage {
			p.start = min(p.start, d.Offset)
		}
	}
	p.end = min(p.end, len(c.source))
	p.start = min(p.start, p.end)
	return p
}

// spanText writes pieces, which are sorted and do not touch, as one text: each followed by
// the here-document bodies of its commands that no piece already holds.
func spanText(commands []Command, source string, pieces []piece) string {
	inside := func(offset int) bool {
		return slices.ContainsFunc(pieces, func(p piece) bool { return offset >= p.start && offset < p.end })
	}
	var b *strings.Builder
	for k, p := range pieces {
		text := source[p.start:p.end]
		if k > 0 {
			b.WriteByte('\n')
			b.WriteString(text)
		}
		for j := range commands {
			d := &commands[j]
			if d.shell != 0 || d.Offset < p.start || d.Offset >= p.end {
				continue
			}
			for _, h := range d.hdocs {
				if inside(h[0]) || h[1] >= len(d.Redirects) {
					continue
				}
				if b == nil {
					b = &strings.Builder{}
					b.WriteString(text)
				}
				b.WriteByte('\n')
				b.WriteString(d.Redirects[h[1]].Body)
			}
		}
		if b == nil && len(pieces) > 1 {
			b = &strings.Builder{}
			b.WriteString(text)
		}
	}
	if b == nil {
		return source[pieces[0].start:pieces[0].end]
	}
	return b.String()
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
