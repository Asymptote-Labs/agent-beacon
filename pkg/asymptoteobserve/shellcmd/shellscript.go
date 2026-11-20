package shellcmd

import (
	"errors"
	"slices"
	"strings"
)

// Script returns the script a shell in the command runs, when it can be read. With -c it is
// the first operand after an option cluster containing c, of the first shell word in the
// command's words, so the shell may follow other words (`docker exec c sh -c ..`). Without -c
// and without a script file, it is the here-document or here-string on the command's standard
// input, when the shell is the command it runs after its wrappers (`sudo bash <<'EOF'`,
// `sh -s -- arg <<EOF`, `bash <<< 'cmd'`). ok is false when there is no such shell, the shell
// runs a file or what a pipe sends it, or the script is not a known word. A PowerShell in the
// command runs the script of its -Command option (powershellScript). WithScripts also reads
// what a pipe sends a shell when the stage before it prints it literally.
func (c Command) Script() (script string, ok bool) {
	script, _, _, ok = c.script()
	return script, ok
}

// script is Script, with zero, the index in Argv of the word that is the script's $0 (the one
// after the script with -c, and the one before the first operand with -s, so that each word
// after it is $1, $2 and so on; -1 where the operands are not the script's, PowerShell's), and
// whether the script is the command's standard input (stdin).
func (c Command) script() (script string, zero int, stdin, ok bool) {
	script, zero, found, onStdin := c.scriptScan()
	switch {
	case found:
		return script, zero, false, true
	case !onStdin:
		return "", -1, false, false
	}
	script, ok = c.HereInput()
	return script, zero, true, ok
}

// scriptScan reads the words after the first word of the command that names a shell
// (shellWord). It returns the -c script when there is one and it is a known word (found), and
// otherwise whether the shell is the command the statement runs and reads its script from its
// standard input (stdin): it is given no -c and no script file. zero is as script says.
func (c Command) scriptScan() (script string, zero int, found, stdin bool) {
	words := c.Argv
	start := c.shellWord()
	if start < 0 {
		script, found := c.powershellScript()
		return script, -1, found, false
	}
	// command is set by -c, and stdin by -s: the operands are then the script's arguments.
	command := false
	zero = len(words) - 1
scan:
	for i := start + 1; ; i++ {
		if i >= len(words) {
			stdin = !command
			break
		}
		w := words[i]
		if !w.Known {
			if stdin && !command {
				zero = i - 1
				break // with -s, the script's first argument
			}
			return "", -1, false, false
		}
		t := w.Text
		switch {
		case t == "--" || t == "-": // a lone - ends the options as -- does
			if command && i+1 < len(words) && words[i+1].Known {
				return words[i+1].Text, i + 2, true, false
			}
			stdin = !command && (stdin || i+1 == len(words))
			zero = i
			break scan
		case t == "--rcfile" || t == "--init-file":
			i++ // takes a file
		case strings.HasPrefix(t, "--"):
		case len(t) > 1 && (t[0] == '-' || t[0] == '+'):
			if t[0] == '-' && strings.ContainsRune(t[1:], 'c') {
				command = true
			}
			if t[0] == '-' && strings.ContainsRune(t[1:], 's') {
				stdin = true
			}
			if last := t[len(t)-1]; last == 'o' || last == 'O' {
				i++ // -o option / -O shopt_option takes a name
			}
		default:
			if command {
				return t, i + 1, true, false
			}
			zero = i - 1
			break scan // with -s an argument of the script, without it a script file
		}
	}
	if !stdin {
		return "", -1, false, false
	}
	if c.unwrapped.how != ran || c.unwrapped.at != start {
		return "", -1, false, false
	}
	return "", zero, false, true
}

