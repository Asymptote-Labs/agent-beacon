package peercred

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
)

// The macOS lookup reads the kernel's TCP socket table from the net.inet.tcp.pcblist_n sysctl, the
// one netstat(1) reads. It is parsed here, apart from the sysctl itself, so the parser is tested on
// every platform with byte fixtures.
//
// The table is a struct xinpgen, then for each TCP protocol control block a run of records, each a
// length and a kind followed by the record and padded to 8 bytes (ROUNDUP64), then a closing
// xinpgen. The records come from xnu's bsd/netinet/in_pcblist.c (get_pcblist_n), in this order:
//
//	xinpcb_n    (XSO_INPCB)   addresses and ports
//	xsocket_n   (XSO_SOCKET)  the socket, with so_uid: the account whose credential created it
//	xsockbuf_n  x2            receive and send buffers
//	xsockstat_n (XSO_STATS)
//	xtcpcb_n    (XSO_TCPCB)   t_state
//
// The layouts are xnu's bsd/netinet/in_pcb.h, bsd/sys/socketvar.h and bsd/netinet/tcp_var.h, all
// declared under #pragma pack(4), so a u_int64_t can sit at a 4-byte offset. Only the fields below
// are read, each at its fixed offset in the record, and a record shorter than what is read from it
// is not used. Lengths, kinds, uids and states are in host byte order, which is little-endian on
// every Mac; ports are in network byte order, as in the kernel's pcb.
const (
	xsoSocket = 0x001 // XSO_SOCKET
	xsoInpcb  = 0x010 // XSO_INPCB
	xsoTcpcb  = 0x020 // XSO_TCPCB

	// sizeof(struct xinpgen): xig_len, xig_count, xig_gen, xig_sogen. A record no longer than this
	// is the closing xinpgen, which has no kind (netstat stops on the same test).
	xinpgenLen = 24

	// struct xinpcb_n.
	xinpcbFPort  = 16 // u_short inp_fport, network order
	xinpcbLPort  = 18 // u_short inp_lport, network order
	xinpcbVFlag  = 44 // u_char inp_vflag
	xinpcbFAddr  = 48 // inp_dependfaddr: in6_addr, or in_addr_4in6 with the IPv4 address last
	xinpcbLAddr  = 64 // inp_dependladdr, the same
	xinpcbMinLen = 80

	// struct xsocket_n.
	xsocketState  = 26 // short so_state
	xsocketFamily = 40 // int xso_family; 0 when the pcb has no socket
	xsocketUID    = 64 // uid_t so_uid
	xsocketMinLen = 68

	// struct xtcpcb_n.
	xtcpcbState  = 36 // int t_state
	xtcpcbMinLen = 40

	inpIPv4 = 0x1 // INP_IPV4
	inpIPv6 = 0x2 // INP_IPV6

	ssNoFDRef = 0x0001 // SS_NOFDREF: no file descriptor refers to the socket any more

	tcpsClosed   = 0  // TCPS_CLOSED
	tcpsListen   = 1  // TCPS_LISTEN
	tcpsTimeWait = 10 // TCPS_TIME_WAIT
)

// errMalformed is a table that does not parse: an answer about nothing, never a guess.
var errMalformed = errors.New("malformed socket table")

