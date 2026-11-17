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

// redirKind is what a redirection does, by its operator.
type redirKind uint8

const (
	redirOut          redirKind = iota // >, >|: the descriptor to a file
	redirAppend                        // >>
	redirOutErr                        // &>: standard output and standard error to a file
	redirOutErrAppend                  // &>>
	redirDupOut                        // >&: a copy of a descriptor, or (bash) a file
	redirIn                            // <: the descriptor from a file
	redirInOut                         // <>
	redirDupIn                         // <&: a copy of a descriptor
	redirHereDoc                       // <<, <<-
	redirHereString                    // <<<
)

// kind is what the redirection does: every question about a redirection asks this, never its
// operator's text.
func (r *Redirect) kind() redirKind {
	switch r.Op {
	case ">>":
		return redirAppend
	case "&>":
		return redirOutErr
	case "&>>":
		return redirOutErrAppend
	case ">&":
		return redirDupOut
	case "<":
		return redirIn
	case "<>":
		return redirInOut
	case "<&":
		return redirDupIn
	case "<<", "<<-":
		return redirHereDoc
	case "<<<":
		return redirHereString
	}
	return redirOut
}

// descriptor is the descriptor the redirection is of: the one written before its operator, or
// the operator's own, standard input (0) for the ones that read and standard output (1)
// otherwise. &> and &>> are of standard error too.
func (r *Redirect) descriptor() string {
	switch {
	case r.Fd != "":
		return r.Fd
	case r.kind() >= redirIn:
		return "0"
	}
	return "1"
}

// ofStdout reports whether the redirection sends the command's standard output elsewhere.
func (r *Redirect) ofStdout() bool {
	switch r.kind() {
	case redirOutErr, redirOutErrAppend:
		return true
	case redirOut, redirAppend, redirDupOut:
		return r.descriptor() == "1"
	}
	return false
}

// stdinRedirect returns the redirection that supplies the command's standard input, the last
// one of descriptor 0 that reads, and its index in Redirects; nil when there is none.
func (c *Command) stdinRedirect() (*Redirect, int) {
	for k := len(c.Redirects) - 1; k >= 0; k-- {
		if r := &c.Redirects[k]; r.kind() >= redirIn && r.descriptor() == "0" {
			return r, k
		}
	}
	return nil, -1
}

// isDescriptor reports whether the target of >& or <&, as written, names a descriptor: its
// number, the number with the - that moves it (3-), or - alone, which closes it.
func isDescriptor(target string) bool {
	number := strings.TrimSuffix(target, "-")
	return target == "-" || number != "" && strings.Trim(number, "0123456789") == ""
}

