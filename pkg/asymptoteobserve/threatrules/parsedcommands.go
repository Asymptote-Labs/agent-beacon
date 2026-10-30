package threatrules

import (
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/shellcmd"
)

// ParsedCommandsPath is the CEL path of the derived parsed command line, after the "e." prefix.
const ParsedCommandsPath = "command.parsed_commands"

// ParsedCommandsLimit bounds how much of command.command is parsed: the limit the writers apply
// to the retained command line, so a longer value (from an in-process caller) is cut there too.
const ParsedCommandsLimit = asymptoteobserve.DefaultStringLimit

// maxShellDepth bounds how many `sh -c` scripts inside one another are read.
const maxShellDepth = 2

// ParsedCommands returns what a rule sees as e.command.parsed_commands: command read as a bash
// command line, one entry per command it runs, in the order shellcmd.Parse walks them.
//
//   - The first ParsedCommandsLimit bytes are parsed. A command line that ends early (an unclosed
//     quote, here-document or substitution, as a cut capture does) yields the commands before the
//     point where it became incomplete; one that does not parse at all yields none, and a rule
//     falls back to matching command.command.
//   - name is the base name of the command after its wrappers (sudo, env, command, exec, nohup,
//     time), "" when it is unknown or a wrapper option hides it (sudo -i, env -C).
//   - argv and argv_raw are every word, wrappers included: argv as the shell passes it on, ""
//     for a word whose value depends on an expansion, glob or leading tilde; argv_raw as written.
//   - env maps each NAME=value assignment before the command to its value ("" when unknown).
//   - file_effects are shellcmd's file effects of the command: {path, op}.
//   - pipeline, stage and nested are shellcmd's.
//   - The script a shell (sh, bash, zsh, dash, ksh) runs with -c, when it is a known word, is
//     read the same way, depth-limited, and its commands follow the shell's with nested true and
//     pipeline numbers of their own. The shell may follow other words (`docker exec c sh -c ..`).
//
// Here-document bodies, and the strings of commands other than a shell's -c, are not read as
// commands. The text is read as bash whatever shell ran it.
//
// It is exported so that another engine implementing the Threat Rules contract can derive the
// field the same way; fixtures that exercise it only conform if the derivation agrees.
func ParsedCommands(command string) []asymptoteobserve.ParsedCommand {
	if command == "" {
		return nil
	}
	cmds, _ := shellcmd.Parse(asymptoteobserve.TruncateUTF8(command, ParsedCommandsLimit))
	if len(cmds) == 0 {
		return nil
	}
	p := parsedCommands{out: make([]asymptoteobserve.ParsedCommand, 0, len(cmds))}
	p.add(cmds, false, 0)
	return p.out
}

type parsedCommands struct {
	out       []asymptoteobserve.ParsedCommand
	pipelines int // the highest pipeline number assigned so far
}

// add appends cmds, numbering their pipelines after every pipeline already added.
func (p *parsedCommands) add(cmds []shellcmd.Command, nested bool, depth int) {
	base := p.pipelines
	for _, c := range cmds {
		p.pipelines = max(p.pipelines, base+c.Pipeline)
	}
	for _, c := range cmds {
		p.out = append(p.out, parsedCommand(c, base, nested))
		if depth >= maxShellDepth {
			continue
		}
		if script, ok := shellScript(c.Argv); ok {
			if sub, _ := shellcmd.Parse(script); len(sub) > 0 {
				p.add(sub, true, depth+1)
			}
		}
	}
}

func parsedCommand(c shellcmd.Command, pipelineBase int, nested bool) asymptoteobserve.ParsedCommand {
	pc := asymptoteobserve.ParsedCommand{
		Name:     c.Name(),
		Argv:     make([]string, len(c.Argv)),
		ArgvRaw:  make([]string, len(c.Argv)),
		Pipeline: pipelineBase + c.Pipeline,
		Stage:    c.Stage,
		Nested:   nested || c.Nested,
	}
	for i, w := range c.Argv {
		pc.Argv[i] = w.Text
		pc.ArgvRaw[i] = w.Raw
	}
	if len(c.Env) > 0 {
		pc.Env = make(map[string]string, len(c.Env))
		for _, as := range c.Env {
			pc.Env[as.Name] = as.Value.Text
		}
	}
	if effects := c.FileEffects(); len(effects) > 0 {
		pc.FileEffects = make([]asymptoteobserve.ParsedFileEffect, len(effects))
		for i, e := range effects {
			pc.FileEffects[i] = asymptoteobserve.ParsedFileEffect{Path: e.Path, Op: string(e.Op)}
		}
	}
	return pc
}

// shellScript returns the script of the first shell word in words when that shell runs it with
// -c: the first operand after an option cluster containing c. ok is false when there is no such
// shell, the shell runs a file or stdin instead, or the script is not a known word.
func shellScript(words []shellcmd.Word) (script string, ok bool) {
	start := -1
	for i, w := range words {
		if w.Known && isShell(w.Text) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	command := false
	for i := start + 1; i < len(words); i++ {
		w := words[i]
		if !w.Known {
			return "", false
		}
		t := w.Text
		switch {
		case t == "--":
			if command && i+1 < len(words) && words[i+1].Known {
				return words[i+1].Text, true
			}
			return "", false
		case t == "--rcfile" || t == "--init-file":
			i++ // takes a file
		case strings.HasPrefix(t, "--"):
		case len(t) > 1 && (t[0] == '-' || t[0] == '+'):
			if t[0] == '-' && strings.ContainsRune(t[1:], 'c') {
				command = true
			}
			if last := t[len(t)-1]; last == 'o' || last == 'O' {
				i++ // -o option / -O shopt_option takes a name
			}
		default:
			if command {
				return t, true
			}
			return "", false
		}
	}
	return "", false
}

func isShell(word string) bool {
	switch filepath.Base(word) {
	case "sh", "bash", "zsh", "dash", "ksh":
		return true
	}
	return false
}
