package shellcmd

import (
	"slices"
	"strings"
)

// Via is how the content of a file reaches a command.
type Via string

const (
	// ViaDirect is a file the command opens itself: an operand, an option's value, a path its
	// inline script opens.
	ViaDirect Via = "direct"
	// ViaStdin is a file whose content arrives on the command's standard input: `< file`, a
	// here-string, or the output of the pipeline stage before it.
	ViaStdin Via = "stdin"
	// ViaArgument is a file whose content is written into one of the command's words: by a
	// command or process substitution, or by a variable that was assigned from one.
	ViaArgument Via = "argument"
)

// FileRead is a file whose content a command takes in, and how.
type FileRead struct {
	Path string
	Via  Via
	// Sent is set when the command is an interpreter whose inline script opens the file inside
	// the arguments of a call that sends them over the network. For any other command, whether
	// it sends what it takes in is for the caller to say from what the command is.
	Sent bool
}

// FileReads returns, for each command of a parsed command line, the files whose content it
// evidently takes in. The result is parallel to commands, or nil when no command takes a file in.
//
// A command takes a file in directly when its own words name it as input: what cat, head, base64
// and the other printers print, what grep, rg, awk, sed and jq search or filter, the sources of
// cp, install, scp and rsync, what tar and zip put into an archive, what curl, wget, httpie,
// `gh gist create` and socat send, and the files an inline python, node, ruby or php script opens
// by a literal path. The command is found behind the wrappers Unwrapped skips and behind timeout,
// nice, ionice, stdbuf, doas, torsocks and proxychains.
//
// What one command read follows the data to the commands that receive it:
//
//   - down a pipeline, from each stage to the next, unless the stage sends its output somewhere
//     else (`cat .env > out | nc ..`), the next stage takes its input from somewhere else
//     (`cat .env | nc .. < other`), or the stage only reports a count or a digest (wc, sha256sum);
//   - out of a command substitution or an input process substitution into the command whose word
//     holds it (`curl -d "$(cat .env)"`, `diff <(cat a) b`), or onto its standard input when the
//     word is a here-string or the target of `<`;
//   - through a variable assigned from a substitution, to the commands that use it later in the
//     line (`T=$(cat .env); curl -d "$T" ..`);
//   - through a file written earlier in the line, to the commands that read it later
//     (`cp .env /tmp/x; curl -T /tmp/x ..`, `cat .env > /tmp/x`, `mv`, `tee`).
//
// curl, wget and httpie take a substitution in as data only where they send it as the request
// body; a header, a URL or a credential option is not data. Any other command takes in every
// substitution in its words.
//
// A path is as written (Word.Unquoted), also where it depends on something this package does not
// evaluate: ~/.aws/credentials, $HOME/.netrc and .env* are reported as those words. That differs
// from FileEffects, which leaves such a path out. An effect is taken as the exact file a command
// wrote; a read is asked what it names, and ~/.ssh/id_rsa names a private key whatever the home
// directory is. Like an effect, a relative path is relative to the directory the command runs in,
// which after a cd (AfterCD) is not the one the command line started in. Paths are compared as
// written, so a file reached by two spellings is two files.
//
// Standard input (-), a device (/dev/stdin), a file on another host (scp host:path), a file a
// command edits in place (sed -i, which FileEffects reports) and a file a command names without
// taking it as input (a search pattern, an archive's exclusions, an identity file) are not reads.
// The body of a here-document is not followed, and neither is what a script does with a file once
// it has opened it, beyond opening it inside a call that sends it (FileRead.Sent).
func FileReads(commands []Command) [][]FileRead {
	f := flow{commands: commands}
	for i := range commands {
		if !f.done(i) {
			f.command(i)
		}
		f.next = i + 1
	}
	return f.reads
}

// flow follows what the commands of one command line read to the commands that receive it. Most
// command lines are a few commands that read no file and hold no substitution, and those cost no
// allocation: every slice and map here is made when it first has something to hold.
type flow struct {
	commands []Command
	reads    [][]FileRead
	// out are, for each command, the files whose content it may write to its standard output.
	out [][]string
	// The commands before next are worked out, and so are the ones marked in early: a command
	// inside a substitution is worked out when the word holding the substitution is.
	next  int
	early []bool
	// vars are the variables assigned from a substitution so far, and files the files written so
	// far, each with the files whose content went into it.
	vars  map[string][]string
	files map[string][]string
}

