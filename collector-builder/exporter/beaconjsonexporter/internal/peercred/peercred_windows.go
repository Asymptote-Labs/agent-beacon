//go:build windows

package peercred

import (
	"errors"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const tcpTableOwnerPIDAll = 5 // TCP_TABLE_OWNER_PID_ALL

// extendedTCPTable returns GetExtendedTcpTable's TCP_TABLE_OWNER_PID_ALL table for one address
// family; a var so tests could stand in for it.
var extendedTCPTable = func(family uint32) ([]byte, error) {
	if err := procGetExtendedTcpTable.Find(); err != nil {
		return nil, err
	}
	size := uint32(16 << 10)
	for attempt := 0; attempt < 8; attempt++ {
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			0, // unsorted
			uintptr(family),
			tcpTableOwnerPIDAll,
			0,
		)
		switch windows.Errno(r) {
		case 0:
			return buf, nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			// size now holds what the table needs; leave room for connections opened meanwhile.
			size += size/8 + 1024
		default:
			return nil, windows.Errno(r)
		}
	}
	return nil, windows.ERROR_INSUFFICIENT_BUFFER
}

// connectionOwner is the PID that owns the client end of client -> server, read from the IPv4 table
// and, for an IPv4 pair, also from the IPv6 table, where a dual-stack socket appears with
// IPv4-mapped addresses.
func connectionOwner(client, server *net.TCPAddr) (uint32, error) {
	var rows []tcpOwnerRow
	ipv4 := client.IP.To4() != nil && server.IP.To4() != nil
	if ipv4 {
		buf, err := extendedTCPTable(windows.AF_INET)
		if err != nil {
			return 0, fmt.Errorf("read the IPv4 TCP table: %w", err)
		}
		r, err := tcpTable4Rows(buf)
		if err != nil {
			return 0, err
		}
		rows = append(rows, r...)
	}
	buf, err := extendedTCPTable(windows.AF_INET6)
	switch {
	case err != nil && ipv4:
		// IPv6 may be off; the IPv4 table is the answer then.
	case err != nil:
		return 0, fmt.Errorf("read the IPv6 TCP table: %w", err)
	default:
		r, err := tcpTable6Rows(buf)
		if err != nil {
			return 0, err
		}
		rows = append(rows, r...)
	}
	return ownerPID(rows, client, server)
}

// lookupUID names the SID of the account whose process owns the client end of client -> server.
//
// The TCP table gives a PID, and a PID is only a name for a process while that process exists: one
// that exits can have its PID handed to another. So the process is opened first, and then the
// table is read again and must still name the same PID for the connection. Windows does not reuse
// a PID while a handle to its process is open, so if the connection still belongs to that PID once
// the handle is held, the handle is the process that owns it, and its token is the sender's. A
// process that exits before that closes its sockets, and the second read no longer names it.
//
// The SID is TokenUser of the process's primary token, the account it runs as. The collector runs
// as LocalSystem, which may open any process for PROCESS_QUERY_LIMITED_INFORMATION and query its
// token; a user-mode collector can open only its own account's processes, so another account's
// export is unknown there rather than misattributed.
func lookupUID(client, server *net.TCPAddr) (string, error) {
	pid, err := connectionOwner(client, server)
	if err != nil {
		return "", err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			// No such process: it exited after the table was read.
			return "", ErrNotFound
		}
		return "", fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(process)
	again, err := connectionOwner(client, server)
	if err != nil {
		return "", err
	}
	if again != pid {
		return "", ErrNotFound
	}
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", fmt.Errorf("open the token of process %d: %w", pid, err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read the token user of process %d: %w", pid, err)
	}
	if user.User.Sid == nil || !user.User.Sid.IsValid() {
		return "", ErrNotFound
	}
	return user.User.Sid.String(), nil
}
