//go:build windows

package localingest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

var pipeSeq atomic.Int64

// testPipe is a pipe name no other test, and no other run on the machine, uses.
func testPipe(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`\\.\pipe\beacon-ingest-test-%d-%d-%d`, os.Getpid(), time.Now().UnixNano(), pipeSeq.Add(1))
}

// currentSID is the account this test runs as.
func currentSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

type pipeHarness struct {
	server *Server
	rec    *recorder
	client Client
	path   string
}

// startPipeServer serves a fresh pipe as this test's account. The client trusts the test's account
// as the pipe's owner, the way a production client trusts SYSTEM's.
func startPipeServer(t *testing.T, configure func(*Server)) *pipeHarness {
	t.Helper()
	path := testPipe(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	s := &Server{Append: rec.append, LookupName: func(uid string) string { return "name-of-" + uid }}
	if configure != nil {
		configure(s)
	}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close() })
	return &pipeHarness{server: s, rec: rec, path: path,
		client: Client{SocketPath: path, TrustedSID: currentSID(t), Timeout: 5 * time.Second}}
}

func TestPipeRoundTripStampsTheSendersAccount(t *testing.T) {
	h := startPipeServer(t, nil)
	resp, err := h.client.Send(lines(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != 3 || resp.Processed != 3 {
		t.Fatalf("response = %+v", resp)
	}
	if h.rec.count() != 3 {
		t.Fatalf("recorded %d lines", h.rec.count())
	}
	sid := currentSID(t)
	for _, line := range h.rec.lines {
		user := decode(t, line)["user"].(map[string]interface{})
		if user["uid"] != sid || user["name"] != "name-of-"+sid {
			t.Fatalf("user = %#v; the line claimed root, and must carry this account (%s) instead", user, sid)
		}
	}
}

func TestPipeManyBatchesAndProbe(t *testing.T) {
	h := startPipeServer(t, nil)
	if err := h.client.Probe(); err != nil {
		t.Fatalf("Probe = %v", err)
	}
	resp, err := h.client.Send(lines(t, MaxLinesPerConn+5))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != MaxLinesPerConn+5 || h.rec.count() != MaxLinesPerConn+5 {
		t.Fatalf("response %+v, recorded %d", resp, h.rec.count())
	}
}

func TestPipeWithoutAServer(t *testing.T) {
	c := Client{SocketPath: testPipe(t), Timeout: time.Second}
	if _, err := c.Send(lines(t, 1)); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Send = %v, want ErrNoSocket", err)
	}
	if err := c.Probe(); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Probe = %v, want ErrNoSocket", err)
	}
}

func TestPipeLogMismatch(t *testing.T) {
	h := startPipeServer(t, func(s *Server) { s.LogPath = `C:\ProgramData\Beacon\Endpoint\logs\runtime.jsonl` })
	c := h.client
	c.LogPath = `C:\elsewhere\runtime.jsonl`
	if _, err := c.Send(lines(t, 1)); !errors.Is(err, ErrLogMismatch) {
		t.Fatalf("Send = %v, want ErrLogMismatch", err)
	}
	if h.rec.count() != 0 {
		t.Fatal("wrote a line meant for another log")
	}
	c.LogPath = h.server.LogPath
	if err := c.Probe(); err != nil {
		t.Fatalf("Probe for the served log = %v", err)
	}
}

