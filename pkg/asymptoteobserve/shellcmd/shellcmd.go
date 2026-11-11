// Package shellcmd reads a captured shell command line as bash does: the simple commands it runs,
// their words, environment prefixes and redirections, and the files those commands evidently
// write and read. It is deliberately shallow. A word whose value depends on something it does not
// evaluate -- a parameter, a command or arithmetic substitution, a glob, a leading tilde -- is
// marked unknown rather than guessed, and a command line that does not parse is an error, because
// it was most likely captured incomplete.
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
	// Unquoted is the word with its quotes and escaping backslashes removed and everything else as
	// written. It is Text when Known; otherwise each part the parse does not evaluate stays as its
	// source, so "$HOME/.aws/credentials" is $HOME/.aws/credentials and ~/.ssh/id_* is itself.
	Unquoted string
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
	// of a here-document, or the string of a here-string.
	Target Word
	// Body is a here-document's text as written, expansions included, without the line that
	// ends it.
	Body string
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
	// ({ a; b; } | c, (a) | c, if ...; fi | c) carries that stage, since its output is the
	// stage's, unless it is itself a stage of an inner pipeline or inside a substitution.
	Pipeline int
	Stage    int
	// Nested is true inside a command substitution ($(...), `...`) or a process substitution.
	Nested bool
	// AfterCD is true when a cd, pushd or popd comes earlier in the command line, so a relative
	// path no longer names the file it would at the start. The directory itself is not tracked.
	AfterCD bool
	// within identifies the command or process substitution the statement is directly inside, or
	// is 0. outer is 1 + the index of the innermost compound statement around it that carries
	// redirections of its own ({ ..; } > f, (..) > f, if ..; fi > f), or 0: that statement's
	// redirections apply to this one's output too. refs are what its words take their value from,
	// and the variables that export, declare, local, readonly or typeset assigned since the
	// statement before it: those are no statements of their own here. FileReads follows all three.
	within int32
	outer  int32
	refs   []wordRef
	// subshell identifies the innermost ( .. ) the statement is inside, or is 0: what an exec
	// there leaves open is closed when the subshell ends.
	subshell int32
}

// wordRef is one thing a word of a command takes its value from: a command substitution or an
// input process substitution ($(..), `..`, <(..)), by the id its statements carry in within, or
// else a variable, by name. With the place refDeclares it is instead a variable a declaration
// assigned, and the refs of its value follow with the place refDeclared and the same index.
type wordRef struct {
	place refPlace
	index int // of the word in Argv, Redirects or Env, or of the declaration
	subst int
	name  string
}

// refPlace is where the word of a wordRef is.
type refPlace uint8

const (
	refArg refPlace = iota
	refRedirect
	refEnv
	refDeclares
	refDeclared
)

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

// compound is the byte range of a compound statement that is a pipeline stage, and that stage.
type compound struct {
	start, end uint
	stage      stage
}

// substitution is the byte range of a command or process substitution, and its id.
type substitution struct {
	start, end uint
	id         int
}

// redirected is the byte range of a compound statement that carries redirections, and its
// index in the commands.
type redirected struct {
	start, end uint
	index      int
}

type parser struct {
	source     string
	commands   []Command
	stages     map[*syntax.Stmt]stage
	containers map[*syntax.Stmt]bool // statements that only join pipeline stages
	compounds  []compound            // compound statements that are pipeline stages
	redirected []redirected          // compound statements with redirections of their own
	pipelines  int
	nested     []substitution
	subshells  []substitution // id is the subshell's start offset + 1
	substIDs   map[syntax.Node]int
	declared   []declaration // not yet carried by a statement
	moved      bool
}

// declaration is an assignment by export, declare, local, readonly or typeset, and where its
// statement ends.
type declaration struct {
	end   uint
	name  string
	value []syntax.WordPart
}

func (p *parser) visit(node syntax.Node) bool {
	switch n := node.(type) {
	case *syntax.CmdSubst:
		p.nested = append(p.nested, substitution{n.Left.Offset(), n.Right.Offset(), p.substID(n)})
	case *syntax.ProcSubst:
		p.nested = append(p.nested, substitution{n.OpPos.Offset(), n.Rparen.Offset(), p.substID(n)})
	case *syntax.Subshell:
		p.subshells = append(p.subshells, substitution{n.Lparen.Offset(), n.Rparen.Offset(), int(n.Lparen.Offset()) + 1})
	case *syntax.Stmt:
		p.stmt(n)
	}
	return true
}

