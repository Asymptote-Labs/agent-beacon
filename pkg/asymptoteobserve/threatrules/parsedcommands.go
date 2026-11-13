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
//     time, timeout, nice, ionice, stdbuf, doas, torsocks, proxychains, caffeinate, uv run and
//     the like: shellcmd.Command.Unwrapped), "" when it is unknown or a wrapper option hides it
//     (sudo -l, env -C).
//   - argv and argv_raw are every word, wrappers included: argv as the shell passes it on, ""
//     for a word whose value depends on an expansion, glob or leading tilde; argv_raw as written.
//   - args is shellcmd's ArgsText: the words of argv after name, one per line.
//   - env maps each NAME=value assignment before the command to its value ("" when unknown).
//   - file_effects are shellcmd's file effects of the command: {path, op}. file_writes are its
//     file writes: the same, the ones whose path is only known as written, and the directory
//     an archive or a clone puts files into.
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
//     file: `sudo bash <<'EOF'`, `sh -s -- arg <<EOF`, `bash <<< 'cmd'`; what a pipe sends
//     such a shell when the stage before prints its own words (`echo 'cmd' | sh`); and the
//     command line a carrier hands on (xargs, find -exec, ssh, docker exec, eval and the
//     like: shellcmd.Command.Carried).
//   - runs_read says the command line a command hands on was read to its end, and span is
//     shellcmd's span of the command.
//   - file_reads and socket_redirects follow the data across a script's boundary: the script's
//     commands read what the shell gets on its standard input, and the shell writes what they
//     write.
//
// Other here-document bodies, what a pipe sends a shell from a command that does not print its
// own words, a script file, and the code of an interpreter are not read as commands. The text
// is read as bash whatever shell ran it.
//
// It is exported so that another engine implementing the Threat Rules contract can derive the
// field the same way; fixtures that exercise it only conform if the derivation agrees.
func ParsedCommands(command string) []asymptoteobserve.ParsedCommand {
	parsed, _ := ParsedCommandLine(command)
	return parsed
}

// ParsedCommandLine returns ParsedCommands of command, and whether the command line was not read
// to its end (e.command.parsed_incomplete): it does not parse, it ends early, it is longer
// than ParsedCommandsLimit, or it has more commands than maxParsedCommands or more words
// than maxParsedWords. A rule can then conclude nothing from what the list lacks.
//
// A command line is whatever an agent ran or was made to run, and every scan, the dashboard
// and the gateway read each one. Should the reading panic, the line counts as one that was
// not read: it has no parsed commands and is incomplete, so the rules that match a line's text
// still see it and nothing else stops. FuzzParsedCommandLine calls the reading itself, where a
// panic is a failure.
func ParsedCommandLine(command string) (parsed []asymptoteobserve.ParsedCommand, incomplete bool) {
	defer func() {
		if recover() != nil {
			parsed, incomplete = nil, true
		}
	}()
	return readCommandLine(command)
}

// readCommandLine is the reading ParsedCommandLine guards; a test replaces it with one that
// panics.
var readCommandLine = parsedCommandLine

// maxParsedCommands and maxParsedWords bound the commands of one command line, those of the
// scripts it runs included, and their words. Past either the line is not read, like one whose
// braces produce too many words (shellcmd.ErrTooManyWords).
//
// What a rule does with the commands grows faster than their number: a rule that asks of each
// command whether another one feeds it asks the square. The longest of 18,600 real command
// lines has 73 commands and 200 words; a line of the limit's 4,096 bytes can be made to hold
// 2,048 commands, which cost every rule together over a second where 256 cost a fifth of
// one, and 1,300 substitutions inside one another took one rule past the cost limit, which
// fails a scan. A line of ParsedCommandsLimit bytes holds half as many words as bytes, and
// each script inside it may add what its brace expansions produce.
const (
	maxParsedCommands = 256
	maxParsedWords    = ParsedCommandsLimit
)

func parsedCommandLine(command string) (parsed []asymptoteobserve.ParsedCommand, incomplete bool) {
	if command == "" {
		return nil, false
	}
	read := asymptoteobserve.TruncateUTF8(command, ParsedCommandsLimit)
	cmds, err := shellcmd.Parse(read)
	incomplete = err != nil || len(read) < len(command)
	if len(cmds) == 0 {
		return nil, incomplete
	}
	if len(cmds) > maxParsedCommands {
		return nil, true
	}
	cmds = shellcmd.WithScripts(cmds, maxShellDepth)
	words := 0
	for _, c := range cmds {
		words += len(c.Argv)
	}
	if words > maxParsedWords || len(cmds) > maxParsedCommands {
		return nil, true
	}
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