// pcbListUID finds, in a net.inet.tcp.pcblist_n table, the socket whose local end is client and whose
// remote end is server, and returns the uid that owns it.
//
// A socket nobody holds any more is passed over, as on Linux: one in TIME_WAIT or CLOSED, one whose
// last descriptor has been closed (SS_NOFDREF, which a FIN_WAIT socket left behind by close(2) has),
// and a pcb without a socket. Unlike Linux, xnu keeps the creator's uid on such a socket, so it
// would name the right account; it is still refused, because the sender has gone and a client that
// can close before the lookup could otherwise pick the moment its export is read.
func pcbListUID(buf []byte, client, server *net.TCPAddr) (string, error) {
	le := binary.LittleEndian
	if len(buf) < 8 {
		return "", errMalformed
	}
	first := int(le.Uint32(buf))
	if first < 8 || first > len(buf) {
		return "", fmt.Errorf("%w: header length %d", errMalformed, first)
	}
	if first == xinpgenLen && le.Uint32(buf[4:]) == 0 && roundup8(first) >= len(buf) {
		// xig_count 0: get_pcblist_n writes the opening xinpgen alone, with no closing one.
		return "", ErrNotFound
	}
	var inpcb, socket, tcpcb []byte
	found := ""
	flush := func() error {
		defer func() { inpcb, socket, tcpcb = nil, nil, nil }()
		uid, ok := pcbOwner(inpcb, socket, tcpcb, client, server)
		if !ok {
			return nil
		}
		if found != "" && found != uid {
			return ErrAmbiguous
		}
		found = uid
		return nil
	}
	closed := false
	for off := roundup8(first); off+8 <= len(buf); {
		n := int(le.Uint32(buf[off:]))
		if n <= xinpgenLen {
			// The closing xinpgen. A table cut short before it, or inside it, is not trusted.
			closed = n == xinpgenLen && off+xinpgenLen <= len(buf)
			break
		}
		if n > len(buf)-off {
			return "", fmt.Errorf("%w: record of %d bytes at %d overruns the table", errMalformed, n, off)
		}
		rec := buf[off : off+n]
		switch le.Uint32(rec[4:]) {
		case xsoInpcb:
			if err := flush(); err != nil {
				return "", err
			}
			inpcb = rec
		case xsoSocket:
			socket = rec
		case xsoTcpcb:
			tcpcb = rec
		}
		next := off + roundup8(n)
		if next <= off {
			return "", errMalformed
		}
		off = next
	}
	if !closed {
		return "", fmt.Errorf("%w: no closing record", errMalformed)
	}
	if err := flush(); err != nil {
		return "", err
	}
	if found == "" {
		return "", ErrNotFound
	}
	return found, nil
}

// pcbOwner is the uid of one pcb's socket when that pcb is the client end of client -> server and
// someone still holds it.
func pcbOwner(inpcb, socket, tcpcb []byte, client, server *net.TCPAddr) (string, bool) {
	le := binary.LittleEndian
	if len(inpcb) < xinpcbMinLen || len(socket) < xsocketMinLen || len(tcpcb) < xtcpcbMinLen {
		return "", false
	}
	if int(binary.BigEndian.Uint16(inpcb[xinpcbLPort:])) != client.Port ||
		int(binary.BigEndian.Uint16(inpcb[xinpcbFPort:])) != server.Port {
		return "", false
	}
	vflag := inpcb[xinpcbVFlag]
	if !pcbAddrIs(inpcb[xinpcbLAddr:xinpcbLAddr+16], vflag, client.IP) ||
		!pcbAddrIs(inpcb[xinpcbFAddr:xinpcbFAddr+16], vflag, server.IP) {
		return "", false
	}
	if le.Uint32(socket[xsocketFamily:]) == 0 {
		// sotoxsocket_n leaves the record zeroed when the pcb has no socket.
		return "", false
	}
	if le.Uint16(socket[xsocketState:])&ssNoFDRef != 0 {
		return "", false
	}
	switch int32(le.Uint32(tcpcb[xtcpcbState:])) {
	case tcpsClosed, tcpsListen, tcpsTimeWait:
		return "", false
	}
	return strconv.FormatUint(uint64(le.Uint32(socket[xsocketUID:])), 10), true
}

// pcbAddrIs reports whether a pcb address field holds ip. An IPv4 pcb keeps its address in the last
// four bytes (in_addr_4in6); an IPv6 one uses all sixteen, and net.IP.Equal matches an IPv4-mapped
// IPv6 address with its IPv4 form. A dual-stack socket connected to an IPv4 address is switched to
// INP_IPV4 by the kernel and stored the IPv4 way.
func pcbAddrIs(field []byte, vflag byte, ip net.IP) bool {
	if vflag&inpIPv4 != 0 && net.IP(field[12:16]).Equal(ip) {
		return true
	}
	return vflag&inpIPv6 != 0 && net.IP(field[:16]).Equal(ip)
}

func roundup8(n int) int {
	return (n + 7) &^ 7
}