// substID is the id of a substitution: the same for the word that holds it and the statements
// inside it, whichever is reached first.
func (p *parser) substID(node syntax.Node) int {
	id, ok := p.substIDs[node]
	if !ok {
		if p.substIDs == nil {
			p.substIDs = map[syntax.Node]int{}
		}
		id = len(p.substIDs) + 1
		p.substIDs[node] = id
	}
	return id
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
	st, ok := p.stages[stmt]
	if ok && call == nil && stmt.Cmd != nil {
		p.compounds = append(p.compounds, compound{start: stmt.Pos().Offset(), end: stmt.End().Offset(), stage: st})
	}
	if decl, ok := stmt.Cmd.(*syntax.DeclClause); ok {
		for _, as := range decl.Args {
			if as.Name != nil && as.Value != nil {
				p.declared = append(p.declared, declaration{end: stmt.End().Offset(), name: as.Name.Value, value: as.Value.Parts})
			}
		}
	}
	if call == nil && len(stmt.Redirs) == 0 {
		return
	}
	if !ok {
		if st, ok = p.enclosingStage(stmt.Pos().Offset()); !ok {
			p.pipelines++
			st = stage{pipeline: p.pipelines}
		}
	}
	offset := stmt.Pos().Offset()
	c := Command{
		Offset:   int(offset),
		Pipeline: st.pipeline,
		Stage:    st.index,
		AfterCD:  p.moved,
	}
	// The innermost substitution around the statement is the one that starts last.
	var innermost uint
	for _, s := range p.nested {
		if offset > s.start && offset < s.end {
			c.Nested = true
			if s.start >= innermost {
				innermost, c.within = s.start, int32(s.id)
			}
		}
	}
	// The innermost redirected compound around the statement, unless a substitution inside it is
	// closer: a substitution's output is not the compound's.
	var around uint
	for _, r := range p.redirected {
		if offset > r.start && offset < r.end && r.start >= around && (!c.Nested || innermost < r.start) {
			around, c.outer = r.start, int32(r.index+1)
		}
	}
	for _, s := range p.subshells {
		if offset > s.start && offset < s.end && int32(s.id) > c.subshell {
			c.subshell = int32(s.id)
		}
	}
	// The statement carries the declarations that ended before it: not one it is inside.
	pending, carried := p.declared[:0], 0
	for _, d := range p.declared {
		if d.end > offset {
			pending = append(pending, d)
			continue
		}
		c.refs = append(c.refs, wordRef{place: refDeclares, index: carried, name: d.name})
		c.refs = p.refs(c.refs, d.value, refDeclared, carried)
		carried++
	}
	p.declared = pending
	if call != nil {
		for i, as := range call.Assigns {
			c.Env = append(c.Env, p.assign(as))
			if as.Value != nil {
				c.refs = p.refs(c.refs, as.Value.Parts, refEnv, i)
			}
		}
		c.Argv, c.refs = p.callArgs(call, c.refs)
	}
	for i, r := range stmt.Redirs {
		c.Redirects = append(c.Redirects, p.redirect(r))
		if r.Word != nil {
			c.refs = p.refs(c.refs, r.Word.Parts, refRedirect, i)
		}
	}
	p.commands = append(p.commands, c)
	if call == nil && stmt.Cmd != nil && len(stmt.Redirs) > 0 {
		p.redirected = append(p.redirected, redirected{start: offset, end: stmt.End().Offset(), index: len(p.commands) - 1})
	}
	// After a cd, a relative path is relative to somewhere this parser does not track.
	if words, _ := c.Unwrapped(); len(words) > 0 && words[0].Known {
		switch words[0].Text {
		case "cd", "pushd", "popd":
			p.moved = true
		}
	}
}

// enclosingStage returns the stage of the innermost compound stage around offset, unless a
// substitution inside it is closer: a substitution's output is not the stage's.
func (p *parser) enclosingStage(offset uint) (stage, bool) {
	var best compound
	found := false
	for _, c := range p.compounds {
		if offset > c.start && offset < c.end && (!found || c.start > best.start) {
			best, found = c, true
		}
	}
	if !found {
		return stage{}, false
	}
	for _, s := range p.nested {
		if offset > s.start && offset < s.end && s.start > best.start {
			return stage{}, false
		}
	}
	return best.stage, true
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
	if r.Hdoc != nil {
		out.Body = hereBody(p.raw(r.Hdoc), out.Target.Unquoted, r.Op == syntax.DashHdoc)
	}
	return out
}

