package threatrules

import (
	"slices"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/shellcmd"
)

// ParsedCommandsPath is the CEL path of the derived parsed command line, after the "e." prefix.
const ParsedCommandsPath = "command.parsed_commands"

// ParsedIncompletePath is the CEL path of the derived flag for a command line that was not read
// to its end, after the "e." prefix.
const ParsedIncompletePath = "command.parsed_incomplete"

// ParsedCommandsLimit bounds how much of command.command is parsed: the limit the writers apply
// to the retained command line, so a longer value (from an in-process caller) is cut there too
// (shellcmd.LineLimit).
const ParsedCommandsLimit = shellcmd.LineLimit

// ParsedCommands returns what a rule sees as e.command.parsed_commands: command read as a bash
// command line by shellcmd.Read, one entry per command it runs, the commands of the command
// lines they hand on after them. Each entry is one shellcmd.Command with what Read worked out
// for it: name (Command.Name, after the wrappers Command.Unwrapped looks through), args
// (Command.ArgsText), argv and argv_raw (every word, wrappers included: its value, "" when
// unknown, and as written), env, pipeline, stage, nested (inside a substitution or a command
// line another command hands on), file_effects, file_writes, file_reads with script_sends,
// socket_redirects, runs_read, span, and earlier_stages and later_stages. SPEC.md ("Derived
// fields") is the contract for each; this is its reference derivation.
//
// It is exported so that another engine implementing the Threat Rules contract can derive the
// field the same way; fixtures that exercise it only conform if the derivation agrees.
func ParsedCommands(command string) []asymptoteobserve.ParsedCommand {
	parsed, _ := ParsedCommandLine(command)
	return parsed
}

// ParsedCommandLine returns ParsedCommands of command, and whether the command line was not read
// to its end (e.command.parsed_incomplete): it does not parse, it ends early, it is longer
// than ParsedCommandsLimit, or reading it passes a bound of shellcmd.Read (more than
// shellcmd.MaxCommands commands, shellcmd.MaxWords words, or scripts past theirs), in which
// case it has no parsed commands. A rule can then conclude nothing from what the list lacks.
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

// parsedCommandLine is shellcmd.Read of command, each command as a rule sees it.
func parsedCommandLine(command string) (parsed []asymptoteobserve.ParsedCommand, incomplete bool) {
	line := shellcmd.Read(command)
	if len(line.Commands) == 0 {
		return nil, line.Incomplete
	}
	out := make([]asymptoteobserve.ParsedCommand, len(line.Commands))
	for i := range line.Commands {
		out[i] = parsedCommand(&line, i)
	}
	return out, line.Incomplete
}

func parsedCommand(line *shellcmd.Line, i int) asymptoteobserve.ParsedCommand {
	c := &line.Commands[i]
	pc := asymptoteobserve.ParsedCommand{
		Name:     c.Name(),
		Args:     c.ArgsText(),
		Argv:     make([]string, len(c.Argv)),
		ArgvRaw:  make([]string, len(c.Argv)),
		Pipeline: c.Pipeline,
		Stage:    c.Stage,
		Nested:   c.Nested || c.InScript,
		RunsRead: c.RunsRead(),
		Span:     line.Spans[i],
	}
	for k, w := range c.Argv {
		pc.Argv[k] = w.Text
		pc.ArgvRaw[k] = w.Raw
	}
	if len(c.Env) > 0 {
		pc.Env = make(map[string]string, len(c.Env))
		for _, as := range c.Env {
			pc.Env[as.Name] = as.Value.Text
		}
	}
	// Every effect is a write, in the same order: as many writes are the same list.
	if pc.FileEffects = parsedFileEffects(line.Effects[i]); len(line.Writes[i]) == len(line.Effects[i]) {
		pc.FileWrites = pc.FileEffects
	} else {
		pc.FileWrites = parsedFileEffects(line.Writes[i])
	}
	if line.Reads != nil && len(line.Reads[i]) > 0 {
		pc.FileReads = make([]asymptoteobserve.ParsedFileRead, len(line.Reads[i]))
		for k, r := range line.Reads[i] {
			pc.FileReads[k] = asymptoteobserve.ParsedFileRead{Path: r.Path, Via: string(r.Via)}
			if r.Sent && !slices.Contains(pc.ScriptSends, r.Path) {
				pc.ScriptSends = append(pc.ScriptSends, r.Path)
			}
		}
	}
	if line.Sockets != nil {
		for _, s := range line.Sockets[i] {
			pc.SocketRedirects = append(pc.SocketRedirects, asymptoteobserve.ParsedSocketRedirect{Path: s.Path, Op: string(s.Op)})
		}
	}
	if line.Earlier != nil {
		pc.EarlierStages, pc.LaterStages = line.Earlier[i], line.Later[i]
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