// A second Listen on a name already served fails: a collector cannot be started alongside another,
// and an account that created the pipe first cannot have the collector serve its instance.
func TestPipeListenRefusesANameAlreadyTaken(t *testing.T) {
	path := testPipe(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if second, err := Listen(path); err == nil {
		_ = second.Close()
		t.Fatal("a second Listen on the same pipe succeeded")
	}
}

func TestListenRefusesAPathThatIsNotAPipe(t *testing.T) {
	if l, err := Listen(`C:\Windows\Temp\ingest.sock`); err == nil {
		_ = l.Close()
		t.Fatal("Listen accepted a path outside the pipe namespace")
	}
}

// The client sends only to a pipe SYSTEM or Administrators created. A pipe this test created is
// owned by its account -- or by Administrators when the test runs elevated, which is trusted -- so
// what the client must do depends on which; both are asserted.
func TestPipeClientChecksThePipesOwner(t *testing.T) {
	h := startPipeServer(t, nil)
	conn, err := winio.DialPipeAccessImpLevel(context.Background(), h.path, clientPipeAccess, winio.PipeImpLevelIdentification)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := pipeOwner(conn)
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	untrusting := h.client
	untrusting.TrustedSID = ""
	_, err = untrusting.Send(lines(t, 1))
	switch owner {
	case sidLocalSystem, sidAdministrators:
		if err != nil {
			t.Fatalf("pipe owned by %s refused: %v", owner, err)
		}
	default:
		if !errors.Is(err, ErrUntrustedSocket) {
			t.Fatalf("pipe owned by %s: Send = %v, want ErrUntrustedSocket", owner, err)
		}
		if h.rec.count() != 0 {
			t.Fatal("sent an event to a pipe an ordinary account created")
		}
	}
}

func TestTrustedPipeOwner(t *testing.T) {
	c := Client{}
	for sid, want := range map[string]bool{
		sidLocalSystem:        true,
		sidAdministrators:     true,
		"S-1-5-32-545":        false, // Users
		"S-1-5-11":            false, // Authenticated Users
		"S-1-5-21-1-2-3-1001": false,
		"":                    false,
	} {
		if got := c.trustedPipeOwner(sid); got != want {
			t.Errorf("trustedPipeOwner(%q) = %v, want %v", sid, got, want)
		}
	}
	c.TrustedSID = "S-1-5-21-1-2-3-1001"
	if !c.trustedPipeOwner("s-1-5-21-1-2-3-1001") {
		t.Error("TrustedSID is compared without regard to case")
	}
}

// The pipe denies network logons ahead of every grant, and gives ordinary accounts no right to add
// an instance of their own.
func TestPipeSecurity(t *testing.T) {
	h := startPipeServer(t, nil)
	conn, err := winio.DialPipeAccessImpLevel(context.Background(), h.path, clientPipeAccess, winio.PipeImpLevelIdentification)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	handle, err := handleOf(conn)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("DACL: %v", err)
	}
	var sawNetworkDeny bool
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			if sid == "S-1-5-2" {
				if i != 0 {
					t.Errorf("the network deny is entry %d; it must come before every grant", i)
				}
				sawNetworkDeny = true
			}
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			// S-1-3-4 is OWNER RIGHTS: the pipe's owner, which is SYSTEM for the collector.
			if sid != sidLocalSystem && sid != sidAdministrators && sid != "S-1-3-4" && ace.Mask&windows.FILE_APPEND_DATA != 0 {
				t.Errorf("%s may create pipe instances (mask %#x)", sid, ace.Mask)
			}
		}
	}
	if !sawNetworkDeny {
		t.Error("no entry denies network logons")
	}
}

// identify reads the client's own token once the server has read from the pipe, and agrees with
// the account the connection was counted against.
func TestIdentifyReadsTheClientsAccount(t *testing.T) {
	path := testPipe(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	type got struct {
		account string
		peer    Peer
		err     error
	}
	result := make(chan got, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			result <- got{err: err}
			return
		}
		defer conn.Close()
		account, err := connectingAccount(conn)
		if err != nil {
			result <- got{err: err}
			return
		}
		if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
			result <- got{err: err}
			return
		}
		peer, err := identify(conn)
		result <- got{account: account, peer: peer, err: err}
	}()
	conn, err := winio.DialPipeAccessImpLevel(context.Background(), path, clientPipeAccess, winio.PipeImpLevelIdentification)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(Preamble + "\n")); err != nil {
		t.Fatal(err)
	}
	r := <-result
	if r.err != nil {
		t.Fatal(r.err)
	}
	sid := currentSID(t)
	if r.account != sid || r.peer.UID != sid {
		t.Fatalf("account %q, peer %q; want both %q", r.account, r.peer.UID, sid)
	}
	if r.peer.PID != int32(os.Getpid()) {
		t.Fatalf("peer pid %d, want %d", r.peer.PID, os.Getpid())
	}
}

// A client that connects anonymously gives the server nothing to stamp, and is refused.
func TestPipeRefusesAnAnonymousClient(t *testing.T) {
	h := startPipeServer(t, nil)
	conn, err := winio.DialPipeAccessImpLevel(context.Background(), h.path, clientPipeAccess, winio.PipeImpLevelAnonymous)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := opening + string(lines(t, 1)[0]) + "\n"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if hc, ok := conn.(halfCloser); ok {
		_ = hc.CloseWrite()
	}
	reply, _ := bufio.NewReader(conn).ReadString('\n')
	var resp Response
	if strings.TrimSpace(reply) != "" {
		if err := json.Unmarshal([]byte(reply), &resp); err != nil {
			t.Fatalf("reply %q: %v", reply, err)
		}
	}
	if resp.Accepted != 0 || h.rec.count() != 0 {
		t.Fatalf("an anonymous client's event was written: reply %q", reply)
	}
}

func TestPipeRefusesAConnectionWithoutThePreamble(t *testing.T) {
	h := startPipeServer(t, nil)
	conn, err := winio.DialPipeAccessImpLevel(context.Background(), h.path, clientPipeAccess, winio.PipeImpLevelIdentification)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("GET / HTTP/1.1\n" + string(lines(t, 1)[0]) + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = conn.(halfCloser).CloseWrite()
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, Preamble) || h.rec.count() != 0 {
		t.Fatalf("reply %q, recorded %d", reply, h.rec.count())
	}
}

func TestPipeServerCloseStopsServing(t *testing.T) {
	h := startPipeServer(t, nil)
	if _, err := h.client.Send(lines(t, 1)); err != nil {
		t.Fatal(err)
	}
	if err := h.server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Send(lines(t, 1)); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Send after Close = %v, want ErrNoSocket: a closed server leaves no pipe behind", err)
	}
}
