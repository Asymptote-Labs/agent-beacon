package threatrules

import (
	"slices"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/shellcmd"
)

// ParsedCommandsPath is the CEL path of the derived parsed command line, after the "e." prefix.
const ParsedCommandsPath = "command.parsed_commands"

// ParsedIncompletePath is the CEL path of the derived flag for a command line that was not read
// to its end, after the "e." prefix.
const ParsedIncompletePath = "command.parsed_incomplete"

// v1_3Fields are the fields of a parsed command that spec v1.3 added.
var v1_3Fields = []string{"args", "file_reads", "file_writes", "script_sends", "socket_redirects", "runs_read", "span"}

// ParsedCommandsLimit bounds how much of command.command is parsed: the limit the writers apply
// to the retained command line, so a longer value (from an in-process caller) is cut there too.
const ParsedCommandsLimit = asymptoteobserve.DefaultStringLimit

// maxShellDepth bounds how many `sh -c` scripts inside one another are read.
const maxShellDepth = 3

// ParsedCommands returns what a rule sees as e.command.parsed_commands: command read as a bash
// command line, one entry per command it runs, in the order shellcmd.Parse walks them.
//
//   - The first ParsedCommandsLimit bytes are parsed. A command line that ends early (an unclosed
//     quote, here-document or substitution, as a cut capture does) yields the commands before the
//     point where it became incomplete; one that does not parse at all yields none, and a rule
//     falls back to matching command.command.
//   - name is the base name of the command after its wrappers (sudo, env, command, exec, nohup,
//     time, timeout, nice, ionice, stdbuf, doas, torsocks, proxychains), "" when it is unknown
//     or a wrapper option hides it (sudo -i, env -C).
//   - argv and argv_raw are every word, wrappers included: argv as the shell passes it on, ""
//     for a word whose value depends on an expansion, glob or leading tilde; argv_raw as written.
//   - args is shellcmd's ArgsText: the words of argv after name, one per line.
//   - env maps each NAME=value assignment before the command to its value ("" when unknown).
//   - file_effects are shellcmd's file effects of the command: {path, op}. file_writes are its
//     file writes: the same, and the ones whose path is only known as written.
//   - file_reads are shellcmd's file reads of the command: {path, via}, each path as written.
//     script_sends are the paths of those an inline script opens inside a call that sends them.
//   - socket_redirects are shellcmd's socket redirections of the command: {path, op}.
//   - pipeline, stage and nested are shellcmd's.
//   - The script a shell (sh, bash, zsh, dash, ksh) runs with -c, when it is a known word, is
//     read the same way, depth-limited, and its commands follow the shell's with nested true and
//     pipeline numbers of their own (shellcmd.WithScripts). The shell may follow other words
//     (`docker exec c sh -c ..`).
//   - So is the here-document or here-string a shell reads its script from, when the shell is
//     the command the statement runs (after its wrappers) and it is given no -c and no script
//     file: `sudo bash <<'EOF'`, `sh -s -- arg <<EOF`, `bash <<< 'cmd'`.
//   - file_reads and socket_redirects follow the data across a script's boundary: the script's
//     commands read what the shell gets on its standard input, and the shell writes what they
//     write.
//
// Other here-document bodies, what a pipe sends a shell, and the strings of commands other than
// a shell's -c are not read as commands. The text is read as bash whatever shell ran it.
//
// It is exported so that another engine implementing the Threat Rules contract can derive the
// field the same way; fixtures that exercise it only conform if the derivation agrees.
func ParsedCommands(command string) []asymptoteobserve.ParsedCommand {
	parsed, _ := ParsedCommandLine(command)
	return parsed
}

// ParsedCommandLine returns ParsedCommands of command, and whether the command line was not read
// to its end (e.command.parsed_incomplete): it does not parse, it ends early, or it is longer
// than ParsedCommandsLimit. A rule can then conclude nothing from what the list lacks.
func ParsedCommandLine(command string) (parsed []asymptoteobserve.ParsedCommand, incomplete bool) {
	if command == "" {
		return nil, false
	}
	read := asymptoteobserve.TruncateUTF8(command, ParsedCommandsLimit)
	cmds, err := shellcmd.Parse(read)
	incomplete = err != nil || len(read) < len(command)
	if len(cmds) == 0 {
		return nil, incomplete
	}
	cmds = shellcmd.WithScripts(cmds, maxShellDepth)
	reads := shellcmd.FileReads(cmds)
	sockets := shellcmd.SocketRedirects(cmds)
	out := make([]asymptoteobserve.ParsedCommand, len(cmds))
	wrote := make([]bool, len(cmds))
	for i, c := range cmds {
		var taken []shellcmd.FileRead
		if reads != nil {
			taken = reads[i]
		}
		out[i] = parsedCommand(c, taken)
		out[i].RunsRead = c.RunsRead()
		wrote[i] = slices.ContainsFunc(out[i].FileWrites, writesContent)
		if sockets != nil {
			for _, s := range sockets[i] {
				out[i].SocketRedirects = append(out[i].SocketRedirects, asymptoteobserve.ParsedSocketRedirect{Path: s.Path, Op: string(s.Op)})
			}
		}
	}
	for i, span := range shellcmd.Spans(cmds, wrote) {
		out[i].Span = span
	}
	return out, incomplete
}

// writesContent reports whether a file write puts content in a file a later command of the
// line could read or run: not a deletion, an emptying or a touch, and not a device.
func writesContent(e asymptoteobserve.ParsedFileEffect) bool {
	switch shellcmd.Op(e.Op) {
	case shellcmd.OpWrite, shellcmd.OpAppend, shellcmd.OpEdit, shellcmd.OpMoveTo, shellcmd.OpLink:
		return !strings.HasPrefix(e.Path, "/dev/")
	}
	return false
}

func parsedCommand(c shellcmd.Command, reads []shellcmd.FileRead) asymptoteobserve.ParsedCommand {
	pc := asymptoteobserve.ParsedCommand{
		Name:     c.Name(),
		Args:     c.ArgsText(),
		Argv:     make([]string, len(c.Argv)),
		ArgvRaw:  make([]string, len(c.Argv)),
		Pipeline: c.Pipeline,
		Stage:    c.Stage,
		Nested:   c.Nested || c.InScript,
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
	pc.FileEffects = parsedFileEffects(c.FileEffects())
	// A command whose words are all known, and that puts no files into a directory, writes
	// exactly its effects.
	if pc.FileWrites = pc.FileEffects; !c.WritesAreEffects() {
		pc.FileWrites = parsedFileEffects(c.FileWrites())
	}
	if len(reads) > 0 {
		pc.FileReads = make([]asymptoteobserve.ParsedFileRead, len(reads))
		for i, r := range reads {
			pc.FileReads[i] = asymptoteobserve.ParsedFileRead{Path: r.Path, Via: string(r.Via)}
			if r.Sent && !slices.Contains(pc.ScriptSends, r.Path) {
				pc.ScriptSends = append(pc.ScriptSends, r.Path)
			}
		}
	}
	return pc
}

func parsedFileEffects(effects []shellcmd.FileEffect) []asymptoteobserve.ParsedFileEffect {
	if len(effects) == 0 {
		return nil
	}
	out := make([]asymptoteobserve.ParsedFileEffect, len(effects))
	for i, e := range effects {
		out[i] = asymptoteobserve.ParsedFileEffect{Path: e.Path, Op: string(e.Op)}
	}
	return out
}
