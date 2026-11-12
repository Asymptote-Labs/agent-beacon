package shellcmd

import (
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
	script, _, ok = c.script()
	return script, ok
}

// script is Script, with zero, the index in Argv of the word that is the script's $0: the one
// after the script with -c, and the one before the first operand with -s, so that each word after
// it is $1, $2 and so on. zero is -1 where the operands are not the script's (PowerShell).
func (c Command) script() (script string, zero int, ok bool) {
	script, zero, found, stdin := c.scriptScan()
	switch {
	case found:
		return script, zero, true
	case !stdin:
		return "", -1, false
	}
	script, ok = c.HereInput()
	return script, zero, ok
}

// scriptScan reads the words after the first shell word of the command. It returns the -c
// script when there is one and it is a known word (found), and otherwise whether the shell is
// the command the statement runs and reads its script from its standard input (stdin): it is
// given no -c and no script file. zero is as script says.
func (c Command) scriptScan() (script string, zero int, found, stdin bool) {
	words := c.Argv
	start := -1
	for i, w := range words {
		if w.Known && isShell(w.Text) {
			start = i
			break
		}
	}
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
	if unwrapped, ok := c.Unwrapped(); !ok || len(c.Argv)-len(unwrapped) != start {
		return "", -1, false, false
	}
	return "", zero, false, true
}

