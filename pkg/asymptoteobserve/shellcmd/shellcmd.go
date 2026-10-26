// Package shellcmd reads a captured shell command line as bash does: the simple commands it runs,
// their words, environment prefixes and redirections, and the files those commands evidently
// write. It is deliberately shallow. A word whose value depends on something it does not evaluate
// -- a parameter, a command or arithmetic substitution, a glob, a leading tilde -- is marked
// unknown rather than guessed, and a command line that does not parse is an error, because it was
// most likely captured incomplete.
//
// It lives apart from the root package so that the binaries that import only the root package do
// not link the shell parser.
package shellcmd

import (
	"errors"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Word is one word of a command after brace expansion.
type Word struct {
	// Text is the value as the shell passes it on, quotes and escaping backslashes removed. It is
	// empty when Known is false.
	Text string
	// Raw is the word's source text, quotes and backslashes included. Every word a brace
	// expansion produces carries the source of the whole unexpanded word. Raw keeps what bash
	// would remove, such as the backslashes of a Windows path (C:\Tools\beacon.exe, whose Text
	// is C:Toolsbeacon.exe).
	Raw string
	// Known is false when the value depends on a parameter, a command or arithmetic substitution,
	// an ANSI-C ($'..') or locale ($"..") string, an unquoted glob character, or a leading tilde.
	Known bool
}

// Assign is an environment assignment before a command (NAME=value cmd).
type Assign struct {
	Name string
	// Value is unknown for an append (+=), an indexed or an array assignment.
	Value Word
}

// Redirect is one redirection of a command.
type Redirect struct {
	// Op is the operator as written: ">", ">>", ">|", "&>", "&>>", ">&", "<", "<>", "<&", "<<",
	// "<<-" or "<<<".
	Op string
	// Fd is the descriptor written before the operator (the 2 of 2>), or "".
	Fd string
	// Target is the word after the operator: a file, a descriptor for >& and <&, the delimiter
	// of a here-document, or the string of a here-string. A here-document's body is not read.
	Target Word
}

// Command is one statement of a command line: a simple command with its words, or a compound
// one (a subshell, block, if, loop) that has redirections of its own, in which case Argv is empty.
type Command struct {
	// Env are the assignments before the command.
	Env []Assign
	// Argv are the command's words, wrappers (sudo, env, exec, ...) included. Unwrapped skips them.
	Argv      []Word
	Redirects []Redirect
	// Offset is the byte offset of the statement in the command line.
	Offset int
	// Pipeline identifies the pipeline the statement is a stage of: the stages of one pipeline
	// share it, and a statement outside a pipeline has one of its own. Stage is its 0-based
	// position in that pipeline (curl ... | sh: sh is stage 1). A command inside a compound stage
	// ({ a; b; } | c) is not itself a stage of the outer pipeline.
	Pipeline int
	Stage    int
	// Nested is true inside a command substitution ($(...), `...`) or a process substitution.
	Nested bool
	// AfterCD is true when a cd, pushd or popd comes earlier in the command line, so a relative
	// path no longer names the file it would at the start. The directory itself is not tracked.
	AfterCD bool
}

// Parse parses command as bash and returns its statements in walk order: each statement, then the
// statements inside it (an if's body, a command substitution in its words). A statement that
// carries nothing -- no simple command and no redirection, such as the `a && b` around its two
// sides -- is left out.
//
// A command line that does not parse is an error. When the input merely ended early (an unclosed
// quote, here-document, substitution or compound command: a capture cut short), Parse also
// returns the commands of the part before the point where it became incomplete, parsed on its
// own; the last of them may be missing words that the cut removed. Any other parse error returns
// no commands. A caller that must not act on a partial command line ignores the commands
// whenever the error is not nil.
func Parse(command string) ([]Command, error) {
	file, err := parseBash(command)
	if err == nil {
		return walk(command, file), nil
	}
	// The prefix can itself end inside an earlier construct (`a "$(b` cut again at `$(`); each
	// retry is strictly shorter, so this ends.
	prefix, perr := command, err
	for {
		var incomplete syntax.ParseError
		if !errors.As(perr, &incomplete) || !incomplete.Incomplete || int(incomplete.Pos.Offset()) >= len(prefix) {
			return nil, err
		}
		prefix = prefix[:incomplete.Pos.Offset()]
		if file, perr = parseBash(prefix); perr == nil {
			return walk(prefix, file), err
		}
	}
}

func parseBash(command string) (*syntax.File, error) {
	return syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
}

func walk(source string, file *syntax.File) []Command {
	p := parser{source: source, stages: map[*syntax.Stmt]stage{}, containers: map[*syntax.Stmt]bool{}}
	syntax.Walk(file, p.visit)
	return p.commands
}

type stage struct{ pipeline, index int }

type parser struct {
	source     string
	commands   []Command
	stages     map[*syntax.Stmt]stage
	containers map[*syntax.Stmt]bool // statements that only join pipeline stages
	pipelines  int
	nested     [][2]uint // byte ranges of command and process substitutions
	moved      bool
}

func (p *parser) visit(node syntax.Node) bool {
	switch n := node.(type) {
	case *syntax.CmdSubst:
		p.nested = append(p.nested, [2]uint{n.Left.Offset(), n.Right.Offset()})
	case *syntax.ProcSubst:
		p.nested = append(p.nested, [2]uint{n.OpPos.Offset(), n.Rparen.Offset()})
	case *syntax.Stmt:
		p.stmt(n)
	}
	return true
}

func (p *parser) stmt(stmt *syntax.Stmt) {
	if p.containers[stmt] {
		return
	}
	if isPipe(stmt) {
		p.pipelines++
		for i, s := range p.flatten(stmt, nil) {
			p.stages[s] = stage{pipeline: p.pipelines, index: i}
		}
		return
	}
	call, _ := stmt.Cmd.(*syntax.CallExpr)
	if call == nil && len(stmt.Redirs) == 0 {
		return
	}
	st, ok := p.stages[stmt]
	if !ok {
		p.pipelines++
		st = stage{pipeline: p.pipelines}
	}
	offset := stmt.Pos().Offset()
	c := Command{
		Offset:   int(offset),
		Pipeline: st.pipeline,
		Stage:    st.index,
		AfterCD:  p.moved,
	}
	for _, r := range p.nested {
		if offset > r[0] && offset < r[1] {
			c.Nested = true
			break
		}
	}
	if call != nil {
		for _, as := range call.Assigns {
			c.Env = append(c.Env, p.assign(as))
		}
		c.Argv = p.callArgs(call)
	}
	for _, r := range stmt.Redirs {
		c.Redirects = append(c.Redirects, p.redirect(r))
	}
	p.commands = append(p.commands, c)
	// After a cd, a relative path is relative to somewhere this parser does not track.
	if words, _ := c.Unwrapped(); len(words) > 0 && words[0].Known {
		switch words[0].Text {
		case "cd", "pushd", "popd":
			p.moved = true
		}
	}
}

func isPipe(stmt *syntax.Stmt) bool {
	bin, ok := stmt.Cmd.(*syntax.BinaryCmd)
	return ok && (bin.Op == syntax.Pipe || bin.Op == syntax.PipeAll)
}

// flatten returns the stages of the pipeline stmt, marking the statements that only join them.
func (p *parser) flatten(stmt *syntax.Stmt, stages []*syntax.Stmt) []*syntax.Stmt {
	if !isPipe(stmt) {
		return append(stages, stmt)
	}
	p.containers[stmt] = true
	bin := stmt.Cmd.(*syntax.BinaryCmd)
	return p.flatten(bin.Y, p.flatten(bin.X, stages))
}

func (p *parser) raw(node syntax.Node) string {
	start, end := node.Pos().Offset(), node.End().Offset()
	if start > end || end > uint(len(p.source)) {
		return ""
	}
	return p.source[start:end]
}

func (p *parser) assign(as *syntax.Assign) Assign {
	a := Assign{Name: as.Name.Value}
	switch {
	case as.Append || as.Index != nil || as.Array != nil:
		if as.Value != nil {
			a.Value.Raw = p.raw(as.Value)
		}
	case as.Value == nil:
		a.Value.Known = true
	default:
		a.Value = p.word(as.Value, as.Value)
	}
	return a
}

func (p *parser) redirect(r *syntax.Redirect) Redirect {
	out := Redirect{Op: r.Op.String()}
	if r.N != nil {
		out.Fd = r.N.Value
	}
	if r.Word != nil {
		out.Target = p.word(r.Word, r.Word)
	}
	return out
}

func (p *parser) callArgs(call *syntax.CallExpr) []Word {
	var words []Word
	for _, word := range call.Args {
		// SplitBraces rewrites the word's parts, so it works on a copy: the tree is still being walked.
		split := &syntax.Word{Parts: slices.Clone(word.Parts)}
		if !syntax.SplitBraces(split) {
			words = append(words, p.word(word, word))
			continue
		}
		for _, expanded := range expand.Braces(split) {
			words = append(words, p.word(expanded, word))
		}
	}
	return words
}

// word reads word's value; source is the word as written, for Raw.
func (p *parser) word(word, source *syntax.Word) Word {
	w := Word{Raw: p.raw(source)}
	w.Text, w.Known = wordText(word)
	return w
}

func wordText(word *syntax.Word) (string, bool) {
	var b strings.Builder
	for i, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if i == 0 && strings.HasPrefix(p.Value, "~") {
				return "", false
			}
			if !unescape(&b, p.Value, false) {
				return "", false
			}
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return "", false
			}
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				unescape(&b, lit.Value, true)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// unescape writes a literal's value as the shell passes it on, removing the backslashes it
// removes. Outside quotes it reports false for an unescaped glob character, whose value depends on
// the file system.
func unescape(b *strings.Builder, s string, quoted bool) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && (!quoted || strings.IndexByte("$`\"\\\n", s[i+1]) >= 0) {
			i++
			if s[i] != '\n' {
				b.WriteByte(s[i])
			}
			continue
		}
		if !quoted && (c == '*' || c == '?' || c == '[') {
			return false
		}
		b.WriteByte(c)
	}
	return true
}