// isFile reports whether a path a command writes or reads names a file: not standard input or
// output (-) and not a device (/dev/..., which also holds bash's network pseudo-files). /dev/shm
// is a directory of ordinary files, which a line can stage a file in as it can in /tmp.
func isFile(path string) bool {
	return path != "" && path != "-" && (!strings.HasPrefix(path, "/dev/") || strings.HasPrefix(path, "/dev/shm/"))
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
	// InScript is true for a command of the script a shell of the command line runs (its -c
	// text, or the here-document it reads): WithScripts adds those after the shell.
	InScript bool
	// AfterCD is true when a cd, pushd or popd comes earlier in the command line, so a relative
	// path no longer names the file it would at the start. It does not ask where.
	AfterCD bool
	// Dir is the directory the command runs in when the line changed into it, as a literal
	// path with one trailing slash (/tmp/probe/): the last cd or pushd before the command in
	// the shell it runs in (not one inside a subshell, substitution or pipeline stage that has
	// ended), with an absolute operand or a relative one from a known Dir. It is "" when the
	// line has not changed directory, and
	// when where it did is unknown (cd alone, cd -, popd, an operand that is not a literal, a
	// change inside a loop or a function, a command line ssh or the like runs elsewhere).
	// dirSet is true when the line has changed directory before the command.
	Dir    string
	dirSet bool
	// within identifies the command or process substitution the statement is directly inside, or
	// is 0. outer is 1 + the index of the innermost compound statement around it that carries
	// redirections of its own ({ ..; } > f, (..) > f, if ..; fi > f), or 0: that statement's
	// redirections apply to this one's output too. refs are what its words take their value from,
	// and the variables that export, declare, local, readonly or typeset assigned since the
	// statement before it: those are no statements of their own here. FileReads follows all three.
	within int32
	outer  int32
	refs   []wordRef
	// execEnd is the byte offset at which the innermost ( .. ) subshell or substitution around
	// the statement ends, or 0 outside any: what an exec of the statement leaves open is open up
	// to there (SocketRedirects).
	execEnd int
	// shell is, for a command WithScripts added, one more than the index of the command whose
	// command line it is in, and 0 for a command of the command line itself. handed is what
	// WithScripts read of the command line this command hands on, when it put its commands
	// after it.
	shell  int32
	handed handOff
	// unwrapped is what the command's wrappers leave of it, worked out when it is read.
	unwrapped unwrapped
	// source is the text the statement was parsed from (the command line, or the script it is
	// in), end the offset where its words and redirections end, and hdocs its here-documents,
	// each the offset of its body, which comes after the line it is named on, and the index
	// of its redirection. scopeStart and scopeEnd are the outermost loop around the statement,
	// or the whole source when it is in a function's body; scopeEnd is 0 when it is in neither.
	source               string
	end                  int
	hdocs                [][2]int
	scopeStart, scopeEnd int
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
	// For refDeclares: exported is set when the declaration is an export, and bare when it
	// gives the variable no value (export T), so that the variable keeps the one it has.
	exported, bare bool
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

// ErrTooManyWords is Parse's error for a command line whose brace expansions produce more than
// maxBraceWords words.
var ErrTooManyWords = errors.New("shellcmd: brace expansions produce too many words to read")

// maxBraceWords bounds the words the brace expansions of one command line may produce. A few
// bytes of braces name any number of words (`{a,b}{a,b}{a,b}..` doubles with every pair, and
// `{1..999999999}` is sixteen bytes), and each is a word some rule then looks at. A thousand is
// more than a line that lists files or counts a loop comes to; a line past it is not read at
// all, which leaves it to a caller's reading of its text, rather than read without some of its
// words.
const maxBraceWords = 1024

// maxCutRetries bounds how many times Parse looks for the part of a cut command line that
// stands on its own. Each try drops the innermost construct left open, so a line needs as many
// as it has constructs open where it was cut: a quote inside a substitution inside a quote is
// three, and an `if` inside a loop is four. A line of nothing but opening parentheses would
// otherwise be parsed once for each.
const maxCutRetries = 32

// Parse parses command as bash and returns its statements in walk order: each statement, then the
// statements inside it (an if's body, a command substitution in its words). A statement that
// carries nothing -- no simple command and no redirection, such as the `a && b` around its two
// sides -- is left out.
//
// A command line that does not parse is an error. When the input merely ended early (an unclosed
// quote, here-document, substitution or compound command: a capture cut short), Parse also
// returns the commands of the part before the point where it became incomplete, parsed on its
// own; the last of them may be missing words that the cut removed. Any other parse error returns
// no commands, and so do a line cut inside more than maxCutRetries constructs and one whose
// brace expansions produce too many words (ErrTooManyWords). A caller that must not act on a
// partial command line ignores the commands whenever the error is not nil.
func Parse(command string) ([]Command, error) {
	return parse(command, new(int))
}

// parse is Parse with the count of the words brace expansions have produced so far, which the
// scripts of one command line share (WithScripts).
func parse(command string, braceWords *int) ([]Command, error) {
	file, err := parseBash(command)
	if err == nil {
		return walk(command, file, braceWords)
	}
	// The prefix can itself end inside an earlier construct (`a "$(b` cut again at `$(`); each
	// retry is strictly shorter.
	prefix, perr := command, err
	for range maxCutRetries {
		var incomplete syntax.ParseError
		if !errors.As(perr, &incomplete) || !incomplete.Incomplete || int(incomplete.Pos.Offset()) >= len(prefix) {
			return nil, err
		}
		prefix = prefix[:incomplete.Pos.Offset()]
		if file, perr = parseBash(prefix); perr == nil {
			commands, tooMany := walk(prefix, file, braceWords)
			if tooMany != nil {
				return nil, tooMany
			}
			return commands, err
		}
	}
	return nil, err
}

func parseBash(command string) (*syntax.File, error) {
	return syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
}

func walk(source string, file *syntax.File, braceWords *int) ([]Command, error) {
	p := parser{source: source, stages: map[*syntax.Stmt]stage{}, containers: map[*syntax.Stmt]bool{}, braceWords: braceWords}
	syntax.Walk(file, p.visit)
	p.directories()
	if p.tooManyWords {
		return nil, ErrTooManyWords
	}
	return p.commands, nil
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
	// scopes are the byte ranges of the loops, and the whole source once per function
	// declaration: a command inside one may run after anything in that range.
	scopes []substitution
	// isolated are the byte ranges that run in a shell of their own, whose cd ends with them:
	// subshells, substitutions, pipeline stages and background statements (directories).
	isolated []substitution
	// braceWords counts the words brace expansions have produced, and tooManyWords is set
	// once that passes maxBraceWords: the command line is then not read (ErrTooManyWords).
	braceWords   *int
	tooManyWords bool
}

// declaration is an assignment by export, declare, local, readonly or typeset, and where its
// statement ends. value is nil for an export that names a variable without assigning it.
type declaration struct {
	end      uint
	name     string
	value    []syntax.WordPart
	exported bool
}

func (p *parser) visit(node syntax.Node) bool {
	switch n := node.(type) {
	case *syntax.CmdSubst:
		p.nested = append(p.nested, substitution{n.Left.Offset(), n.Right.Offset(), p.substID(n)})
		p.isolated = append(p.isolated, substitution{start: n.Left.Offset(), end: n.Right.Offset()})
	case *syntax.ProcSubst:
		p.nested = append(p.nested, substitution{n.OpPos.Offset(), n.Rparen.Offset(), p.substID(n)})
		p.isolated = append(p.isolated, substitution{start: n.OpPos.Offset(), end: n.Rparen.Offset()})
	case *syntax.Subshell:
		p.subshells = append(p.subshells, substitution{n.Lparen.Offset(), n.Rparen.Offset(), int(n.Lparen.Offset()) + 1})
		p.isolated = append(p.isolated, substitution{start: n.Lparen.Offset(), end: n.Rparen.Offset()})
	case *syntax.ForClause:
		p.scopes = append(p.scopes, substitution{start: n.Pos().Offset(), end: n.End().Offset()})
	case *syntax.WhileClause:
		p.scopes = append(p.scopes, substitution{start: n.Pos().Offset(), end: n.End().Offset()})
	case *syntax.FuncDecl:
		// Whatever the line holds may be there by the time the function is called.
		p.scopes = append(p.scopes, substitution{start: 0, end: uint(len(p.source))})
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
	if stmt.Background {
		p.isolated = append(p.isolated, substitution{start: stmt.Pos().Offset(), end: stmt.End().Offset()})
	}
	if p.containers[stmt] {
		return
	}
	if isPipe(stmt) {
		p.pipelines++
		for i, s := range p.flatten(stmt, nil) {
			p.stages[s] = stage{pipeline: p.pipelines, index: i}
			p.isolated = append(p.isolated, substitution{start: s.Pos().Offset(), end: s.End().Offset()})
		}
		return
	}
	call, _ := stmt.Cmd.(*syntax.CallExpr)
	st, ok := p.stages[stmt]
	if ok && call == nil && stmt.Cmd != nil {
		p.compounds = append(p.compounds, compound{start: stmt.Pos().Offset(), end: stmt.End().Offset(), stage: st})
	}
	decl, isDecl := stmt.Cmd.(*syntax.DeclClause)
	if isDecl {
		exported := decl.Variant != nil && decl.Variant.Value == "export"
		for _, as := range decl.Args {
			switch {
			case as.Name == nil:
			case as.Value != nil:
				p.declared = append(p.declared, declaration{end: stmt.End().Offset(), name: as.Name.Value, value: as.Value.Parts, exported: exported})
			case exported && as.Naked:
				// export T: the variable keeps its value and the commands it runs get it.
				p.declared = append(p.declared, declaration{end: stmt.End().Offset(), name: as.Name.Value, exported: true})
			}
		}
	}
	if call == nil && !isDecl && len(stmt.Redirs) == 0 {
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
				c.execEnd = int(s.end)
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
	// A subshell's range nests in any around it, so the innermost one ends first.
	for _, s := range p.subshells {
		if offset > s.start && offset < s.end && (c.execEnd == 0 || int(s.end) < c.execEnd) {
			c.execEnd = int(s.end)
		}
	}
	// The statement carries the declarations that ended before it: not one it is inside.
	pending, carried := p.declared[:0], 0
	for _, d := range p.declared {
		if d.end > offset {
			pending = append(pending, d)
			continue
		}
		c.refs = append(c.refs, wordRef{place: refDeclares, index: carried, name: d.name, exported: d.exported, bare: d.value == nil})
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
	// export, declare, local, readonly and typeset are commands of the line too, with the
	// builtin as their name and each option and NAME=value as a word. What they assign is
	// carried to the statements after them (declared), not read from these words.
	if isDecl {
		c.Argv = p.declWords(decl)
	}
	end := offset
	if stmt.Cmd != nil {
		end = stmt.Cmd.End().Offset()
	}
	for i, r := range stmt.Redirs {
		c.Redirects = append(c.Redirects, p.redirect(r))
		if r.Word != nil {
			c.refs = p.refs(c.refs, r.Word.Parts, refRedirect, i)
			end = max(end, r.Word.End().Offset())
		}
		// What a here-document's body expands (unless its delimiter is quoted, which makes the
		// body literal) is written onto the command's standard input.
		if r.Hdoc != nil {
			c.refs = p.refs(c.refs, r.Hdoc.Parts, refRedirect, i)
			c.hdocs = append(c.hdocs, [2]int{int(r.Hdoc.Pos().Offset()), i})
		}
	}
	c.source, c.end = p.source, int(min(end, uint(len(p.source))))
	// A later statement of a loop's body runs before an earlier one the next time round, and
	// a function's body runs wherever it is called: see Spans.
	for _, s := range p.scopes {
		if offset >= s.start && offset < s.end && (c.scopeEnd == 0 || int(s.start) < c.scopeStart) {
			c.scopeStart, c.scopeEnd = int(s.start), int(min(s.end, uint(len(p.source))))
		}
	}
	c.resolve()
	p.commands = append(p.commands, c)
	if call == nil && stmt.Cmd != nil && len(stmt.Redirs) > 0 {
		p.redirected = append(p.redirected, redirected{start: offset, end: stmt.End().Offset(), index: len(p.commands) - 1})
	}
	// After a cd, a relative path is relative to somewhere this parser does not track.
	switch c.unwrapped.program {
	case "cd", "pushd", "popd":
		p.moved = true
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

// declWords returns a declaration as the words of a command: the builtin, then each option and
// each NAME, NAME=value or NAME+=value as one word. An array or an indexed assignment is a
// word whose value is unknown.
func (p *parser) declWords(decl *syntax.DeclClause) []Word {
	words := make([]Word, 0, 1+len(decl.Args))
	if decl.Variant != nil {
		words = append(words, Word{Text: decl.Variant.Value, Raw: decl.Variant.Value, Unquoted: decl.Variant.Value, Known: true})
	}
	for _, as := range decl.Args {
		switch {
		case as.Name == nil:
			if as.Value != nil {
				words = append(words, p.word(as.Value, as.Value))
			}
		case as.Index != nil || as.Array != nil:
			raw := p.raw(as)
			words = append(words, Word{Raw: raw, Unquoted: raw})
		default:
			w := Word{Raw: p.raw(as), Unquoted: as.Name.Value, Known: true}
			if !as.Naked {
				if as.Append {
					w.Unquoted += "+"
				}
				w.Unquoted += "="
				if as.Value != nil {
					value := p.word(as.Value, as.Value)
					w.Unquoted, w.Known = w.Unquoted+value.Unquoted, value.Known
				}
			}
			if w.Known {
				w.Text = w.Unquoted
			}
			words = append(words, w)
		}
	}
	return words
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
		if p.tooManyWords || !syntax.SplitBraces(split) {
			refs = p.refs(refs, word.Parts, refArg, len(words))
			words = append(words, p.word(word, word))
			continue
		}
		// Every word a brace expansion produces may hold what the unexpanded word does. The
		// words are taken one at a time, so an expansion past the bound is never built.
		for expanded, err := range expand.BracesSeq(nil, split) {
			if *p.braceWords++; err != nil || *p.braceWords > maxBraceWords {
				p.tooManyWords = true
				break
			}
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
	// A lone [ is the test command, not the start of a glob pattern: nothing closes it.
	if !w.Known && w.Unquoted == "[" && w.Raw == "[" {
		w.Known = true
	}
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
