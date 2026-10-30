//go:build windows

package localingest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"runtime"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// DefaultSocketPath is the named pipe a system-mode collector in the restricted log access mode
// serves. Named pipes live in their own namespace rather than in a directory, so what keeps a user
// from standing up a pipe of this name first is checked on both ends instead: the collector creates
// it as the first instance or fails to start, and a client sends only to a pipe whose owner is
// SYSTEM or Administrators, which no ordinary account can make itself.
const DefaultSocketPath = `\\.\pipe\beacon-agent-ingest`

// Listener is what Listen returns.
type Listener = net.Listener

// pipeSecurity is the pipe's security descriptor, in SDDL.
//
//   - Network logons are denied outright, ahead of every grant. The pipe also rejects remote
//     clients (go-winio creates it with FILE_PIPE_REJECT_REMOTE_CLIENTS), so the deny is a second
//     lock on the same door: a pipe is otherwise reachable over SMB, and this channel must never be
//     a network one.
//   - SYSTEM and Administrators have full control.
//   - Authenticated users may connect, read and write: 0x12019b is FILE_GENERIC_READ and
//     FILE_GENERIC_WRITE without FILE_APPEND_DATA, which on a pipe is FILE_CREATE_PIPE_INSTANCE.
//     Without it no other account can add an instance of its own to this pipe and answer the
//     clients that connect to it.
const pipeSecurity = "D:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x12019b;;;AU)"

// clientPipeAccess is what a client asks for: read and write data, read attributes (go-winio reads
// the pipe's mode), READ_CONTROL to read the pipe's owner, and SYNCHRONIZE. GENERIC_WRITE would
// include FILE_CREATE_PIPE_INSTANCE, which pipeSecurity withholds, and fail.
const clientPipeAccess = windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.FILE_READ_ATTRIBUTES |
	windows.READ_CONTROL | windows.SYNCHRONIZE

const (
	sidLocalSystem    = "S-1-5-18"
	sidAdministrators = "S-1-5-32-544"
)

// pipeBufferBytes sizes the server's inbound buffer to hold a maximum-size line with room to spare.
const pipeBufferBytes = 2 * (MaxLineBytes + 2)

// Listen creates the pipe at path for a Server to serve. The pipe must not already exist: its first
// instance is created with pipeSecurity, and a pipe someone else created first -- another collector,
// or an account squatting the name -- fails the call rather than being served alongside.
//
// The pipe is in message mode so a client can signal the end of its batch with a zero-byte message,
// which is how a pipe half-closes; it is read as a byte stream all the same.
func Listen(path string) (Listener, error) {
	if path == "" {
		path = DefaultSocketPath
	}
	if !isPipePath(path) {
		return nil, fmt.Errorf("%s is not a named pipe path (\\\\.\\pipe\\name)", path)
	}
	l, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: pipeSecurity,
		MessageMode:        true,
		InputBufferSize:    pipeBufferBytes,
		OutputBufferSize:   maxResponseBytes,
	})
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) || errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("another process already serves %s: %w", path, err)
		}
		return nil, err
	}
	return l, nil
}

// RemoveStale has nothing to remove on Windows: a pipe disappears with the last handle to it, so a
// collector that was killed leaves nothing behind.
func RemoveStale(string) error { return nil }

