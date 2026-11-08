package peercred

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
)

// Fixtures for GetExtendedTcpTable's TCP_TABLE_OWNER_PID_ALL tables, byte for byte: addresses as
// the bytes on the wire, ports in network byte order in the low two bytes of their DWORD, every
// other DWORD little-endian.
type fixtureRow struct {
	local, remote *net.TCPAddr
	state, pid    uint32
}

func tcpTable4Fixture(rows ...fixtureRow) []byte {
	buf := make([]byte, 4+len(rows)*tcpRow4Size)
	binary.LittleEndian.PutUint32(buf, uint32(len(rows)))
	for i, r := range rows {
		row := buf[4+i*tcpRow4Size:]
		binary.LittleEndian.PutUint32(row[0:], r.state)
		copy(row[4:8], r.local.IP.To4())
		binary.BigEndian.PutUint16(row[8:], uint16(r.local.Port))
		copy(row[12:16], r.remote.IP.To4())
		binary.BigEndian.PutUint16(row[16:], uint16(r.remote.Port))
		binary.LittleEndian.PutUint32(row[20:], r.pid)
	}
	return buf
}

func tcpTable6Fixture(rows ...fixtureRow) []byte {
	buf := make([]byte, 4+len(rows)*tcpRow6Size)
	binary.LittleEndian.PutUint32(buf, uint32(len(rows)))
	for i, r := range rows {
		row := buf[4+i*tcpRow6Size:]
		copy(row[0:16], r.local.IP.To16())
		binary.BigEndian.PutUint16(row[20:], uint16(r.local.Port))
		copy(row[24:40], r.remote.IP.To16())
		binary.BigEndian.PutUint16(row[44:], uint16(r.remote.Port))
		binary.LittleEndian.PutUint32(row[48:], r.state)
		binary.LittleEndian.PutUint32(row[52:], r.pid)
	}
	return buf
}

// The byte order is the point of these fixtures: port 50123 is 0xC3CB, so the row holds CB C3 if
// it was ever written little-endian.
func TestTCPTable4RowByteOrder(t *testing.T) {
	raw := []byte{
		1, 0, 0, 0, // one row
		5, 0, 0, 0, // MIB_TCP_STATE_ESTAB
		127, 0, 0, 1, // local 127.0.0.1
		0xC3, 0xCB, 0, 0, // local port 50123
		127, 0, 0, 1, // remote 127.0.0.1
		0x10, 0xDE, 0, 0, // remote port 4318
		0x39, 0x30, 0, 0, // PID 12345
	}
	rows, err := tcpTable4Rows(raw)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	r := rows[0]
	if !r.local.IP.Equal(net.IPv4(127, 0, 0, 1)) || r.local.Port != 50123 || r.remote.Port != 4318 || r.state != 5 || r.pid != 12345 {
		t.Fatalf("row = %+v", r)
	}
}

func TestTCPTable6RowByteOrder(t *testing.T) {
	raw := make([]byte, 4+tcpRow6Size)
	raw[0] = 1
	row := raw[4:]
	row[15] = 1                   // ::1
	row[20], row[21] = 0xC3, 0xCB // local port 50123
	row[39] = 1                   // ::1
	row[44], row[45] = 0x10, 0xDE // remote port 4318
	row[48] = 5                   // ESTAB
	row[52], row[53] = 0x39, 0x30 // PID 12345
	rows, err := tcpTable6Rows(raw)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	r := rows[0]
	if !r.local.IP.Equal(net.IPv6loopback) || r.local.Port != 50123 || !r.remote.IP.Equal(net.IPv6loopback) || r.remote.Port != 4318 || r.state != 5 || r.pid != 12345 {
		t.Fatalf("row = %+v", r)
	}
}

