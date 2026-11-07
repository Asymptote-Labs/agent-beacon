package peercred

import (
	"encoding/binary"
	"fmt"
	"net"
)

// The Windows lookup reads the TCP tables GetExtendedTcpTable returns with TCP_TABLE_OWNER_PID_ALL:
// a MIB_TCPTABLE_OWNER_PID (AF_INET) or MIB_TCP6TABLE_OWNER_PID (AF_INET6), each a DWORD count
// followed by that many rows. They are parsed here, apart from the call, so the parser is tested on
// every platform with byte fixtures.
//
// MIB_TCPROW_OWNER_PID, 24 bytes:
//
//	0  dwState
//	4  dwLocalAddr   the address in network byte order
//	8  dwLocalPort   the port in network byte order in the low 16 bits (the first two bytes)
//	12 dwRemoteAddr
//	16 dwRemotePort
//	20 dwOwningPid
//
// MIB_TCP6ROW_OWNER_PID, 56 bytes:
//
//	0  ucLocalAddr[16]
//	16 dwLocalScopeId
//	20 dwLocalPort
//	24 ucRemoteAddr[16]
//	40 dwRemoteScopeId
//	44 dwRemotePort
//	48 dwState
//	52 dwOwningPid
//
// DWORDs other than addresses and ports are little-endian, as on every Windows Beacon supports.
const (
	tcpRow4Size = 24
	tcpRow6Size = 56

	mibTCPStateClosed    = 1  // MIB_TCP_STATE_CLOSED
	mibTCPStateListen    = 2  // MIB_TCP_STATE_LISTEN
	mibTCPStateFinWait1  = 6  // MIB_TCP_STATE_FIN_WAIT1
	mibTCPStateFinWait2  = 7  // MIB_TCP_STATE_FIN_WAIT2
	mibTCPStateClosing   = 9  // MIB_TCP_STATE_CLOSING
	mibTCPStateLastAck   = 10 // MIB_TCP_STATE_LAST_ACK
	mibTCPStateTimeWait  = 11 // MIB_TCP_STATE_TIME_WAIT
	mibTCPStateDeleteTCB = 12 // MIB_TCP_STATE_DELETE_TCB

	pidSystemIdle = 0
	pidSystem     = 4
)

// tcpOwnerRow is one row of either table.
type tcpOwnerRow struct {
	local, remote net.TCPAddr
	state         uint32
	pid           uint32
}

func tcpTableRows(buf []byte, rowSize int, parse func([]byte) tcpOwnerRow) ([]tcpOwnerRow, error) {
	if len(buf) < 4 {
		return nil, errMalformed
	}
	n := int(binary.LittleEndian.Uint32(buf))
	if n < 0 || n > (len(buf)-4)/rowSize {
		return nil, fmt.Errorf("%w: %d rows do not fit in %d bytes", errMalformed, n, len(buf))
	}
	rows := make([]tcpOwnerRow, 0, n)
	for i := 0; i < n; i++ {
		off := 4 + i*rowSize
		rows = append(rows, parse(buf[off:off+rowSize]))
	}
	return rows, nil
}

// tcpTable4Rows parses a MIB_TCPTABLE_OWNER_PID.
func tcpTable4Rows(buf []byte) ([]tcpOwnerRow, error) {
	return tcpTableRows(buf, tcpRow4Size, func(r []byte) tcpOwnerRow {
		return tcpOwnerRow{
			state:  binary.LittleEndian.Uint32(r[0:]),
			local:  net.TCPAddr{IP: net.IP(append([]byte(nil), r[4:8]...)), Port: int(binary.BigEndian.Uint16(r[8:]))},
			remote: net.TCPAddr{IP: net.IP(append([]byte(nil), r[12:16]...)), Port: int(binary.BigEndian.Uint16(r[16:]))},
			pid:    binary.LittleEndian.Uint32(r[20:]),
		}
	})
}

// tcpTable6Rows parses a MIB_TCP6TABLE_OWNER_PID.
func tcpTable6Rows(buf []byte) ([]tcpOwnerRow, error) {
	return tcpTableRows(buf, tcpRow6Size, func(r []byte) tcpOwnerRow {
		return tcpOwnerRow{
			local:  net.TCPAddr{IP: net.IP(append([]byte(nil), r[0:16]...)), Port: int(binary.BigEndian.Uint16(r[20:]))},
			remote: net.TCPAddr{IP: net.IP(append([]byte(nil), r[24:40]...)), Port: int(binary.BigEndian.Uint16(r[44:]))},
			state:  binary.LittleEndian.Uint32(r[48:]),
			pid:    binary.LittleEndian.Uint32(r[52:]),
		}
	})
}

// ownerPID finds the process that owns the client end of client -> server among rows: the row whose
// local end is client and remote end is server. Rows nobody owns any more are passed over (TIME_WAIT
// and the like report PID 0), as is one owned by the System process (PID 4), whose token names
// SYSTEM rather than whoever caused the connection.
//
// So is a client end that has already been closed: FIN_WAIT1, FIN_WAIT2, CLOSING and LAST_ACK are
// the states a socket reaches only after its own side closed it. Windows can keep the PID of a
// process that has exited on such an orphaned row, and once that PID is handed to another process
// the re-read in lookupUID would still match and name the new process's account. A client that is
// still sending its export has not closed its end, so these are never the answer (the macOS
// equivalent is the SS_NOFDREF skip). Two rows with different owners are ambiguous;
// none is not found.
func ownerPID(rows []tcpOwnerRow, client, server *net.TCPAddr) (uint32, error) {
	var found uint32
	have := false
	for _, row := range rows {
		if row.local.Port != client.Port || row.remote.Port != server.Port ||
			!row.local.IP.Equal(client.IP) || !row.remote.IP.Equal(server.IP) {
			continue
		}
		switch row.state {
		case mibTCPStateClosed, mibTCPStateListen, mibTCPStateTimeWait, mibTCPStateDeleteTCB,
			mibTCPStateFinWait1, mibTCPStateFinWait2, mibTCPStateClosing, mibTCPStateLastAck:
			continue
		}
		if row.pid == pidSystemIdle || row.pid == pidSystem {
			continue
		}
		if have && found != row.pid {
			return 0, ErrAmbiguous
		}
		found, have = row.pid, true
	}
	if !have {
		return 0, ErrNotFound
	}
	return found, nil
}
