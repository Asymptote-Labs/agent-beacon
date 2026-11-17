package shellcmd

import (
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// LineLimit is how much of a command line Read reads: the limit the writers apply to a retained
// command line, so a longer value (from an in-process caller) is cut there too.
const LineLimit = asymptoteobserve.DefaultStringLimit

// The bounds of what reading one command line may come to, beside maxBraceWords, maxScriptBytes
// and maxCutRetries. Past any of them the line is not read at all (Read): it has no commands and
// is incomplete, which leaves it to a caller's reading of its text, rather than read without
// some of what it says.
//
// What a rule does with the commands grows faster than their number: a rule that asks of each
// command whether another one feeds it asks the square. The longest of 18,600 real command
// lines has 73 commands and 200 words; a line of LineLimit bytes can be made to hold 2,048
// commands, which cost every rule together over a second where 256 cost a fifth of one, and
// 1,300 substitutions inside one another took one rule past the cost limit, which fails a scan.
const (
	// MaxDepth is how many command lines handed on inside one another are read (`sh -c` in
	// `sh -c` ..): one deeper is not read, and the command that hands it on has RunsRead false.
	MaxDepth = 3
	// MaxCommands is how many commands the line and the command lines it hands on may have.
	MaxCommands = 256
	// MaxWords is how many words those commands may come to.
	MaxWords = 4096
)

// Line is a command line as a caller that acts on it reads it: its commands with the commands of
// the command lines they hand on (withScripts), and what each of them takes in, writes and runs.
// Every slice indexed by command is parallel to Commands.
type Line struct {
	Commands []Command
	// Incomplete is set when the line was not read to its end: it does not parse, it ends
	// early (a cut capture), it is longer than LineLimit, or reading it would pass a bound
	// (see MaxCommands). Commands then holds what stands before the point where it became
	// incomplete, and nothing when a bound was passed.
	Incomplete bool
	// Reads are the files each command takes in (fileReads), and Sockets its redirections to
	// or from a network pseudo-file (socketRedirects); each is nil when no command has any.
	Reads   [][]FileRead
	Sockets [][]SocketRedirect
	// Effects and Writes are each command's FileEffects and FileWrites.
	Effects, Writes [][]FileEffect
	// Spans are each command's span (spans).
	Spans []string
	// Earlier and Later are, for each command, the indexes of the commands at earlier and at
	// later stages of its pipeline, in the order of Commands.
	Earlier, Later [][]int
}

// Read reads a command line as bash, the whole of what this package says about it, within the
// bounds above: its first LineLimit bytes are parsed (Parse), the command lines its commands
// hand on are read MaxDepth levels deep (withScripts), and the files, sockets and spans of the
// commands are worked out together. An empty line is no commands and complete.
func Read(command string) Line {
	if command == "" {
		return Line{}
	}
	text := asymptoteobserve.TruncateUTF8(command, LineLimit)
	commands, err := Parse(text)
	line := Line{Incomplete: err != nil || len(text) < len(command)}
	if len(commands) == 0 {
		return line
	}
	commands, ok := withScripts(commands, MaxDepth)
	if !ok {
		line.Incomplete = true
		return line
	}
	line.Commands = commands
	line.Reads = fileReads(commands)
	line.Sockets = socketRedirects(commands)
	line.Effects = make([][]FileEffect, len(commands))
	line.Writes = make([][]FileEffect, len(commands))
	for i := range commands {
		line.Effects[i] = commands[i].FileEffects()
		line.Writes[i] = commands[i].FileWrites()
	}
	line.Spans = spans(commands, line.Writes)
	line.Earlier, line.Later = stageLists(commands)
	return line
}

// pastBound reports whether commands and words, so far, are more than a line is read with.
func pastBound(commands, words int) bool {
	return commands > MaxCommands || words > MaxWords
}

// stageLists works out Line.Earlier and Line.Later.
//
// A rule about two stages of a pipeline (a download piped to a shell, an environment dump
// piped to a sender) used to find the second by asking every command of the line whether it
// is in the first one's pipeline, for each candidate first: the square of the line's commands,
// and most of what the costliest lines cost. The relation is worked out here once, and such a
// rule asks the few commands of the pipeline. One pipeline of n stages still has n(n-1)/2
// pairs; MaxCommands bounds n.
func stageLists(commands []Command) (earlier, later [][]int) {
	if len(commands) < 2 {
		return nil, nil
	}
	pipelines := map[int][]int{}
	for i := range commands {
		pipelines[commands[i].Pipeline] = append(pipelines[commands[i].Pipeline], i)
	}
	for _, members := range pipelines {
		if len(members) < 2 {
			continue
		}
		if earlier == nil {
			earlier, later = make([][]int, len(commands)), make([][]int, len(commands))
		}
		for _, i := range members {
			// One allocation holds both lists: the earlier stages, then the later ones.
			both := make([]int, 0, len(members)-1)
			for _, j := range members {
				if commands[j].Stage < commands[i].Stage {
					both = append(both, j)
				}
			}
			n := len(both)
			for _, j := range members {
				if commands[j].Stage > commands[i].Stage {
					both = append(both, j)
				}
			}
			if n > 0 {
				earlier[i] = both[:n:n]
			}
			if n < len(both) {
				later[i] = both[n:]
			}
		}
	}
	return earlier, later
}
