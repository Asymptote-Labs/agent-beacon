package shellcmd

import (
	"math"
	"path"
	"strings"
)

// directories works out each command's Dir, in walk order: the directory the last cd or
// pushd before it left the shell it runs in. A change of directory reaches the commands after
// it up to the end of the shell it ran in: the subshell, substitution, pipeline stage or
// background statement around it (isolated), or the line. A change inside a loop or a
// function's body is to an unknown directory, and may come before any command of that loop,
// or of the line, so those have no directory either.
func (p *parser) directories() {
	type change struct {
		offset, start, end int
		dir                string
	}
	var changes []change
	var unknown [][2]int // the loops and functions whose bodies change directory
	for i := range p.commands {
		c := &p.commands[i]
		for k := len(changes) - 1; k >= 0; k-- {
			if d := changes[k]; d.offset < c.Offset && c.Offset >= d.start && c.Offset < d.end {
				c.Dir, c.dirSet = d.dir, true
				break
			}
		}
		switch c.unwrapped.program {
		case "cd", "pushd", "popd":
		default:
			continue
		}
		dir := ""
		if c.scopeEnd != 0 {
			unknown = append(unknown, [2]int{c.scopeStart, c.scopeEnd})
		} else {
			dir = c.changesInto()
		}
		start, end := 0, math.MaxInt
		for _, s := range p.isolated {
			if uint(c.Offset) >= s.start && uint(c.Offset) < s.end && int(s.start) >= start {
				start, end = int(s.start), int(s.end)
			}
		}
		changes = append(changes, change{offset: c.Offset, start: start, end: end, dir: dir})
	}
	for _, scope := range unknown {
		for i := range p.commands {
			if c := &p.commands[i]; c.Offset >= scope[0] && c.Offset < scope[1] {
				c.Dir, c.dirSet = "", true
			}
		}
	}
}

// changesInto returns the directory a cd or pushd changes into when its operand is a literal
// path, absolute or relative to a known Dir, written with one trailing slash as file_writes
// writes a directory, and "" otherwise: cd alone (home), cd -, popd, a stack entry (+1), a
// word the parse cannot evaluate, a relative path from an unknown directory.
func (c *Command) changesInto() string {
	words, ok := c.Unwrapped()
	if !ok || c.unwrapped.program == "popd" {
		return ""
	}
	var target *Word
	for k := 1; k < len(words); k++ {
		w := &words[k]
		if w.Known && w.Text == "--" && target == nil {
			if k+1 < len(words) {
				target = &words[k+1]
			}
			break
		}
		if w.Known && strings.HasPrefix(w.Text, "-") && len(w.Text) > 1 {
			continue
		}
		target = w
		break
	}
	if target == nil || !target.Known || target.Text == "" || target.Text == "-" || strings.HasPrefix(target.Text, "+") {
		return ""
	}
	dir := ""
	switch {
	case path.IsAbs(target.Text):
		dir = path.Clean(target.Text)
	case c.dirSet && c.Dir != "":
		dir = path.Join(c.Dir, target.Text)
	default:
		return ""
	}
	if dir == "/" {
		return dir
	}
	return dir + "/"
}

// inheritsDir reports whether the command line a command hands on starts in the command's
// directory: a shell's or an interpreter's script, eval's and xargs's command line run here.
// One that ssh, docker exec and the like run, or a wrapper moves (sudo -i, env -C), runs
// somewhere else.
func (c *Command) inheritsDir() bool {
	if c.unwrapped.how != ran {
		return false
	}
	switch p := c.unwrapped.program; p {
	case "eval", "xargs":
		return true
	default:
		if isShell(p) {
			return true
		}
		_, ok := scriptLanguage(p)
		return ok
	}
}