// pipedScript returns the script a pipe sends the shell at commands[i], when it is told by
// the command line: the shell reads its script from its standard input, that is the pipe and
// not a redirection, and every command of the stage before it prints its own words (printed).
// `echo 'rm -rf /' | sh`, `printf '%s\n' 'id' | sudo bash` and `cat <<'EOF' | sh` are read;
// `curl .. | sh` and `echo "$CMD" | sh` are not. pipes indexes commands by pipeline.
func pipedScript(commands []Command, i int, pipes *pipelineIndex) (string, bool) {
	shell := &commands[i]
	if shell.Stage == 0 {
		return "", false
	}
	if _, _, found, stdin := shell.scriptScan(); found || !stdin {
		return "", false
	}
	if r, _ := shell.stdinRedirect(); r != nil {
		return "", false // its standard input is not the pipe
	}
	var parts []string
	for _, k := range pipes.at(i, shell.Stage-1) {
		text, ok := commands[k].printed()
		if !ok {
			return "", false
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

// printed returns what the command writes to its standard output, when its own words say it:
// the words of echo, a printf format that takes only strings with its arguments, and the
// here-document or here-string cat is given with no file. ok is false for any other command,
// for one with a word whose value is unknown, and for one whose output is redirected.
func (c Command) printed() (string, bool) {
	if c.outputElsewhere() {
		return "", false
	}
	name, args := c.program()
	for _, w := range args {
		if !w.Known {
			return "", false
		}
	}
	switch name {
	case "echo":
		escapes := false
		for len(args) > 0 && len(args[0].Text) > 1 && args[0].Text[0] == '-' && strings.Trim(args[0].Text[1:], "neE") == "" {
			escapes = escapes || strings.ContainsRune(args[0].Text, 'e')
			args = args[1:]
		}
		words := make([]string, len(args))
		for k, w := range args {
			words[k] = w.Text
		}
		text := strings.Join(words, " ")
		if escapes {
			text = backslashEscapes.Replace(text)
		}
		return text, true
	case "printf":
		if len(args) > 0 && args[0].Text == "--" {
			args = args[1:]
		}
		if len(args) == 0 || strings.HasPrefix(args[0].Text, "-") {
			return "", false // printf -v assigns a variable
		}
		return printfOutput(args[0].Text, args[1:])
	case "cat":
		if len(args) == 0 || len(args) == 1 && args[0].Text == "-" {
			return c.HereInput()
		}
	}
	return "", false
}

// backslashEscapes are the backslash escapes a printf format and echo -e turn into
// characters, and an inline script's string literal holds, as far as a command line needs
// them.
var backslashEscapes = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\\`, `\`, `\"`, `"`, `\'`, `'`)

// printfOutput returns what printf prints for a format whose only directives are %s, %b and
// %%: the format once when it has none, and otherwise once for each group of arguments, as
// printf reuses its format. ok is false for any other directive. Output that the reuse makes
// longer than maxScriptBytes is not built to its end: what is returned is then longer than any
// script a line is read with, which is decided where every script is (expander.add).
func printfOutput(format string, args []Word) (string, bool) {
	var literals []string // the text before each directive, and after the last
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			b.WriteByte(format[i])
			continue
		}
		if i+1 == len(format) {
			return "", false
		}
		i++
		switch format[i] {
		case '%':
			b.WriteByte('%')
		case 's', 'b':
			literals = append(literals, backslashEscapes.Replace(b.String()))
			b.Reset()
		default:
			return "", false
		}
	}
	tail := backslashEscapes.Replace(b.String())
	if len(literals) == 0 {
		return tail, true
	}
	var out strings.Builder
	for next := 0; ; {
		for _, literal := range literals {
			out.WriteString(literal)
			if next < len(args) {
				out.WriteString(args[next].Text)
				next++
			}
		}
		out.WriteString(tail)
		if next >= len(args) {
			return out.String(), true
		}
		if out.Len() > maxScriptBytes {
			return out.String(), true
		}
	}
}

// shellWord is the index of the first word of the command that names a shell, by any path
// (commandBase), or -1.
func (c *Command) shellWord() int {
	return slices.IndexFunc(c.Argv, func(w Word) bool { return isShell(programOf(commandBase(w))) })
}

// isShell reports whether a program is a shell whose script WithScripts reads.
func isShell(program string) bool {
	switch program {
	case "sh", "bash", "zsh", "dash", "ksh":
		return true
	}
	return false
}

// withScripts returns commands with the commands each one hands on put after it: those of a
// shell's script (Command.Script) and of the command line a carrier runs (Command.Carried:
// xargs, find -exec, ssh, docker exec, eval and the like), InScript set, and so on for the
// ones inside those, depth levels deep. The text is read as bash whatever reads it. Its
// pipelines are numbered after every pipeline before them, so no two commands of the result
// share a Pipeline unless they are stages of one pipeline.
//
// The result is one command line to FileReads and SocketRedirects, which follow the data
// across the boundary: what the shell or carrier gets on its standard input reaches the
// commands it runs, what they write is what it writes, and they see its environment (its
// NAME=value prefix, and what the command line exported).
//
// ok is false when the result would pass a bound of a line (pastBound), or the command lines
// handed on do: their brace expansions produce more than maxBraceWords words together, or
// their text comes to more than maxScriptBytes. The line is then not read (Read).
func withScripts(commands []Command, depth int) (_ []Command, ok bool) {
	if depth <= 0 || !slices.ContainsFunc(commands, Command.mayHandOn) {
		words := 0
		for i := range commands {
			words += len(commands[i].Argv)
		}
		return commands, !pastBound(len(commands), words)
	}
	x := expander{out: make([]Command, 0, len(commands)+4), scriptBytes: maxScriptBytes}
	x.add(commands, 0, depth)
	return x.out, !x.past
}

// mayHandOn reports whether the command is one WithScripts looks into for a command line it
// hands on: it names a shell or PowerShell among its words, its program is a carrier (Carried)
// or an interpreter (scriptLanguage), or a wrapper runs its command somewhere else. Most commands
// are none of these, and a command line none of whose commands is costs WithScripts nothing.
func (c Command) mayHandOn() bool {
	if c.unwrapped.how == moved || carriers[c.unwrapped.program] != nil {
		return true
	}
	if _, ok := scriptLanguage(c.unwrapped.program); ok {
		return true
	}
	return c.shellWord() >= 0 || c.powerShellWord() >= 0
}

// powerShellWord is the index of the first word of the command that names PowerShell
// (powershell, pwsh) by any path and in any case, or -1.
func (c *Command) powerShellWord() int {
	return slices.IndexFunc(c.Argv, func(w Word) bool {
		program := strings.ToLower(programOf(commandBase(w)))
		return program == "powershell" || program == "pwsh"
	})
}

// powershellScript returns the script a PowerShell in the command runs from its -Command option:
// the words after it, joined with spaces as PowerShell joins them. PowerShell is not bash, but a
// command and its words read the same in both (Get-StoredCredential -Target x, az account
// get-access-token), and Unwrapped reads past an assignment's `$name =`. ok is false without
// -Command, with -File or -EncodedCommand, or when a word is not known. An option is matched as
// PowerShell matches it, in any case and by the prefixes it takes.
func (c Command) powershellScript() (script string, ok bool) {
	words := c.Argv
	start := c.powerShellWord()
	if start < 0 {
		return "", false
	}
	for i := start + 1; i < len(words); i++ {
		if !words[i].Known {
			return "", false
		}
		option, ok := strings.CutPrefix(strings.ToLower(words[i].Text), "-")
		if !ok {
			return "", false // a script file
		}
		switch {
		case option == "c" || len(option) >= 3 && strings.HasPrefix("command", option):
			parts := make([]string, 0, len(words)-i-1)
			for _, w := range words[i+1:] {
				if !w.Known {
					return "", false
				}
				parts = append(parts, w.Text)
			}
			script = strings.Join(parts, " ")
			return script, script != ""
		case option == "f" || strings.HasPrefix(option, "fi") || option == "e" || option == "ec" || strings.HasPrefix(option, "enc"):
			return "", false // -File, -EncodedCommand
		case slices.Contains(powershellValueOptions, option):
			i++
		}
	}
	return "", false
}

// powershellValueOptions are the PowerShell options, as they are commonly written, that take the
// next word as their value.
var powershellValueOptions = []string{
	"ep", "ex", "exec", "executionpolicy", "w", "windowstyle", "wd", "workingdirectory",
	"if", "inputformat", "of", "outputformat", "config", "configurationname", "settingsfile", "v", "version",
}

// maxScriptBytes bounds the script text withScripts reads for one command line. A script is
// part of the line that holds it, so a line's scripts come to a few times the line at most,
// one for each level; a printf that reuses its format for a thousand arguments is the
// exception, and prints megabytes from a line of four kilobytes. Past the bound the line is
// not read, as past every bound of a line (see MaxCommands).
const maxScriptBytes = 16 << 10

type expander struct {
	out []Command
	// pipelines and substs are the highest of each number given out so far: a script is parsed
	// on its own and its numbers start again, so they are moved past these.
	pipelines int
	substs    int32
	// scriptBytes is what is left of maxScriptBytes, and braceWords the words the scripts'
	// brace expansions have produced, bounded for all of them together as a line's own are.
	// words are the words of out. past is set once a bound is passed; nothing more is read.
	scriptBytes int
	braceWords  int
	words       int
	past        bool
}

// add appends commands, the commands of one parse, as the script of the shell at index
// shell-1 of the result, or as the command line itself when shell is 0.
func (x *expander) add(commands []Command, shell int32, depth int) {
	pipelines, substs := x.pipelines, x.substs
	for i := range commands {
		c := &commands[i]
		x.pipelines = max(x.pipelines, pipelines+c.Pipeline)
		x.substs = max(x.substs, substs+c.within)
		for _, ref := range c.refs {
			x.substs = max(x.substs, substs+int32(ref.subst))
		}
	}
	// outer names a compound of the same parse by its index there, which a script spliced in
	// before it moves: at holds where each of these commands landed, 1-based like outer.
	var at []int32
	if slices.ContainsFunc(commands, func(c Command) bool { return c.outer != 0 }) {
		at = make([]int32, len(commands))
	}
	// The pipeline index of this parse, made when a command at a later stage first asks.
	var pipes *pipelineIndex
	for idx, c := range commands {
		if c.outer != 0 {
			c.outer = at[c.outer-1]
		}
		c.Pipeline += pipelines
		if shell != 0 {
			c.InScript, c.shell = true, shell
			// Before a cd of its own, a script runs where the command that hands it on does,
			// unless that runs it elsewhere.
			if !c.dirSet {
				if parent := &x.out[shell-1]; parent.inheritsDir() {
					c.Dir, c.dirSet = parent.Dir, parent.dirSet
				} else {
					c.dirSet = true
				}
			}
			if c.within != 0 {
				c.within += substs
			}
			if slices.ContainsFunc(c.refs, func(ref wordRef) bool { return ref.subst != 0 }) {
				c.refs = slices.Clone(c.refs)
				for k := range c.refs {
					if c.refs[k].subst != 0 {
						c.refs[k].subst += int(substs)
					}
				}
			}
		}
		x.out = append(x.out, c)
		if at != nil {
			at[idx] = int32(len(x.out))
		}
		if x.words += len(c.Argv); pastBound(len(x.out), x.words) {
			x.past = true
		}
		if x.past {
			return
		}
		if depth <= 0 {
			continue
		}
		if c.Stage > 0 && pipes == nil {
			pipes = indexPipelines(commands)
		}
		line, handed, ok := handOn(commands, idx, pipes)
		if !ok {
			continue
		}
		if len(line) > x.scriptBytes {
			x.past = true
			return
		}
		x.scriptBytes -= len(line)
		sub, err := parse(line, &x.braceWords)
		if errors.Is(err, ErrTooManyWords) {
			x.past = true
			return
		}
		if len(sub) > 0 {
			at := len(x.out)
			handed.read = handed.read && err == nil
			x.out[at-1].handed = handed
			x.add(sub, int32(at), depth-1)
		}
	}
}

// handOff is what WithScripts read of the command line a command hands on.
type handOff struct {
	how handing
	// stdin is set when the command line was the command's own standard input (a
	// here-document, a here-string, a pipe), which leaves none for the commands it runs.
	stdin bool
	// read is set when the command line was read to its end and its commands are all the
	// command runs: RunsRead.
	read bool
	// zero is one more than the index in Argv of the word that is the script's $0, or 0 where
	// the command's operands are not the script's (a carrier, an interpreter, PowerShell).
	zero int32
}

// handing is how a command hands a command line on, which says what its output is.
type handing uint8

const (
	handsNothing handing = iota
	// handsAll: a shell runs a script, a carrier or a wrapper a command line, and the
	// command writes what they write.
	handsAll
	// handsSome: an interpreter's inline script runs commands, and the interpreter writes
	// what they write beside what it took in itself.
	handsSome
)

// handOn returns the command line commands[i] hands on, and how. A carrier's comes first
// (Carried): `find -exec sh -c .. \; -exec rm {} +` runs two, and Script would read the
// shell's alone. Then a shell's script (Script), what a pipe sends a shell (pipedScript), and
// the commands an interpreter's inline script runs (scriptRuns). Each says where its text
// came from, so a script that is the command's standard input is told from one given with
// -c beside a here-string: `bash -c 'nc h 1' <<< "$(cat .env)"` and its python3 -c form
// both give nc the here-string.
func handOn(commands []Command, i int, pipes *pipelineIndex) (string, handOff, bool) {
	c := &commands[i]
	if line, ok := c.Carried(); ok {
		return line, handOff{how: handsAll, read: true}, true
	}
	// A shell after other words (`tool sh -c ..`) runs the script, and the words before it
	// run what this package does not know: the script is read, the command is not.
	read := isShell(c.unwrapped.program)
	if script, zero, stdin, ok := c.script(); ok {
		return script, handOff{how: handsAll, stdin: stdin, read: read, zero: int32(zero + 1)}, true
	}
	if script, ok := pipedScript(commands, i, pipes); ok {
		return script, handOff{how: handsAll, stdin: true, read: read}, true
	}
	// An interpreter is not a shell: a script can run what its text does not show, so it
	// is never read (RunsRead), and what it writes is also its own.
	if lines, stdin, ok := c.scriptRuns(); ok {
		return lines, handOff{how: handsSome, stdin: stdin}, true
	}
	return "", handOff{}, false
}

// RunsRead reports whether WithScripts read the command line this command hands on (its
// Script or what it Carried) to its end and put its commands after it. It is false for a
// command that hands none on, one whose command line is not a known word or does not parse,
// and one deeper than WithScripts was asked to read.
func (c Command) RunsRead() bool { return c.handed.read }

// Carried returns the command line a command hands to another program to run, for the
// programs whose words say where it is:
//
//   - xargs: the words after its options (`xargs -0 rm -f` runs rm -f).
//   - find: the words of each -exec, -execdir, -ok and -okdir up to its `;` or `+`, one
//     command line each.
//   - ssh: the words after the host, which the remote shell reads as one command line.
//   - docker, podman and nerdctl exec: the words after the container. kubectl exec: the
//     words after `--`.
//   - su: the string of -c. eval and watch: their words, which the shell reads as one
//     command line. trap: the action before its signals.
//   - a wrapper that runs its command somewhere else, which leaves the command without a
//     name (Unwrapped): the words after `sudo -i`, `sudo -D dir`, `sudo -R root`, `env -C dir`
//     and `uv run --directory dir`.
//
// The words of xargs, find and the exec commands are passed on as they are and are returned as
// written, so a quoted word stays one word; the others are returned as the shell passes them
// on, because a shell reads them again. ok is false when the command is none of these, names
// no command (`ssh host`, `xargs` alone, a find without -exec), or a find clause has no end.
//
// The commands run where the carrier puts them: on another host, in a container, once per
// file. Their words are still what runs.
func (c Command) Carried() (script string, ok bool) {
	if c.unwrapped.how == moved {
		// A wrapper runs it somewhere else: Unwrapped gives it no name, because its paths are
		// not this directory's, and its words are still what runs.
		return joinWords(c.Argv[c.unwrapped.at:], true)
	}
	program, args := c.program()
	if carry := carriers[program]; carry != nil {
		return carry(args)
	}
	return "", false
}

// carriers read the command line each carrier hands on from the words after its name: see
// Carried. They are the programs WithScripts looks into for one, with the shells and the
// interpreters.
var carriers = map[string]func(args []Word) (string, bool){
	"xargs": func(args []Word) (string, bool) {
		return joinWords(afterOptions(args, xargsOptions), true)
	},
	"find": findExec,
	"ssh": func(args []Word) (string, bool) {
		if rest := afterOptions(args, sshOptions); len(rest) >= 2 {
			return joinWords(rest[1:], false)
		}
		return "", false
	},
	"docker":  containerExec,
	"podman":  containerExec,
	"nerdctl": containerExec,
	"kubectl": func(args []Word) (string, bool) {
		if isSub(args, "exec") {
			for i, w := range args {
				if w.Known && w.Text == "--" {
					return joinWords(args[i+1:], true)
				}
			}
		}
		return "", false
	},
	"su": func(args []Word) (string, bool) {
		for i, w := range args {
			switch {
			case !w.Known:
			case (w.Text == "-c" || w.Text == "--command") && i+1 < len(args) && args[i+1].Known:
				return args[i+1].Text, args[i+1].Text != ""
			case strings.HasPrefix(w.Text, "--command="):
				return w.Text[len("--command="):], len(w.Text) > len("--command=")
			}
		}
		return "", false
	},
	"eval": func(args []Word) (string, bool) { return joinWords(args, false) },
	"watch": func(args []Word) (string, bool) {
		return joinWords(afterOptions(args, watchOptions), false)
	},
	"trap": func(args []Word) (string, bool) {
		// trap action signal..: `-` and "" reset a signal and run nothing; -l and -p list.
		if len(args) >= 2 && args[0].Known && args[0].Text != "" && !strings.HasPrefix(args[0].Text, "-") {
			return args[0].Text, true
		}
		return "", false
	},
}

// containerExec reads `docker exec`, `podman exec` and `nerdctl exec`: the words after the
// container.
func containerExec(args []Word) (string, bool) {
	if !isSub(args, "exec") {
		return "", false
	}
	if rest := afterOptions(args[1:], dockerExecOptions); len(rest) >= 2 {
		return joinWords(rest[1:], true)
	}
	return "", false
}

// The options of the carriers that take a value.
var (
	xargsOptions = optionSpec{short: "IndPLEsaJRS", long: []string{
		"--max-args", "--max-procs", "--delimiter", "--arg-file", "--max-lines", "--max-chars", "--eof", "--process-slot-var",
	}}
	sshOptions        = optionSpec{short: "BbcDEeFIiJLlmOopQRSWw"}
	dockerExecOptions = optionSpec{short: "euw", long: []string{"--env", "--env-file", "--user", "--workdir", "--detach-keys"}}
	watchOptions      = optionSpec{short: "nq", long: []string{"--interval", "--equexit"}}
)

// joinWords returns words as one command line: as written when raw, else as the shell passes
// them on, with a word it does not evaluate as written without its quotes.
func joinWords(words []Word, raw bool) (string, bool) {
	if len(words) == 0 {
		return "", false
	}
	var b strings.Builder
	for i, w := range words {
		if i > 0 {
			b.WriteByte(' ')
		}
		switch {
		case raw:
			b.WriteString(w.Raw)
		case w.Known:
			b.WriteString(w.Text)
		default:
			b.WriteString(w.Unquoted)
		}
	}
	return b.String(), true
}

// findExec returns the command lines of a find's -exec, -execdir, -ok and -okdir clauses, one
// per line.
func findExec(args []Word) (string, bool) {
	var lines []string
	for i := 0; i < len(args); i++ {
		if w := args[i]; !w.Known || (w.Text != "-exec" && w.Text != "-execdir" && w.Text != "-ok" && w.Text != "-okdir") {
			continue
		}
		end := i + 1
		for end < len(args) && !(args[end].Known && (args[end].Text == ";" || args[end].Text == "+")) {
			end++
		}
		if end == len(args) {
			return "", false
		}
		line, ok := joinWords(args[i+1:end], true)
		if !ok {
			return "", false
		}
		lines = append(lines, line)
		i = end
	}
	if len(lines) == 0 {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}
