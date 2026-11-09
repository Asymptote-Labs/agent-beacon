package peercred

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
)

// Fixtures for the macOS net.inet.tcp.pcblist_n table, laid out as xnu writes it: the record sizes
// are the real sizeof() of each pack(4) struct, each record padded to 8 bytes.
const (
	fixtureXinpcbLen   = 104 // sizeof(struct xinpcb_n)
	fixtureXsocketLen  = 104 // sizeof(struct xsocket_n)
	fixtureXsockbufLen = 32  // sizeof(struct xsockbuf_n)
	fixtureXsockstat   = 136 // sizeof(struct xsockstat_n)
	fixtureXtcpcbLen   = 204 // sizeof(struct xtcpcb_n)

	xsoRcvbuf = 0x002
	xsoSndbuf = 0x004
	xsoStats  = 0x008

	tcpsEstablished = 4
	tcpsFinWait1    = 6
	afInet          = 2
	afInet6         = 30
)

type fixturePCB struct {
	local, foreign *net.TCPAddr
	ipv6           bool // an AF_INET6 pcb (INP_IPV6); otherwise INP_IPV4
	uid            uint32
	state          int32
	soState        uint16
	noSocket       bool // the pcb has no socket: sotoxsocket_n leaves the record zeroed
	skipTCPCB      bool
}

func appendRecord(buf []byte, length int, kind uint32, fill func(rec []byte)) []byte {
	rec := make([]byte, roundup8(length))
	binary.LittleEndian.PutUint32(rec[0:], uint32(length))
	binary.LittleEndian.PutUint32(rec[4:], kind)
	if fill != nil {
		fill(rec[:length])
	}
	return append(buf, rec...)
}

func putPCBAddr(field []byte, ip net.IP, ipv6 bool) {
	if ipv6 {
		copy(field[:16], ip.To16())
		return
	}
	copy(field[12:16], ip.To4())
}

func pcbListFixture(pcbs ...fixturePCB) []byte {
	xinpgen := func(buf []byte, count int) []byte {
		rec := make([]byte, xinpgenLen)
		binary.LittleEndian.PutUint32(rec[0:], xinpgenLen)
		binary.LittleEndian.PutUint32(rec[4:], uint32(count)) // xig_count, not a kind
		binary.LittleEndian.PutUint64(rec[8:], 77)
		binary.LittleEndian.PutUint64(rec[16:], 88)
		return append(buf, rec...)
	}
	buf := xinpgen(nil, len(pcbs))
	for _, p := range pcbs {
		buf = appendRecord(buf, fixtureXinpcbLen, xsoInpcb, func(r []byte) {
			binary.LittleEndian.PutUint64(r[8:], 0xfeedface) // xi_inpp
			binary.BigEndian.PutUint16(r[xinpcbFPort:], uint16(p.foreign.Port))
			binary.BigEndian.PutUint16(r[xinpcbLPort:], uint16(p.local.Port))
			binary.LittleEndian.PutUint64(r[20:], 0xcafef00d) // inp_ppcb, at a 4-byte offset
			vflag := byte(inpIPv4)
			if p.ipv6 {
				vflag = inpIPv6
			}
			r[xinpcbVFlag] = vflag
			r[46] = 6 // inp_ip_p
			putPCBAddr(r[xinpcbFAddr:xinpcbFAddr+16], p.foreign.IP, p.ipv6)
			putPCBAddr(r[xinpcbLAddr:xinpcbLAddr+16], p.local.IP, p.ipv6)
		})
		buf = appendRecord(buf, fixtureXsocketLen, xsoSocket, func(r []byte) {
			if p.noSocket {
				return
			}
			binary.LittleEndian.PutUint64(r[8:], 0xabad1dea) // xso_so
			binary.LittleEndian.PutUint16(r[16:], 1)         // so_type SOCK_STREAM
			binary.LittleEndian.PutUint16(r[xsocketState:], p.soState)
			binary.LittleEndian.PutUint32(r[36:], 6) // xso_protocol
			family := uint32(afInet)
			if p.ipv6 {
				family = afInet6
			}
			binary.LittleEndian.PutUint32(r[xsocketFamily:], family)
			binary.LittleEndian.PutUint32(r[xsocketUID:], p.uid)
			binary.LittleEndian.PutUint32(r[68:], 4242) // so_last_pid
		})
		buf = appendRecord(buf, fixtureXsockbufLen, xsoRcvbuf, nil)
		buf = appendRecord(buf, fixtureXsockbufLen, xsoSndbuf, nil)
		buf = appendRecord(buf, fixtureXsockstat, xsoStats, nil)
		if !p.skipTCPCB {
			buf = appendRecord(buf, fixtureXtcpcbLen, xsoTcpcb, func(r []byte) {
				binary.LittleEndian.PutUint32(r[xtcpcbState:], uint32(p.state))
			})
		}
	}
	return xinpgen(buf, len(pcbs))
}