// hereBody returns a here-document's text without the delimiter line that ends it, which the
// source range of the document's word may run to. tabs is set for <<-, whose delimiter may be
// indented with tabs.
func hereBody(raw, delimiter string, tabs bool) string {
	body := strings.TrimSuffix(raw, "\n")
	start := strings.LastIndexByte(body, '\n') + 1
	last := body[start:]
	if tabs {
		last = strings.TrimLeft(last, "\t")
	}
	if last == delimiter {
		return raw[:start]
	}
	return raw
}

// callArgs returns the call's words after brace expansion, and refs with what they take their
// value from.
func (p *parser) callArgs(call *syntax.CallExpr, refs []wordRef) ([]Word, []wordRef) {
	var words []Word
	for _, word := range call.Args {
		// SplitBraces rewrites the word's parts, so it works on a copy: the tree is still being walked.
		split := &syntax.Word{Parts: slices.Clone(word.Parts)}
		if !syntax.SplitBraces(split) {
			refs = p.refs(refs, word.Parts, refArg, len(words))
			words = append(words, p.word(word, word))
			continue
		}
		// Every word a brace expansion produces may hold what the unexpanded word does.
		for _, expanded := range expand.Braces(split) {
			refs = p.refs(refs, word.Parts, refArg, len(words))
			words = append(words, p.word(expanded, word))
		}
	}
	return words, refs
}

// refs appends what a word, given by its parts, takes its value from: the substitutions and the
// variables in it, inside double quotes too.
func (p *parser) refs(refs []wordRef, parts []syntax.WordPart, place refPlace, index int) []wordRef {
	for _, part := range parts {
		switch part := part.(type) {
		case *syntax.DblQuoted:
			refs = p.refs(refs, part.Parts, place, index)
		case *syntax.CmdSubst:
			refs = append(refs, wordRef{place: place, index: index, subst: p.substID(part)})
		case *syntax.ProcSubst:
			if part.Op == syntax.CmdIn {
				refs = append(refs, wordRef{place: place, index: index, subst: p.substID(part)})
			}
		case *syntax.ParamExp:
			if part.Param != nil {
				refs = append(refs, wordRef{place: place, index: index, name: part.Param.Value})
			}
		}
	}
	return refs
}

// word reads word's value; source is the word as written, for Raw.
func (p *parser) word(word, source *syntax.Word) Word {
	w := Word{Raw: p.raw(source)}
	w.Unquoted, w.Known = p.unquote(word)
	if w.Known {
		w.Text = w.Unquoted
	}
	return w
}

// unquote returns word without its quotes and escaping backslashes, and whether that is its
// value: it is not when a part depends on something the parse does not evaluate, and such a part
// is written as its source.
func (p *parser) unquote(word *syntax.Word) (string, bool) {
	var b strings.Builder
	known := true
	for i, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			if i == 0 && strings.HasPrefix(part.Value, "~") {
				known = false
			}
			if !unescape(&b, part.Value, false) {
				known = false
			}
		case *syntax.SglQuoted:
			if part.Dollar {
				known = false
				b.WriteString(p.raw(part))
				continue
			}
			b.WriteString(part.Value)
		case *syntax.DblQuoted:
			if part.Dollar {
				known = false
			}
			for _, inner := range part.Parts {
				if lit, ok := inner.(*syntax.Lit); ok {
					unescape(&b, lit.Value, true)
					continue
				}
				known = false
				b.WriteString(p.raw(inner))
			}
		default:
			known = false
			b.WriteString(p.raw(part))
		}
	}
	return b.String(), known
}

// unescape writes a literal's value as the shell passes it on, removing the backslashes it
// removes. Outside quotes it reports false for an unescaped glob character, whose value depends on
// the file system; the character is written as it is.
func unescape(b *strings.Builder, s string, quoted bool) bool {
	plain := true
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
			plain = false
		}
		b.WriteByte(c)
	}
	return plain
}