// digests report a count or a digest of their input and pass none of it on.
var digests = []string{"wc", "md5sum", "md5", "sha1sum", "sha224sum", "sha256sum", "sha384sum", "sha512sum", "shasum", "b2sum", "cksum", "sum"}

func (f *flow) done(i int) bool {
	return i < f.next || f.early != nil && f.early[i]
}

// written returns what command j writes to its standard output, working j out first when it has
// not been: with the variables and files as they are now, which is when a substitution runs.
func (f *flow) written(j int) []string {
	if !f.done(j) && j != f.next {
		if f.early == nil {
			f.early = make([]bool, len(f.commands))
		}
		f.early[j] = true
		f.command(j)
	}
	if f.out == nil {
		return nil
	}
	return f.out[j]
}

// command works out what command i takes in, what it passes on, and what it leaves behind.
func (f *flow) command(i int) {
	c := &f.commands[i]
	// What export, declare and the like assigned since the statement before this one.
	for _, ref := range c.refs {
		if ref.place == refDeclares {
			value, _ := f.wordValue(c, refDeclared, ref.index)
			f.assign(ref.name, value)
		}
	}
	name, args := c.run()
	var (
		files, sent []string
		data        dataWords
	)
	if language, ok := scriptLanguage(name); ok {
		files, sent = scriptReads(language, inlineScript(language, c, args))
	} else {
		files, data = c.inputs(name, args)
	}
	for _, path := range files {
		f.read(i, path, ViaDirect, slices.Contains(sent, path))
	}
	f.stdin(i)
	for _, ref := range c.refs {
		if ref.place == refArg && data.holds(ref.index) {
			for _, path := range f.value(ref) {
				f.read(i, path, ViaArgument, false)
			}
		}
	}
	taken := f.taken(i)
	if len(taken) > 0 && !slices.Contains(digests, name) && !c.outputElsewhere() {
		if f.out == nil {
			f.out = make([][]string, len(f.commands))
		}
		f.out[i] = taken
	}
	// A statement of assignments only (T=$(cat f)) sets variables of the line; an assignment
	// before a command is that command's environment.
	if len(c.Argv) == 0 {
		for k := range c.Env {
			value, _ := f.wordValue(c, refEnv, k)
			f.assign(c.Env[k].Name, value)
		}
	}
	f.writes(c, name, taken)
}

// read records that command i takes path in, and with it whatever was written into path earlier
// in the line.
func (f *flow) read(i int, path string, via Via, sent bool) {
	// Standard input, a device, and a process substitution, whose commands are followed instead.
	if path == "" || path == "-" || strings.HasPrefix(path, "/dev/") || strings.HasPrefix(path, "<(") {
		return
	}
	if f.reads == nil {
		f.reads = make([][]FileRead, len(f.commands))
	}
	for k := range f.reads[i] {
		if r := &f.reads[i][k]; r.Path == path && r.Via == via {
			r.Sent = r.Sent || sent
			return
		}
	}
	f.reads[i] = append(f.reads[i], FileRead{Path: path, Via: via, Sent: sent})
	for _, source := range f.files[path] {
		f.read(i, source, via, sent)
	}
}

// taken are the files command i takes in, by any way, each once.
func (f *flow) taken(i int) []string {
	if f.reads == nil {
		return nil
	}
	var paths []string
	for _, r := range f.reads[i] {
		if !slices.Contains(paths, r.Path) {
			paths = append(paths, r.Path)
		}
	}
	return paths
}

// stdin records what arrives on command i's standard input: what its last redirection of it
// names, or else what the stage before it in its pipeline writes.
func (f *flow) stdin(i int) {
	c := &f.commands[i]
	for k := len(c.Redirects) - 1; k >= 0; k-- {
		r := &c.Redirects[k]
		if r.Fd != "" && r.Fd != "0" {
			continue
		}
		switch r.Op {
		case "<", "<>":
			// The target is a file, or a process substitution whose commands write it.
			if value, substituted := f.wordValue(c, refRedirect, k); substituted {
				for _, path := range value {
					f.read(i, path, ViaStdin, false)
				}
			} else {
				f.read(i, r.Target.Unquoted, ViaStdin, false)
			}
			return
		case "<<<":
			value, _ := f.wordValue(c, refRedirect, k)
			for _, path := range value {
				f.read(i, path, ViaStdin, false)
			}
			return
		case "<<", "<<-", "<&":
			return
		}
	}
	if c.Stage == 0 {
		return
	}
	for j := range f.commands {
		if before := &f.commands[j]; before.Pipeline == c.Pipeline && before.Stage == c.Stage-1 {
			for _, path := range f.written(j) {
				f.read(i, path, ViaStdin, false)
			}
		}
	}
}