func isPipePath(path string) bool {
	return len(path) > len(`\\.\pipe\`) && strings.EqualFold(path[:len(`\\.\pipe\`)], `\\.\pipe\`)
}

// connect opens the pipe at the client's path and checks who created it before anything is sent.
//
// The client allows the server to identify it and no more (SECURITY_IDENTIFICATION): the collector
// needs to know whose events these are, and has no business acting as that account.
func (c Client) connect(timeout time.Duration) (net.Conn, error) {
	path := c.path()
	if !isPipePath(path) {
		return nil, fmt.Errorf("%w: %s is not a named pipe", ErrUntrustedSocket, path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := winio.DialPipeAccessImpLevel(ctx, path, clientPipeAccess, winio.PipeImpLevelIdentification)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoSocket
		}
		return nil, fmt.Errorf("connect to %s: %w", path, err)
	}
	owner, err := pipeOwner(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %s: %v", ErrUntrustedSocket, path, err)
	}
	if !c.trustedPipeOwner(owner) {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %s was created by %s, not SYSTEM or Administrators", ErrUntrustedSocket, path, owner)
	}
	return conn, nil
}

// trustedPipeOwner reports whether a pipe owned by sid can only have been created by a privileged
// process. A pipe's owner is its creator's default owner -- SYSTEM, or Administrators for an
// elevated administrator -- and no ordinary account can name another owner when it creates one.
func (c Client) trustedPipeOwner(sid string) bool {
	switch {
	case strings.EqualFold(sid, sidLocalSystem), strings.EqualFold(sid, sidAdministrators):
		return true
	case c.TrustedSID != "" && strings.EqualFold(sid, c.TrustedSID):
		return true
	}
	return false
}

// handleOf is the Windows handle under a go-winio pipe connection.
func handleOf(conn net.Conn) (windows.Handle, error) {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return 0, errors.New("not a named pipe")
	}
	return windows.Handle(f.Fd()), nil
}

func pipeOwner(conn net.Conn) (string, error) {
	h, err := handleOf(conn)
	if err != nil {
		return "", err
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("read the pipe's owner: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return "", fmt.Errorf("read the pipe's owner: %v", err)
	}
	sid := owner.String()
	runtime.KeepAlive(sd)
	return sid, nil
}

// connectingAccount is the account of the process at the other end of conn, read from that
// process's token before anything is read from the pipe, for the server's per-account connection
// cap. It names whoever runs that process id now, which could in principle be another process that
// took the id after the client exited; identify, which reads the client's own token once the pipe
// has been read, settles the identity events are stamped with, and the server refuses the
// connection when the two disagree.
func connectingAccount(conn net.Conn) (string, error) {
	h, err := handleOf(conn)
	if err != nil {
		return "", err
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(h, &pid); err != nil {
		return "", fmt.Errorf("read the client's process id: %w", err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("open the client process %d: %w", pid, err)
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", fmt.Errorf("open the client process's token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read the client process's account: %w", err)
	}
	return user.User.Sid.String(), nil
}

var procImpersonateNamedPipeClient = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

// identify asks Windows who is on the other end of conn: the server takes on the client's identity
// for a moment, reads the account from that token, and reverts. The client connected at
// identification level, which is enough to read the token and not enough to act as the account.
// It needs the server to have read from the pipe first, which Server.handle does.
func identify(conn net.Conn) (Peer, error) {
	h, err := handleOf(conn)
	if err != nil {
		return Peer{}, err
	}
	sid, err := pipeClientSID(h)
	if err != nil {
		return Peer{}, err
	}
	peer := Peer{UID: sid}
	var pid uint32
	if windows.GetNamedPipeClientProcessId(h, &pid) == nil {
		peer.PID = int32(pid)
	}
	return peer, nil
}

// pipeClientSID reads the account of the client of the pipe h. It runs on a thread of its own,
// locked, because impersonation belongs to the thread: if reverting failed, the thread is left
// locked so it ends with its goroutine and the client's identity never leaks to other work.
func pipeClientSID(h windows.Handle) (string, error) {
	type result struct {
		sid string
		err error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		if r, _, callErr := procImpersonateNamedPipeClient.Call(uintptr(h)); r == 0 {
			runtime.UnlockOSThread()
			done <- result{err: fmt.Errorf("identify the client: %w", callErr)}
			return
		}
		var token windows.Token
		openErr := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token)
		if err := windows.RevertToSelf(); err != nil {
			if openErr == nil {
				_ = token.Close()
			}
			done <- result{err: fmt.Errorf("stop identifying as the client: %w", err)}
			return
		}
		runtime.UnlockOSThread()
		if openErr != nil {
			done <- result{err: fmt.Errorf("read the client's token: %w", openErr)}
			return
		}
		defer token.Close()
		user, err := token.GetTokenUser()
		if err != nil {
			done <- result{err: fmt.Errorf("read the client's account: %w", err)}
			return
		}
		if user.User.Sid.IsWellKnown(windows.WinAnonymousSid) {
			done <- result{err: errors.New("the client connected anonymously")}
			return
		}
		done <- result{sid: user.User.Sid.String()}
	}()
	r := <-done
	return r.sid, r.err
}

const peerCredentialsSupported = true

// temporaryAcceptError reports an accept failure that passes on its own: the system is short of
// memory or handles for a moment.
func temporaryAcceptError(err error) bool {
	for _, errno := range []windows.Errno{windows.ERROR_NOT_ENOUGH_MEMORY, windows.ERROR_NO_SYSTEM_RESOURCES, windows.ERROR_TOO_MANY_OPEN_FILES, windows.ERROR_OUTOFMEMORY} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}