func TestPCBListFixtures(t *testing.T) {
	lo := net.IPv4(127, 0, 0, 1)
	client := &net.TCPAddr{IP: lo, Port: 50123}
	server := &net.TCPAddr{IP: lo, Port: 4318}
	other := &net.TCPAddr{IP: lo, Port: 8080}
	established := func(local, foreign *net.TCPAddr, uid uint32) fixturePCB {
		return fixturePCB{local: local, foreign: foreign, uid: uid, state: tcpsEstablished}
	}
	listener := fixturePCB{local: server, foreign: &net.TCPAddr{IP: net.IPv4zero, Port: 0}, uid: 0, state: tcpsListen}

	cases := []struct {
		name           string
		client, server *net.TCPAddr
		pcbs           []fixturePCB
		want           string
		err            error
	}{
		{
			name: "the client end among the listener and the collector's own end",
			pcbs: []fixturePCB{listener, established(server, client, 0), established(client, server, 501)},
			want: "501",
		},
		{
			name: "the collector's end of the same connection is not the client",
			pcbs: []fixturePCB{listener, established(server, client, 0)},
			err:  ErrNotFound,
		},
		{
			name: "a socket in TIME_WAIT is gone, though xnu still knows its creator",
			pcbs: []fixturePCB{{local: client, foreign: server, uid: 501, state: tcpsTimeWait, soState: ssNoFDRef}},
			err:  ErrNotFound,
		},
		{
			name: "TIME_WAIT is refused on its own",
			pcbs: []fixturePCB{{local: client, foreign: server, uid: 501, state: tcpsTimeWait}},
			err:  ErrNotFound,
		},
		{
			name: "a socket closed by its owner and left in FIN_WAIT is not an answer",
			pcbs: []fixturePCB{{local: client, foreign: server, uid: 501, state: tcpsFinWait1, soState: ssNoFDRef}},
			err:  ErrNotFound,
		},
		{
			name: "a half-closed socket its owner still holds is",
			pcbs: []fixturePCB{{local: client, foreign: server, uid: 501, state: tcpsFinWait1}},
			want: "501",
		},
		{
			name: "a pcb without a socket reads uid 0 and is not root",
			pcbs: []fixturePCB{{local: client, foreign: server, state: tcpsEstablished, noSocket: true}},
			err:  ErrNotFound,
		},
		{
			name: "a pcb whose tcpcb is missing is not used",
			pcbs: []fixturePCB{{local: client, foreign: server, uid: 501, state: tcpsEstablished, skipTCPCB: true}},
			err:  ErrNotFound,
		},
		{
			name: "an unrelated TIME_WAIT on the same port does not stop the lookup",
			pcbs: []fixturePCB{
				{local: client, foreign: other, uid: 0, state: tcpsTimeWait, soState: ssNoFDRef},
				established(client, server, 501),
			},
			want: "501",
		},
		{
			name: "another account's socket on the same port to another service is not this client",
			pcbs: []fixturePCB{established(client, other, 502), established(client, server, 501)},
			want: "501",
		},
		{
			name: "and when this client has gone, that socket is still not named",
			pcbs: []fixturePCB{established(client, other, 502)},
			err:  ErrNotFound,
		},
		{
			name: "two owners of the same connection is ambiguous",
			pcbs: []fixturePCB{established(client, server, 501), established(client, server, 502)},
			err:  ErrAmbiguous,
		},
		{
			name: "the same owner twice is still that owner",
			pcbs: []fixturePCB{established(client, server, 501), established(client, server, 501)},
			want: "501",
		},
		{
			name: "another port is not this client",
			pcbs: []fixturePCB{established(&net.TCPAddr{IP: lo, Port: client.Port + 1}, server, 501)},
			err:  ErrNotFound,
		},
		{
			name: "another address is not this client",
			pcbs: []fixturePCB{established(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 2), Port: client.Port}, server, 501)},
			err:  ErrNotFound,
		},
		{
			name:   "an IPv6 loopback connection",
			client: &net.TCPAddr{IP: net.IPv6loopback, Port: 50123},
			server: &net.TCPAddr{IP: net.IPv6loopback, Port: 4318},
			pcbs: []fixturePCB{{
				local: &net.TCPAddr{IP: net.IPv6loopback, Port: 50123}, foreign: &net.TCPAddr{IP: net.IPv6loopback, Port: 4318},
				ipv6: true, uid: 503, state: tcpsEstablished,
			}},
			want: "503",
		},
		{
			name: "an IPv4 client seen by a dual-stack listener as IPv4-mapped",
			pcbs: []fixturePCB{{
				local: &net.TCPAddr{IP: mapped(lo.To4()), Port: client.Port}, foreign: &net.TCPAddr{IP: mapped(lo.To4()), Port: server.Port},
				ipv6: true, uid: 504, state: tcpsEstablished,
			}},
			want: "504",
		},
		{
			name:   "an IPv4 pcb's padding is not read as an IPv6 address",
			client: &net.TCPAddr{IP: net.ParseIP("::127.0.0.1"), Port: 50123},
			pcbs:   []fixturePCB{established(client, server, 501)},
			err:    ErrNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, s := client, server
			if tc.client != nil {
				c = tc.client
			}
			if tc.server != nil {
				s = tc.server
			}
			uid, err := pcbListUID(pcbListFixture(tc.pcbs...), c, s)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("pcbListUID = %q, %v; want %v", uid, err, tc.err)
				}
				return
			}
			if err != nil || uid != tc.want {
				t.Fatalf("pcbListUID = %q, %v; want %s", uid, err, tc.want)
			}
		})
	}
}