// outputElsewhere reports whether the command's standard output is redirected, so that it reaches
// neither the next stage of its pipeline nor the substitution it is in.
func (c *Command) outputElsewhere() bool {
	for _, r := range c.Redirects {
		switch r.Op {
		case "&>", "&>>":
			return true
		case ">", ">>", ">|", ">&":
			if r.Fd == "" || r.Fd == "1" {
				return true
			}
		}
	}
	return false
}

// wordValue returns the files whose content the substitutions and variables in one word of c
// carry, and whether the word holds a substitution at all.
func (f *flow) wordValue(c *Command, place refPlace, index int) (value []string, substituted bool) {
	for _, ref := range c.refs {
		if ref.place != place || ref.index != index {
			continue
		}
		substituted = substituted || ref.subst != 0
		for _, path := range f.value(ref) {
			if !slices.Contains(value, path) {
				value = append(value, path)
			}
		}
	}
	return value, substituted
}

// value returns the files whose content a variable holds or a substitution writes: for a
// substitution, what the last stage of each pipeline directly inside it writes.
func (f *flow) value(ref wordRef) []string {
	if ref.subst == 0 {
		return f.vars[ref.name]
	}
	var value []string
	for j := range f.commands {
		if int(f.commands[j].within) != ref.subst {
			continue
		}
		written := f.written(j)
		if !f.lastStage(j) {
			continue
		}
		for _, path := range written {
			if !slices.Contains(value, path) {
				value = append(value, path)
			}
		}
	}
	return value
}

// lastStage reports whether no later stage of command j's pipeline takes its output.
func (f *flow) lastStage(j int) bool {
	c := &f.commands[j]
	for k := range f.commands {
		if later := &f.commands[k]; later.Pipeline == c.Pipeline && later.Stage > c.Stage {
			return false
		}
	}
	return true
}

// assign records the files whose content a variable holds, as a substitution or another variable
// gave it. Assigning anything else clears it.
func (f *flow) assign(name string, value []string) {
	switch {
	case len(value) == 0:
		delete(f.vars, name)
	case f.vars == nil:
		f.vars = map[string][]string{name: value}
	default:
		f.vars[name] = value
	}
}

// writes records the files c writes with what it took in: the target of a redirection of its
// standard output, and the files its own words name (cp's and install's destination, tee's files,
// mv's destination, which holds what its sources held). Writing nothing that was read into a file
// clears it, and so does deleting or truncating it.
func (f *flow) writes(c *Command, name string, taken []string) {
	// With nothing taken in and no file holding anything, only a move leaves something to follow.
	if len(taken) == 0 && f.files == nil && name != "mv" && name != "git" {
		return
	}
	for _, r := range c.Redirects {
		if path, op, ok := redirectTarget(r); ok && (r.Fd == "" || r.Fd == "1" || r.Op == "&>" || r.Op == "&>>") {
			f.write(path, taken, op == OpAppend)
		}
	}
	effects := c.commandTargets()
	if len(effects) == 0 {
		return
	}
	moved := taken
	for _, e := range effects {
		if e.Op == OpMoveFrom {
			if !slices.Contains(moved, e.Path) {
				moved = append(slices.Clip(moved), e.Path)
			}
			for _, source := range f.files[e.Path] {
				if !slices.Contains(moved, source) {
					moved = append(slices.Clip(moved), source)
				}
			}
		}
	}
	for _, e := range effects {
		switch e.Op {
		case OpWrite:
			f.write(e.Path, taken, false)
		case OpAppend:
			f.write(e.Path, taken, true)
		case OpMoveTo:
			f.write(e.Path, moved, false)
		case OpMoveFrom, OpDelete, OpTruncate:
			delete(f.files, e.Path)
		}
	}
}

func (f *flow) write(path string, content []string, appends bool) {
	switch {
	case path == "":
	case len(content) == 0:
		if !appends {
			delete(f.files, path)
		}
	case f.files == nil:
		f.files = map[string][]string{path: content}
	case appends:
		held := f.files[path]
		for _, source := range content {
			if !slices.Contains(held, source) {
				held = append(slices.Clip(held), source)
			}
		}
		f.files[path] = held
	default:
		f.files[path] = content
	}
}