// pipedScript returns the script a pipe sends the shell at commands[i], when it is told by
// the command line: the shell reads its script from its standard input, that is the pipe and
// not a redirection, and every command of the stage before it prints its own words (printed).
// `echo 'rm -rf /' | sh`, `printf '%s\n' 'id' | sudo bash` and `cat <<'EOF' | sh` are read;
// `curl .. | sh` and `echo "$CMD" | sh` are not.
func pipedScript(commands []Command, i int) (string, bool) {
	shell := &commands[i]
	if shell.Stage == 0 {
		return "", false
	}
	if _, _, found, stdin := shell.scriptScan(); found || !stdin {
		return "", false
	}
	for _, r := range shell.Redirects {
		if (r.Fd == "" || r.Fd == "0") && strings.HasPrefix(r.Op, "<") {
			return "", false // its standard input is not the pipe
		}
	}
	var parts []string
	for k := range commands {
		d := &commands[k]
		if k == i || d.Pipeline != shell.Pipeline || d.Stage != shell.Stage-1 {
			continue
		}
		text, ok := d.printed()
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
	name, args := c.run()
	for _, r := range c.Redirects {
		if (r.Fd == "" || r.Fd == "1") && (strings.HasPrefix(r.Op, ">") || strings.HasPrefix(r.Op, "&>")) {
			return "", false
		}
	}
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
			text = printfEscapes.Replace(text)
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

// printfEscapes are the backslash escapes a printf format and echo -e turn into characters,
// as far as a command line needs them.
var printfEscapes = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\\`, `\`, `\"`, `"`, `\'`, `'`)

// printfOutput returns what printf prints for a format whose only directives are %s, %b and
// %%: the format once when it has none, and otherwise once for each group of arguments, as
// printf reuses its format. ok is false for any other directive.
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
			literals = append(literals, printfEscapes.Replace(b.String()))
			b.Reset()
		default:
			return "", false
		}
	}
	tail := printfEscapes.Replace(b.String())
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
	}
}

func isShell(word string) bool {
	switch ProgramName(word) {
	case "sh", "bash", "zsh", "dash", "ksh":
		return true
	}
	return false
}

// powershellScript returns the script a PowerShell (powershell, pwsh) in the command runs from
// its -Command option: the words after it, joined with spaces as PowerShell joins them.
// PowerShell is not bash, but a command and its words read the same in both
// (Get-StoredCredential -Target x, az account get-access-token), and Unwrapped reads past an
// assignment's `$name =`. ok is false without -Command, with -File or -EncodedCommand, or when a
// word is not known. An option is matched as PowerShell matches it, in any case and by the
// prefixes it takes.
func (c Command) powershellScript() (script string, ok bool) {
	words := c.Argv
	start := slices.IndexFunc(words, func(w Word) bool { return w.Known && isPowerShell(w.Text) })
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

// isRunner is a cheap test for a word that may name a program that is handed a command line: a
// shell, or one of the programs Carried reads.
func isRunner(word string) bool {
	switch ProgramName(word) {
	case "sh", "bash", "zsh", "dash", "ksh",
		"xargs", "find", "ssh", "docker", "podman", "nerdctl", "kubectl", "su", "eval", "watch", "trap":
		return true
	}
	return isPowerShell(word)
}

// WithScripts returns commands with the commands each one hands on put after it: those of a
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
func WithScripts(commands []Command, depth int) []Command {
	if depth <= 0 || !slices.ContainsFunc(commands, hasRunnerWord) {
		return commands
	}
	x := expander{out: make([]Command, 0, len(commands)+4)}
	x.add(commands, 0, depth)
	return x.out
}

// hasRunnerWord is a cheap test for a command that may hand a command line on: most do not.
func hasRunnerWord(c Command) bool {
	return slices.ContainsFunc(c.Argv, func(w Word) bool { return w.Known && isRunner(w.Text) })
}

func isPowerShell(word string) bool {
	name := strings.ToLower(ProgramName(word))
	return name == "powershell" || name == "pwsh"
}

type expander struct {
	out []Command
	// pipelines and substs are the highest of each number given out so far: a script is parsed
	// on its own and its numbers start again, so they are moved past these.
	pipelines int
	substs    int32
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
	for idx, c := range commands {
		if c.outer != 0 {
			c.outer = at[c.outer-1]
		}
		c.Pipeline += pipelines
		if shell != 0 {
			c.InScript, c.shell = true, shell
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
		if depth <= 0 {
			continue
		}
		// A carrier's command line comes first: `find -exec sh -c .. \; -exec rm {} +` runs
		// two, and Script would read the shell's alone. Its operands are not that line's $1.
		script, ok := c.Carried()
		onStdin, zero := false, -1
		if !ok {
			if script, zero, ok = c.script(); ok {
				// A script read from the shell's standard input leaves none for its commands.
				here, isHere := c.HereInput()
				onStdin = isHere && here == script
			} else if script, ok = pipedScript(commands, idx); ok {
				onStdin = true
			}
		}
		if !ok {
			continue
		}
		if sub, err := Parse(script); len(sub) > 0 {
			at := len(x.out)
			x.out[at-1].scripted, x.out[at-1].scriptOnStdin = true, onStdin
			x.out[at-1].scriptZero = int32(zero + 1)
			x.out[at-1].runsRead = err == nil
			x.add(sub, int32(at), depth-1)
		}
	}
}

// RunsRead reports whether WithScripts read the command line this command hands on (its
// Script or what it Carried) to its end and put its commands after it. It is false for a
// command that hands none on, one whose command line is not a known word or does not parse,
// and one deeper than WithScripts was asked to read.
func (c Command) RunsRead() bool { return c.runsRead }

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
//
// The words of xargs, find and the exec commands are passed on as they are and are returned as
// written, so a quoted word stays one word; the others are returned as the shell passes them
// on, because a shell reads them again. ok is false when the command is none of these, names
// no command (`ssh host`, `xargs` alone, a find without -exec), or a find clause has no end.
//
// The commands run where the carrier puts them: on another host, in a container, once per
// file. Their words are still what runs.
func (c Command) Carried() (script string, ok bool) {
	name, args := c.run()
	switch name {
	case "xargs":
		rest, _ := afterOptions(args, &carrierXargs)
		return joinWords(rest, true)
	case "find":
		return findExec(args)
	case "ssh":
		rest, _ := afterOptions(args, &carrierSSH)
		if len(rest) < 2 {
			return "", false
		}
		return joinWords(rest[1:], false)
	case "docker", "podman", "nerdctl":
		if len(args) == 0 || !args[0].Known || args[0].Text != "exec" {
			return "", false
		}
		rest, _ := afterOptions(args[1:], &carrierDockerExec)
		if len(rest) < 2 {
			return "", false
		}
		return joinWords(rest[1:], true)
	case "kubectl":
		if len(args) == 0 || !args[0].Known || args[0].Text != "exec" {
			return "", false
		}
		for i, w := range args {
			if w.Known && w.Text == "--" {
				return joinWords(args[i+1:], true)
			}
		}
	case "su":
		for i, w := range args {
			switch {
			case !w.Known:
			case (w.Text == "-c" || w.Text == "--command") && i+1 < len(args) && args[i+1].Known:
				return args[i+1].Text, args[i+1].Text != ""
			case strings.HasPrefix(w.Text, "--command="):
				return w.Text[len("--command="):], len(w.Text) > len("--command=")
			}
		}
	case "eval":
		return joinWords(args, false)
	case "watch":
		rest, _ := afterOptions(args, &carrierWatch)
		return joinWords(rest, false)
	case "trap":
		// trap action signal..: `-` and "" reset a signal and run nothing; -l and -p list.
		if len(args) >= 2 && args[0].Known && args[0].Text != "" && !strings.HasPrefix(args[0].Text, "-") {
			return args[0].Text, true
		}
	}
	return "", false
}

var (
	carrierXargs = wrapperSpec{optionSpec: optionSpec{short: "IndPLEsaJRS", long: []string{
		"--max-args", "--max-procs", "--delimiter", "--arg-file", "--max-lines", "--max-chars", "--eof", "--process-slot-var",
	}}}
	carrierSSH        = wrapperSpec{optionSpec: optionSpec{short: "BbcDEeFIiJLlmOopQRSWw"}}
	carrierDockerExec = wrapperSpec{optionSpec: optionSpec{short: "euw", long: []string{"--env", "--env-file", "--user", "--workdir", "--detach-keys"}}}
	carrierWatch      = wrapperSpec{optionSpec: optionSpec{short: "nq", long: []string{"--interval", "--equexit"}}}
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
