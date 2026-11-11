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
// command runs the script of its -Command option (powershellScript).
func (c Command) Script() (script string, ok bool) {
	words := c.Argv
	start := -1
	for i, w := range words {
		if w.Known && isShell(w.Text) {
			start = i
			break
		}
	}
	if start < 0 {
		return c.powershellScript()
	}
	// command is set by -c, and stdin by -s: the operands are then the script's arguments.
	command, stdin := false, false
scan:
	for i := start + 1; ; i++ {
		if i >= len(words) {
			stdin = !command
			break
		}
		w := words[i]
		if !w.Known {
			if stdin && !command {
				break // with -s, the script's first argument
			}
			return "", false
		}
		t := w.Text
		switch {
		case t == "--" || t == "-": // a lone - ends the options as -- does
			if command && i+1 < len(words) && words[i+1].Known {
				return words[i+1].Text, true
			}
			stdin = !command && (stdin || i+1 == len(words))
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
				return t, true
			}
			break scan // with -s an argument of the script, without it a script file
		}
	}
	if !stdin {
		return "", false
	}
	if unwrapped, ok := c.Unwrapped(); !ok || len(c.Argv)-len(unwrapped) != start {
		return "", false
	}
	return c.HereInput()
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

// WithScripts returns commands with the commands of each shell's script (Command.Script) put
// after the shell that runs it, InScript set, and so on for the shells inside a script, depth
// scripts deep. The script is read as bash whatever shell runs it. Its pipelines are numbered
// after every pipeline before them, so no two commands of the result share a Pipeline unless
// they are stages of one pipeline.
//
// The result is one command line to FileReads and SocketRedirects, which follow the data
// across a script's boundary: what the shell gets on its standard input reaches the script's
// commands, what they write is what the shell writes, and the script sees the shell's
// environment (its NAME=value prefix, and what the command line exported).
func WithScripts(commands []Command, depth int) []Command {
	if depth <= 0 || !slices.ContainsFunc(commands, hasShellWord) {
		return commands
	}
	x := expander{out: make([]Command, 0, len(commands)+4)}
	x.add(commands, 0, depth)
	return x.out
}

// hasShellWord is a cheap test for a command that may run a script: most do not.
func hasShellWord(c Command) bool {
	return slices.ContainsFunc(c.Argv, func(w Word) bool { return w.Known && (isShell(w.Text) || isPowerShell(w.Text)) })
}

func isPowerShell(word string) bool {
	name := strings.ToLower(ProgramName(word))
	return name == "powershell" || name == "pwsh"
}

type expander struct {
	out []Command
	// pipelines, substs and subshells are the highest of each number given out so far: a
	// script is parsed on its own and its numbers start again, so they are moved past these.
	pipelines        int
	substs, subshell int32
}

// add appends commands, the commands of one parse, as the script of the shell at index
// shell-1 of the result, or as the command line itself when shell is 0.
func (x *expander) add(commands []Command, shell int32, depth int) {
	pipelines, substs, subshell := x.pipelines, x.substs, x.subshell
	for i := range commands {
		c := &commands[i]
		x.pipelines = max(x.pipelines, pipelines+c.Pipeline)
		x.substs = max(x.substs, substs+c.within)
		for _, ref := range c.refs {
			x.substs = max(x.substs, substs+int32(ref.subst))
		}
		x.subshell = max(x.subshell, subshell+c.subshell)
	}
	// outer names a compound of the same parse by its index there, which a script spliced in
	// before it moves: at holds where each of these commands landed, 1-based like outer.
	var at []int32
	if slices.ContainsFunc(commands, func(c Command) bool { return c.outer != 0 }) {
		at = make([]int32, len(commands))
	}
	for i, c := range commands {
		if c.outer != 0 {
			c.outer = at[c.outer-1]
		}
		c.Pipeline += pipelines
		if shell != 0 {
			c.InScript, c.shell = true, shell
			if c.within != 0 {
				c.within += substs
			}
			if c.subshell != 0 {
				c.subshell += subshell
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
			at[i] = int32(len(x.out))
		}
		if depth <= 0 {
			continue
		}
		if script, ok := c.Script(); ok {
			if sub, _ := Parse(script); len(sub) > 0 {
				at := len(x.out)
				x.out[at-1].scripted = true
				x.add(sub, int32(at), depth-1)
			}
		}
	}
}