// A table that does not parse is an error, never an answer, and never reads past its end.
func TestPCBListRefusesMalformedTables(t *testing.T) {
	client := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50123}
	server := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4318}
	good := pcbListFixture(fixturePCB{local: client, foreign: server, uid: 501, state: tcpsEstablished})

	if _, err := pcbListUID(nil, client, server); !errors.Is(err, errMalformed) {
		t.Fatalf("empty table err = %v, want errMalformed", err)
	}
	header := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(header, uint32(len(good)+1))
	if _, err := pcbListUID(header, client, server); !errors.Is(err, errMalformed) {
		t.Fatalf("oversized header err = %v, want errMalformed", err)
	}
	overrun := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(overrun[xinpgenLen:], uint32(len(good)))
	if _, err := pcbListUID(overrun, client, server); !errors.Is(err, errMalformed) {
		t.Fatalf("overrunning record err = %v, want errMalformed", err)
	}
	// Every truncation either errors or finds nothing; none panics or names an account it cut off.
	for n := 0; n < len(good); n++ {
		if uid, err := pcbListUID(good[:n], client, server); err == nil {
			t.Fatalf("truncated to %d bytes: %q, want no answer", n, uid)
		}
	}
	// A table with no pcbs at all is the opening xinpgen alone, with xig_count 0.
	if _, err := pcbListUID(pcbListFixture()[:xinpgenLen], client, server); !errors.Is(err, ErrNotFound) {
		t.Fatalf("zero-count table err = %v, want ErrNotFound", err)
	}
	// An empty table (two xinpgens and nothing between) names nobody.
	if _, err := pcbListUID(pcbListFixture(), client, server); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty table err = %v, want ErrNotFound", err)
	}
}
