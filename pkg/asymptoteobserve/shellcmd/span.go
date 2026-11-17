package shellcmd

import (
	"slices"
	"sort"
	"strings"
)

// spans returns, for each command of a command line, the part of the line that can decide what
// the command runs: its own words, redirections and here-documents, and whatever of the line
// before it can reach it. The result is parallel to commands, and so is writes, each command's
// FileWrites.
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
// file's content (writesContent), which it may then read or run without naming it
// (`cat > x.sh <<EOF .. EOF; sh x.sh`, `echo .. > Makefile; make`). Pieces are in the order of
// the line, a newline between two that do not touch, and each is followed by the bodies of the
// here-documents of its commands, which a command line holds after the line they are named on.
// What lies between two pieces (a commit, a test run) is not in the span.
//
// When a word, a redirection or a NAME=value prefix of the command, or of one of those
// writers, takes a variable's value, the span is instead everything from the start of the
// line to the end of its last piece: the variable may hold anything the line said before.
// A variable the environment sets (HOME, PWD, PATH and the like) does not count unless the
// line assigns it.
//
// A command withScripts added (one of a shell's script, or of what a carrier runs) has the
// span of the command of the line itself that it came from: that command's words hold the
// script, and what reaches it reaches them.
//
// The span is an over-approximation, on purpose. It is not followed across a background job
// that writes a file while a later command runs, or a file the line names nowhere.
func spans(commands []Command, writes [][]FileEffect) []string {
	out := make([]string, len(commands))
	// wrote[i], for a command of the line itself, is whether it or a command it handed on
	// writes a file's content.
	wrote := make([]bool, len(commands))
	for i := range commands {
		if slices.ContainsFunc(writes[i], FileEffect.writesContent) {
			top := i
			for commands[top].shell != 0 {
				top = int(commands[top].shell) - 1
			}
			wrote[top] = true
		}
	}
	// A line of a thousand short commands is four kilobytes, and every command is asked about
	// every writer before it, so what is asked of a command is worked out once: its own piece,
	// whether it takes a variable's value, and which commands write or carry a here-document.
	own := ownPieces(commands)
	takes := make([]int8, len(commands)) // 0 not asked yet, 1 yes, -1 no
	takesVar := func(i int) bool {
		if takes[i] == 0 {
			takes[i] = -1
			if takesVariable(commands, i) {
				takes[i] = 1
			}
		}
		return takes[i] > 0
	}
	var writers, hdocs []int
	for j := range commands {
		if commands[j].shell != 0 {
			continue
		}
		if wrote[j] {
			writers = append(writers, j)
		}
		if len(commands[j].hdocs) > 0 {
			hdocs = append(hdocs, j)
		}
	}
	slices.SortStableFunc(writers, func(a, b int) int { return own[a].start - own[b].start })
	// add joins a piece to the ones before it, which start no later: pieces that overlap or
	// touch are one.
	var pieces []piece
	add := func(p piece) {
		if n := len(pieces); n > 0 && p.start <= pieces[n-1].end {
			pieces[n-1].end = max(pieces[n-1].end, p.end)
		} else {
			pieces = append(pieces, p)
		}
	}
	for i := range commands {
		c := &commands[i]
		if c.shell != 0 {
			out[i] = out[c.shell-1]
			continue
		}
		fromStart := takesVar(i)
		pieces = pieces[:0]
		placed := false
		for _, j := range writers {
			if commands[j].Offset >= c.Offset {
				continue
			}
			if !placed && own[i].start <= own[j].start {
				add(own[i])
				placed = true
			}
			add(own[j])
			fromStart = fromStart || takesVar(j)
		}
		if !placed {
			add(own[i])
		}
		if fromStart {
			// The pieces are in order and apart, so the last one ends last.
			pieces = append(pieces[:0], piece{0, pieces[len(pieces)-1].end})
		}
		out[i] = spanText(commands, hdocs, c.source, pieces)
	}
	return out
}

// piece is a byte range of a command line.
type piece struct{ start, end int }

// ownPieces returns, for each command of the line itself, the range of the line it covers by
// itself: see Spans.
func ownPieces(commands []Command) []piece {
	// The stages of each pipeline in order, each with the earliest offset of a command at that
	// stage or before it: a command's piece starts at the earliest of the stages before its own.
	type stageStart struct{ stage, offset int }
	pipelines := map[int][]stageStart{}
	for j := range commands {
		if d := &commands[j]; d.shell == 0 {
			pipelines[d.Pipeline] = append(pipelines[d.Pipeline], stageStart{d.Stage, d.Offset})
		}
	}
	for _, stages := range pipelines {
		slices.SortStableFunc(stages, func(a, b stageStart) int { return a.stage - b.stage })
		for k := 1; k < len(stages); k++ {
			stages[k].offset = min(stages[k].offset, stages[k-1].offset)
		}
	}
	own := make([]piece, len(commands))
	for i := range commands {
		c := &commands[i]
		if c.shell != 0 {
			continue
		}
		p := piece{c.Offset, max(c.end, c.Offset)}
		if c.scopeEnd > 0 {
			p.start, p.end = min(p.start, c.scopeStart), max(p.end, c.scopeEnd)
		}
		stages := pipelines[c.Pipeline]
		if k := sort.Search(len(stages), func(k int) bool { return stages[k].stage >= c.Stage }); k > 0 {
			// Only a command that comes before this one counts; the earliest of the stages
			// before it is one whenever any is.
			if first := stages[k-1].offset; first < c.Offset {
				p.start = min(p.start, first)
			}
		}
		p.end = min(p.end, len(c.source))
		p.start = min(p.start, p.end)
		own[i] = p
	}
	return own
}

// spanText writes pieces, which are in order and do not touch, as one text: each followed by
// the here-document bodies of its commands that no piece already holds. hdocs are the commands
// of the line itself that have a here-document.
func spanText(commands []Command, hdocs []int, source string, pieces []piece) string {
	if len(pieces) == 1 && len(hdocs) == 0 {
		return source[pieces[0].start:pieces[0].end]
	}
	// holding is the piece that holds an offset, or -1.
	holding := func(offset int) int {
		k := sort.Search(len(pieces), func(k int) bool { return pieces[k].end > offset })
		if k < len(pieces) && pieces[k].start <= offset {
			return k
		}
		return -1
	}
	// The bodies that follow each piece, in the order of their commands.
	type body struct {
		piece int
		text  string
	}
	var bodies []body
	size := 0
	for _, j := range hdocs {
		d := &commands[j]
		k := holding(d.Offset)
		if k < 0 {
			continue
		}
		for _, h := range d.hdocs {
			if holding(h[0]) >= 0 || h[1] >= len(d.Redirects) {
				continue
			}
			bodies = append(bodies, body{k, d.Redirects[h[1]].Body})
			size += 1 + len(d.Redirects[h[1]].Body)
		}
	}
	if len(pieces) == 1 && len(bodies) == 0 {
		return source[pieces[0].start:pieces[0].end]
	}
	slices.SortStableFunc(bodies, func(a, b body) int { return a.piece - b.piece })
	for _, p := range pieces {
		size += 1 + p.end - p.start
	}
	var b strings.Builder
	b.Grow(size)
	next := 0
	for k, p := range pieces {
		if k > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(source[p.start:p.end])
		for ; next < len(bodies) && bodies[next].piece == k; next++ {
			b.WriteByte('\n')
			b.WriteString(bodies[next].text)
		}
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