func TestOwnerPIDFixtures(t *testing.T) {
	const estab = 5
	lo := net.IPv4(127, 0, 0, 1)
	client := &net.TCPAddr{IP: lo, Port: 50123}
	server := &net.TCPAddr{IP: lo, Port: 4318}
	other := &net.TCPAddr{IP: lo, Port: 8080}
	any4 := &net.TCPAddr{IP: net.IPv4zero}
	cases := []struct {
		name  string
		rows4 []fixtureRow
		rows6 []fixtureRow
		want  uint32
		err   error
	}{
		{
			name: "the client end among the listener and the collector's own end",
			rows4: []fixtureRow{
				{local: server, remote: any4, state: mibTCPStateListen, pid: 900},
				{local: server, remote: client, state: estab, pid: 900},
				{local: client, remote: server, state: estab, pid: 1234},
			},
			want: 1234,
		},
		{
			name:  "the collector's end alone is not the client",
			rows4: []fixtureRow{{local: server, remote: client, state: estab, pid: 900}},
			err:   ErrNotFound,
		},
		{
			name:  "TIME_WAIT is gone",
			rows4: []fixtureRow{{local: client, remote: server, state: mibTCPStateTimeWait, pid: 0}},
			err:   ErrNotFound,
		},
		{
			name:  "TIME_WAIT is refused even if a PID is reported",
			rows4: []fixtureRow{{local: client, remote: server, state: mibTCPStateTimeWait, pid: 1234}},
			err:   ErrNotFound,
		},
		{
			name:  "FIN_WAIT1: the client closed its end, and the PID may be an exited process's",
			rows4: []fixtureRow{{local: client, remote: server, state: mibTCPStateFinWait1, pid: 1234}},
			err:   ErrNotFound,
		},
		{
			name:  "FIN_WAIT2 is a closed client end",
			rows4: []fixtureRow{{local: client, remote: server, state: mibTCPStateFinWait2, pid: 1234}},
			err:   ErrNotFound,
		},
		{
			name:  "CLOSING is a closed client end",
			rows4: []fixtureRow{{local: client, remote: server, state: mibTCPStateClosing, pid: 1234}},
			err:   ErrNotFound,
		},
		{
			name:  "LAST_ACK is a closed client end",
			rows4: []fixtureRow{{local: client, remote: server, state: mibTCPStateLastAck, pid: 1234}},
			err:   ErrNotFound,
		},
		{
			name:  "a closed client end in the IPv6 table is not an answer either",
			rows6: []fixtureRow{{local: &net.TCPAddr{IP: mapped(lo.To4()), Port: client.Port}, remote: &net.TCPAddr{IP: mapped(lo.To4()), Port: server.Port}, state: mibTCPStateFinWait2, pid: 2222}},
			err:   ErrNotFound,
		},
		{
			name: "a closed row does not make a live one ambiguous",
			rows4: []fixtureRow{
				{local: client, remote: server, state: mibTCPStateLastAck, pid: 9999},
				{local: client, remote: server, state: estab, pid: 1234},
			},
			want: 1234,
		},
		{
			name:  "CLOSE_WAIT is still held by the client (the collector closed first)",
			rows4: []fixtureRow{{local: client, remote: server, state: 8, pid: 1234}},
			want:  1234,
		},
		{
			name:  "PID 0 is nobody",
			rows4: []fixtureRow{{local: client, remote: server, state: estab, pid: 0}},
			err:   ErrNotFound,
		},
		{
			name:  "PID 4 is the System process, not a sender",
			rows4: []fixtureRow{{local: client, remote: server, state: estab, pid: 4}},
			err:   ErrNotFound,
		},
		{
			name: "an unrelated TIME_WAIT on the same port does not stop the lookup",
			rows4: []fixtureRow{
				{local: client, remote: other, state: mibTCPStateTimeWait},
				{local: client, remote: server, state: estab, pid: 1234},
			},
			want: 1234,
		},
		{
			name:  "another process's socket on the same port to another service is not this client",
			rows4: []fixtureRow{{local: client, remote: other, state: estab, pid: 77}},
			err:   ErrNotFound,
		},
		{
			name: "two owners of the same connection is ambiguous",
			rows4: []fixtureRow{
				{local: client, remote: server, state: estab, pid: 1234},
				{local: client, remote: server, state: estab, pid: 5678},
			},
			err: ErrAmbiguous,
		},
		{
			name:  "a dual-stack socket appears in the IPv6 table as IPv4-mapped",
			rows6: []fixtureRow{{local: &net.TCPAddr{IP: mapped(lo.To4()), Port: client.Port}, remote: &net.TCPAddr{IP: mapped(lo.To4()), Port: server.Port}, state: estab, pid: 2222}},
			want:  2222,
		},
		{
			name:  "disagreeing tables are ambiguous",
			rows4: []fixtureRow{{local: client, remote: server, state: estab, pid: 1234}},
			rows6: []fixtureRow{{local: &net.TCPAddr{IP: mapped(lo.To4()), Port: client.Port}, remote: &net.TCPAddr{IP: mapped(lo.To4()), Port: server.Port}, state: estab, pid: 2222}},
			err:   ErrAmbiguous,
		},
		{
			name:  "another port is not this client",
			rows4: []fixtureRow{{local: &net.TCPAddr{IP: lo, Port: client.Port + 1}, remote: server, state: estab, pid: 1234}},
			err:   ErrNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r4, err := tcpTable4Rows(tcpTable4Fixture(tc.rows4...))
			if err != nil {
				t.Fatal(err)
			}
			r6, err := tcpTable6Rows(tcpTable6Fixture(tc.rows6...))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := ownerPID(append(r4, r6...), client, server)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("ownerPID = %d, %v; want %v", pid, err, tc.err)
				}
				return
			}
			if err != nil || pid != tc.want {
				t.Fatalf("ownerPID = %d, %v; want %d", pid, err, tc.want)
			}
		})
	}
	v6c := &net.TCPAddr{IP: net.IPv6loopback, Port: 50123}
	v6s := &net.TCPAddr{IP: net.IPv6loopback, Port: 4318}
	r6, _ := tcpTable6Rows(tcpTable6Fixture(fixtureRow{local: v6c, remote: v6s, state: estab, pid: 3333}))
	if pid, err := ownerPID(r6, v6c, v6s); err != nil || pid != 3333 {
		t.Fatalf("IPv6 loopback: %d, %v; want 3333", pid, err)
	}
}

func TestTCPTableRefusesMalformedTables(t *testing.T) {
	if _, err := tcpTable4Rows([]byte{1, 0}); !errors.Is(err, errMalformed) {
		t.Fatalf("short table err = %v", err)
	}
	// A count the buffer cannot hold is refused rather than read past the end.
	raw := tcpTable4Fixture(fixtureRow{local: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}})
	binary.LittleEndian.PutUint32(raw, 2)
	if _, err := tcpTable4Rows(raw); !errors.Is(err, errMalformed) {
		t.Fatalf("overcounted IPv4 table err = %v", err)
	}
	raw6 := tcpTable6Fixture()
	binary.LittleEndian.PutUint32(raw6, 0xFFFFFFFF)
	if _, err := tcpTable6Rows(raw6); !errors.Is(err, errMalformed) {
		t.Fatalf("overcounted IPv6 table err = %v", err)
	}
}
